package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	*httptest.Server
	key       *rsa.PrivateKey
	challenge string
	nonce     string
	aud       string
	email     string
	badSig    bool
	authAge   int64 // seconds between the reported authentication and now
}

func newFake(t *testing.T) *fakeProvider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{key: key, aud: "nebula", email: "me@example.com"}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token", "userinfo_endpoint": f.URL + "/userinfo", "jwks_uri": f.URL + "/jwks.json"})
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if id != "nebula" || secret != "s3cret" || r.FormValue("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": f.idToken(), "access_token": "at"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sub": "pairwise-sub", "email": f.email, "email_verified": true})
	})
	return f
}

func (f *fakeProvider) idToken() string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	now := time.Now().Unix()
	signing := enc(map[string]string{"alg": "RS256", "kid": "k1"}) + "." + enc(map[string]any{"iss": f.URL, "sub": "pairwise-sub", "aud": f.aud, "exp": now + 300, "iat": now, "nonce": f.nonce, "auth_time": now - f.authAge})
	digest := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if f.badSig {
		sig[0] ^= 0xff
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// signIn runs Start, plays the provider, and runs Callback.
func signIn(t *testing.T, p *Provider, f *fakeProvider, tamperState bool) *httptest.ResponseRecorder {
	rec, _ := flow(t, p, f, tamperState, "")
	return rec
}

// flow runs Start, or StepUp for an action, then the provider and Callback.
func flow(t *testing.T, p *Provider, f *fakeProvider, tamperState bool, action string) (*httptest.ResponseRecorder, string) {
	start := httptest.NewRecorder()
	link := ""
	if action == "" {
		p.Start(start, httptest.NewRequest("GET", "/auth/login", nil))
		link = start.Header().Get("Location")
	} else {
		link, _ = p.StepUp(start, httptest.NewRequest("POST", "/auth/reset/x", nil), action)
	}
	loc, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(loc.String(), f.URL+"/authorize") {
		t.Fatalf("start redirect: %v %s", err, loc)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "https://nebula.example/auth/callback" {
		t.Fatalf("authorize parameters: %v", q)
	}
	if fresh := q.Get("prompt") == "login" && q.Get("max_age") == "0"; fresh != (action != "") {
		t.Fatalf("prompt=%q max_age=%q for action %q", q.Get("prompt"), q.Get("max_age"), action)
	}
	f.challenge, f.nonce = q.Get("code_challenge"), q.Get("nonce")
	state := q.Get("state")
	if tamperState {
		state = "forged"
	}
	cb := httptest.NewRequest("GET", "/auth/callback?code=the-code&state="+url.QueryEscape(state), nil)
	for _, c := range start.Result().Cookies() {
		cb.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	got, _ := p.Callback(rec, cb)
	return rec, got
}

func TestStepUp(t *testing.T) {
	f := newFake(t)
	defer f.Close()
	p := provider(t, f)
	rec, action := flow(t, p, f, false, "reset:abc")
	if action != "reset:abc" {
		t.Fatalf("fresh step-up returned %q: %s", action, rec.Header().Get("Location"))
	}
	if _, ok := session(p, rec); ok {
		t.Fatal("step-up created a dashboard session")
	}
	f.authAge = 600 // the provider reused an older login
	if rec, action = flow(t, p, f, false, "reset:abc"); action != "" || !strings.Contains(rec.Header().Get("Location"), "signin_error=not-fresh") {
		t.Fatalf("stale step-up accepted: %q %s", action, rec.Header().Get("Location"))
	}
	if _, action = flow(t, p, f, true, "reset:abc"); action != "" {
		t.Fatal("step-up with forged state accepted")
	}
}

func session(p *Provider, rec *httptest.ResponseRecorder) (string, bool) {
	r := httptest.NewRequest("GET", "/v1/me", nil)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge >= 0 {
			r.AddCookie(c)
		}
	}
	return p.Session(r)
}

func provider(t *testing.T, f *fakeProvider, emails ...string) *Provider {
	p, err := New(Config{Issuer: f.URL, ClientID: "nebula", ClientSecret: "s3cret", PublicURL: "https://nebula.example", Owner: "oliver", Emails: emails, Key: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSignIn(t *testing.T) {
	f := newFake(t)
	defer f.Close()
	p := provider(t, f, "me@example.com")
	rec := signIn(t, p, f, false)
	if owner, ok := session(p, rec); !ok || owner != "oliver" {
		t.Fatalf("no session after sign-in: %s", rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && (!c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode) {
			t.Fatalf("session cookie attributes: %+v", c)
		}
	}
}

func TestRejections(t *testing.T) {
	cases := map[string]func(f *fakeProvider) (*Provider, bool){
		"forged state":   func(f *fakeProvider) (*Provider, bool) { return provider(t, f), true },
		"other audience": func(f *fakeProvider) (*Provider, bool) { f.aud = "someone-else"; return provider(t, f), false },
		"bad signature":  func(f *fakeProvider) (*Provider, bool) { f.badSig = true; return provider(t, f), false },
		"email not allowed": func(f *fakeProvider) (*Provider, bool) {
			f.email = "stranger@example.com"
			return provider(t, f, "me@example.com"), false
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			defer f.Close()
			p, tamper := setup(f)
			rec := signIn(t, p, f, tamper)
			if _, ok := session(p, rec); ok {
				t.Fatal("signed in anyway")
			}
			if !strings.Contains(rec.Header().Get("Location"), "signin_error=") {
				t.Fatalf("no error redirect: %s", rec.Header().Get("Location"))
			}
		})
	}
}

func TestTamperedSessionCookie(t *testing.T) {
	f := newFake(t)
	defer f.Close()
	p := provider(t, f)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.sign("oliver", "sub", "99999999999") + "x"})
	if _, ok := p.Session(r); ok {
		t.Fatal("tampered cookie accepted")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.sign("oliver", "sub", "1")})
	if _, ok := p.Session(r); ok {
		t.Fatal("expired session accepted")
	}
}
