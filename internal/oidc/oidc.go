// Package oidc signs dashboard users in through an OpenID Connect provider
// (Claustra): authorization code with PKCE, client_secret_basic, RS256 ID
// tokens. It keeps its own session in a signed cookie and never stores the
// provider's tokens.
package oidc

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	flowCookie    = "nebula_oidc"
	sessionCookie = "nebula_session"
	flowTTL       = 10 * time.Minute
	SessionTTL    = 30 * 24 * time.Hour
)

type Config struct {
	Issuer       string // e.g. https://claustra.marcusson.dev
	ClientID     string
	ClientSecret string
	PublicURL    string   // e.g. https://nebula.marcusson.dev; the callback is /auth/callback
	Owner        string   // archive owner every signed-in user acts as
	Emails       []string // optional: also require one of these verified emails
	Key          []byte   // HMAC key for cookies, at least 32 bytes
}

type Provider struct {
	cfg    Config
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	discovery *discovery
	keys      map[string]*rsa.PublicKey
	keysAt    time.Time
}

type discovery struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Userinfo      string `json:"userinfo_endpoint"`
	JWKS          string `json:"jwks_uri"`
}

func New(cfg Config) (*Provider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.PublicURL == "" || cfg.Owner == "" {
		return nil, errors.New("OIDC needs an issuer, client id, client secret, public URL, and owner")
	}
	if len(cfg.Key) < 32 {
		return nil, errors.New("OIDC cookie key must be at least 32 bytes")
	}
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	return &Provider{cfg: cfg, now: time.Now, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (p *Provider) RedirectURI() string { return p.cfg.PublicURL + "/auth/callback" }

// Origin is the dashboard's own origin, required on cookie-authenticated writes.
func (p *Provider) Origin() string {
	u, _ := url.Parse(p.cfg.PublicURL)
	return u.Scheme + "://" + u.Host
}

func (p *Provider) secure() bool { return strings.HasPrefix(p.cfg.PublicURL, "https://") }

// --- signed values ---------------------------------------------------------

func (p *Provider) sign(fields ...string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, "\x00")))
	mac := hmac.New(sha256.New, p.cfg.Key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *Provider) verify(value string) ([]string, bool) {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok {
		return nil, false
	}
	mac := hmac.New(sha256.New, p.cfg.Key)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	return strings.Split(string(raw), "\x00"), true
}

func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// --- provider metadata -----------------------------------------------------

func (p *Provider) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s returned HTTP %d", u, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

func (p *Provider) meta(ctx context.Context) (*discovery, error) {
	p.mu.Lock()
	d := p.discovery
	p.mu.Unlock()
	if d != nil {
		return d, nil
	}
	d = &discovery{}
	if err := p.getJSON(ctx, p.cfg.Issuer+"/.well-known/openid-configuration", d); err != nil {
		return nil, err
	}
	if strings.TrimRight(d.Issuer, "/") != p.cfg.Issuer {
		return nil, errors.New("provider issuer does not match configuration")
	}
	for _, e := range []string{d.Authorization, d.Token, d.JWKS} {
		if !strings.HasPrefix(e, "https://") && !strings.HasPrefix(e, "http://127.0.0.1") {
			return nil, errors.New("provider endpoint is not https")
		}
	}
	p.mu.Lock()
	p.discovery = d
	p.mu.Unlock()
	return d, nil
}

// key returns the signing key for kid, refetching JWKS for an unknown kid at
// most once a minute (keys rotate; a forged kid must not hammer the provider).
func (p *Provider) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	k := p.keys[kid]
	stale := p.now().Sub(p.keysAt) > time.Minute
	p.mu.Unlock()
	if k != nil || !stale {
		if k == nil {
			return nil, errors.New("unknown signing key")
		}
		return k, nil
	}
	d, err := p.meta(ctx)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []struct {
			Kty, Kid, N, E, Use, Alg string
		} `json:"keys"`
	}
	if err = p.getJSON(ctx, d.JWKS, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, j := range set.Keys {
		if j.Kty != "RSA" || (j.Use != "" && j.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(j.N)
		e, err2 := base64.RawURLEncoding.DecodeString(j.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() >= 2048 {
			keys[j.Kid] = pub
		}
	}
	p.mu.Lock()
	p.keys, p.keysAt = keys, p.now()
	p.mu.Unlock()
	if k = keys[kid]; k == nil {
		return nil, errors.New("unknown signing key")
	}
	return k, nil
}

// --- flow ------------------------------------------------------------------

// Start redirects to the provider with fresh state, nonce, and PKCE verifier,
// remembered in a short-lived signed cookie.
func (p *Provider) Start(w http.ResponseWriter, r *http.Request) {
	d, err := p.meta(r.Context())
	if err != nil {
		http.Error(w, "sign-in provider unavailable", 502)
		return
	}
	state, nonce, verifier := random(), random(), random()
	exp := strconv.FormatInt(p.now().Add(flowTTL).Unix(), 10)
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: p.sign(state, nonce, verifier, exp), Path: "/auth/", MaxAge: int(flowTTL.Seconds()), HttpOnly: true, Secure: p.secure(), SameSite: http.SameSiteLaxMode})
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {p.RedirectURI()},
		"scope":                 {"openid email"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, d.Authorization+"?"+q.Encode(), http.StatusFound)
}

// Callback finishes the flow and sets the session cookie. Failures return to
// the dashboard with a short reason; details go to the error log only.
func (p *Provider) Callback(w http.ResponseWriter, r *http.Request) (string, error) {
	fail := func(reason string, err error) (string, error) {
		http.SetCookie(w, &http.Cookie{Name: flowCookie, Path: "/auth/", MaxAge: -1})
		http.Redirect(w, r, "/?signin_error="+url.QueryEscape(reason), http.StatusFound)
		return "", err
	}
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return fail("expired", errors.New("no sign-in in progress"))
	}
	f, ok := p.verify(c.Value)
	if !ok || len(f) != 4 {
		return fail("expired", errors.New("invalid flow cookie"))
	}
	state, nonce, verifier := f[0], f[1], f[2]
	if exp, _ := strconv.ParseInt(f[3], 10, 64); p.now().Unix() > exp {
		return fail("expired", errors.New("sign-in took too long"))
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return fail("denied", fmt.Errorf("provider returned %s", e))
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || q.Get("code") == "" {
		return fail("invalid", errors.New("state mismatch"))
	}
	d, err := p.meta(r.Context())
	if err != nil {
		return fail("unavailable", err)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {p.RedirectURI()}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(r.Context(), "POST", d.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return fail("unavailable", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	res, err := p.client.Do(req)
	if err != nil {
		return fail("unavailable", err)
	}
	defer res.Body.Close()
	var tok struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok) != nil || tok.IDToken == "" {
		return fail("invalid", fmt.Errorf("token exchange returned HTTP %d", res.StatusCode))
	}
	sub, err := p.verifyIDToken(r.Context(), tok.IDToken, nonce)
	if err != nil {
		return fail("invalid", err)
	}
	if len(p.cfg.Emails) > 0 {
		email, err := p.userEmail(r.Context(), d, tok.AccessToken)
		if err != nil || !slices.Contains(p.cfg.Emails, strings.ToLower(email)) {
			return fail("not-allowed", fmt.Errorf("email %q is not allowed: %v", email, err))
		}
	}
	exp := strconv.FormatInt(p.now().Add(SessionTTL).Unix(), 10)
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Path: "/auth/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: p.sign(p.cfg.Owner, sub, exp), Path: "/", MaxAge: int(SessionTTL.Seconds()), HttpOnly: true, Secure: p.secure(), SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/#accounts", http.StatusFound)
	return sub, nil
}

func (p *Provider) verifyIDToken(ctx context.Context, raw, nonce string) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed ID token")
	}
	var header struct{ Alg, Kid string }
	if b, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil || json.Unmarshal(b, &header) != nil {
		return "", errors.New("malformed ID token header")
	}
	if header.Alg != "RS256" {
		return "", errors.New("unexpected ID token algorithm")
	}
	key, err := p.key(ctx, header.Kid)
	if err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("malformed ID token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err = rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return "", errors.New("ID token signature is invalid")
	}
	var claims struct {
		Iss   string          `json:"iss"`
		Sub   string          `json:"sub"`
		Aud   json.RawMessage `json:"aud"`
		Exp   int64           `json:"exp"`
		Iat   int64           `json:"iat"`
		Nonce string          `json:"nonce"`
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(b, &claims) != nil {
		return "", errors.New("malformed ID token claims")
	}
	var aud []string
	if json.Unmarshal(claims.Aud, &aud) != nil {
		var one string
		if json.Unmarshal(claims.Aud, &one) != nil {
			return "", errors.New("malformed audience")
		}
		aud = []string{one}
	}
	now := p.now().Unix()
	switch {
	case strings.TrimRight(claims.Iss, "/") != p.cfg.Issuer:
		return "", errors.New("ID token issuer mismatch")
	case !slices.Contains(aud, p.cfg.ClientID):
		return "", errors.New("ID token audience mismatch")
	case claims.Exp < now-30:
		return "", errors.New("ID token expired")
	case claims.Iat > now+60:
		return "", errors.New("ID token issued in the future")
	case subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1:
		return "", errors.New("ID token nonce mismatch")
	case claims.Sub == "":
		return "", errors.New("ID token has no subject")
	}
	return claims.Sub, nil
}

func (p *Provider) userEmail(ctx context.Context, d *discovery, accessToken string) (string, error) {
	if d.Userinfo == "" || accessToken == "" {
		return "", errors.New("no userinfo")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", d.Userinfo, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	res, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var info struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&info) != nil {
		return "", fmt.Errorf("userinfo returned HTTP %d", res.StatusCode)
	}
	if !info.Verified {
		return "", errors.New("email is not verified")
	}
	return info.Email, nil
}

// Session returns the owner of a valid session cookie.
func (p *Provider) Session(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	f, ok := p.verify(c.Value)
	if !ok || len(f) != 3 || f[0] != p.cfg.Owner {
		return "", false
	}
	if exp, _ := strconv.ParseInt(f[2], 10, 64); p.now().Unix() > exp {
		return "", false
	}
	return f[0], true
}

func (p *Provider) Logout(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: p.secure(), SameSite: http.SameSiteStrictMode})
}
