package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
	"github.com/olivermarcusson/nebula/internal/logins"
	"github.com/olivermarcusson/nebula/internal/sessions"
)

// The vault signs the server's own Claude Code CLI into an account once and
// shares it with every device. The server keeps the refresh token and is the
// only one that ever renews; devices receive the current access token alone,
// so no device can renew and sign the others out. A device that goes offline
// keeps working until its last access token expires.
//
// In the dashboard the vault appears as one more device, "All devices", so
// sign-ins reuse the relay: claude prints a sign-in link and waits for the
// code the sign-in page shows, which the person pastes into the dashboard.

// vaultDevice identifies the vault among an owner's devices.
const vaultDevice = "6e656275-6c61-4000-8000-000000000001"

const (
	vaultName = "All devices"
	// renewBelow renews a token with less validity left than this, so devices
	// always hold several hours of it.
	renewBelow  = 4 * time.Hour
	renewRetry  = 15 * time.Minute
	vaultReport = 5 * time.Minute
)

type vault struct {
	dir, claude string
	owners      []string
	signIns     *logins.Manager
	store       *accounts.Store

	mu     sync.Mutex
	active map[string]*signIn
	failed map[string]time.Time // account dir -> last failed renewal
	// work serializes sign-ins finishing, renewals, and removals on disk.
	work sync.Mutex
}

// vaultToken is what a device receives for one account.
type vaultToken struct {
	AccountID        string          `json:"account_id"`
	Email            string          `json:"email"`
	OAuthAccount     json.RawMessage `json:"oauth_account"`
	AccessToken      string          `json:"access_token"`
	ExpiresAt        int64           `json:"expires_at"`
	Scopes           []string        `json:"scopes"`
	SubscriptionType string          `json:"subscription_type,omitempty"`
	RateLimitTier    string          `json:"rate_limit_tier,omitempty"`
}

func (v *vault) ownerDir(owner string) string {
	h := sha256.Sum256([]byte(owner))
	return filepath.Join(v.dir, hex.EncodeToString(h[:8]))
}

// claudeCmd runs the server's claude in one vault profile.
func (v *vault) claudeCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, v.claude, args...)
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") && !strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") && !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "CLAUDE_CONFIG_DIR="+dir, "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	cmd.Dir = v.dir
	return cmd
}

func (v *vault) run(ctx context.Context) {
	v.active, v.failed = map[string]*signIn{}, map[string]time.Time{}
	for _, owner := range v.owners {
		if err := os.MkdirAll(v.ownerDir(owner), 0700); err != nil {
			log.Print("Vault unavailable: ", err)
			return
		}
		// Sign-ins the server was stopped during are abandoned.
		if entries, err := os.ReadDir(v.ownerDir(owner)); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".pending-") {
					_ = os.RemoveAll(filepath.Join(v.ownerDir(owner), e.Name()))
				}
			}
		}
	}
	var maintained time.Time
	for {
		for _, owner := range v.owners {
			v.handle(owner, v.signIns.Poll(owner, vaultDevice, vaultName))
		}
		if time.Since(maintained) >= time.Minute {
			maintained = time.Now()
			go v.maintain(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (v *vault) handle(owner string, w logins.Work) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, l := range w.Start {
		if v.active[l.ID] != nil {
			continue
		}
		sctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
		s := &signIn{codes: make(chan string, 1), ctx: sctx, cancel: cancel}
		v.active[l.ID] = s
		go v.signIn(owner, l.ID, s)
	}
	for id, code := range w.Codes {
		if s := v.active[id]; s != nil {
			select {
			case s.codes <- code:
			default:
			}
		}
	}
	for _, id := range w.Cancel {
		if s := v.active[id]; s != nil {
			s.cancel()
		}
	}
}

func (v *vault) signIn(owner, id string, s *signIn) {
	defer func() {
		s.cancel()
		v.mu.Lock()
		delete(v.active, id)
		v.mu.Unlock()
	}()
	status := func(state, link, msg, account string) {
		if _, err := v.signIns.Update(owner, vaultDevice, id, state, link, msg, account); err != nil {
			log.Print("Vault sign-in status update failed: ", err)
		}
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	pending := filepath.Join(v.ownerDir(owner), ".pending-"+hex.EncodeToString(b))
	if err := os.Mkdir(pending, 0700); err != nil {
		status(logins.Failed, "", "Could not create a Claude profile on the server", "")
		return
	}
	defer os.RemoveAll(pending)
	cmd := v.claudeCmd(s.ctx, pending, "auth", "login", "--claudeai")
	if msg, ok := runSignIn(s, cmd, func(link string) { status(logins.Waiting, link, "", "") }); !ok {
		if msg != "" {
			status(logins.Failed, "", msg, "")
		}
		return
	}
	p, err := accounts.ReadLocal("vault", filepath.Join(pending, ".claude.json"))
	if err != nil || p == nil {
		status(logins.Failed, "", "Claude did not record a signed-in account", "")
		return
	}
	if creds, err := readCredentials(pending); err != nil || creds.RefreshToken == "" {
		status(logins.Failed, "", "Claude did not store a renewable sign-in", "")
		return
	}
	// A new sign-in to an account the vault already holds replaces it.
	v.work.Lock()
	dest := filepath.Join(v.ownerDir(owner), p.AccountUUID)
	err = os.RemoveAll(dest)
	if err == nil {
		err = os.Rename(pending, dest)
	}
	v.work.Unlock()
	if err != nil {
		status(logins.Failed, "", "Signed in, but the server could not keep the sign-in", "")
		return
	}
	if err = v.report(context.Background(), owner); err != nil {
		log.Print("Vault account report failed: ", err)
	}
	if _, err = v.store.Connect(owner, p.AccountUUID); err != nil {
		log.Print("Could not connect vault account: ", err)
	}
	status(logins.Completed, "", "", p.AccountUUID)
	log.Printf("Vault signed in to %s", p.Email)
}

type credentials struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

func readCredentials(dir string) (credentials, error) {
	var file struct {
		OAuth *credentials `json:"claudeAiOauth"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return credentials{}, err
	}
	if err = json.Unmarshal(raw, &file); err != nil || file.OAuth == nil || file.OAuth.AccessToken == "" {
		return credentials{}, errors.New("no Claude sign-in stored")
	}
	return *file.OAuth, nil
}

// accountDirs lists the owner's vault profiles, one per account.
func (v *vault) accountDirs(owner string) []string {
	entries, err := os.ReadDir(v.ownerDir(owner))
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(v.ownerDir(owner), e.Name()))
		}
	}
	return out
}

// Tokens returns the current access token of every vault account.
func (v *vault) Tokens(owner string) []vaultToken {
	out := []vaultToken{}
	for _, dir := range v.accountDirs(owner) {
		// Expired tokens are listed too: devices drop only removed accounts.
		c, err := readCredentials(dir)
		if err != nil || c.RefreshToken == "" {
			continue
		}
		var cfg struct {
			OAuthAccount json.RawMessage `json:"oauthAccount"`
		}
		raw, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
		p, _ := accounts.ReadLocal("vault", filepath.Join(dir, ".claude.json"))
		if err != nil || json.Unmarshal(raw, &cfg) != nil || p == nil || len(cfg.OAuthAccount) == 0 {
			continue
		}
		out = append(out, vaultToken{
			AccountID: p.AccountUUID, Email: p.Email, OAuthAccount: cfg.OAuthAccount,
			AccessToken: c.AccessToken, ExpiresAt: c.ExpiresAt, Scopes: c.Scopes,
			SubscriptionType: c.SubscriptionType, RateLimitTier: c.RateLimitTier,
		})
	}
	return out
}

// Remove signs the vault out of an account; devices drop their copies.
func (v *vault) Remove(owner, account string) error {
	if !sessions.ID(account) {
		return nil
	}
	v.work.Lock()
	err := os.RemoveAll(filepath.Join(v.ownerDir(owner), account))
	v.work.Unlock()
	if err != nil {
		return err
	}
	return v.report(context.Background(), owner)
}

// maintain renews tokens running low and reports the vault's accounts with
// fresh usage, read with the vault's own tokens.
func (v *vault) maintain(ctx context.Context) {
	if !v.work.TryLock() {
		return
	}
	for _, owner := range v.owners {
		for _, dir := range v.accountDirs(owner) {
			c, err := readCredentials(dir)
			if err != nil || time.Until(time.UnixMilli(c.ExpiresAt)) > renewBelow {
				continue
			}
			v.mu.Lock()
			recent := time.Since(v.failed[dir]) < renewRetry
			v.mu.Unlock()
			if recent {
				continue
			}
			if err := v.renew(ctx, dir); err != nil {
				log.Printf("Vault renewal of %s failed: %v", filepath.Base(dir), err)
				v.mu.Lock()
				v.failed[dir] = time.Now()
				v.mu.Unlock()
			} else {
				log.Printf("Vault renewed %s", filepath.Base(dir))
			}
		}
	}
	v.work.Unlock()
	for _, owner := range v.owners {
		if err := v.report(ctx, owner); err != nil {
			log.Print("Vault account report failed: ", err)
		}
	}
}

// renew has the vault's claude renew its own sign-in. Claude Code renews
// when it is about to call the API with a token near expiry, so the stored
// expiry is moved to now and claude runs one tiny request; only the token
// change counts as success. A failed attempt restores the old expiry, so the
// still-valid token keeps being shared.
func (v *vault) renew(ctx context.Context, dir string) error {
	file := filepath.Join(dir, ".credentials.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	var oauth map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if err = json.Unmarshal(doc["claudeAiOauth"], &oauth); err != nil {
		return err
	}
	before, _ := oauth["accessToken"].(string)
	expiry := oauth["expiresAt"]
	setExpiry := func(at any) error {
		oauth["expiresAt"] = at
		b, err := json.Marshal(oauth)
		if err != nil {
			return err
		}
		doc["claudeAiOauth"] = b
		out, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		return writeFileAtomic(file, out)
	}
	if err = setExpiry(time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := v.claudeCmd(rctx, dir, "-p", "Reply with OK.", "--model", "haiku", "--tools", "", "--strict-mcp-config", "--no-session-persistence", "--disable-slash-commands")
	out, runErr := cmd.CombinedOutput()
	c, err := readCredentials(dir)
	if err == nil && c.AccessToken != before && time.Until(time.UnixMilli(c.ExpiresAt)) > time.Hour {
		return nil
	}
	if err == nil && c.AccessToken == before {
		_ = setExpiry(expiry)
	}
	msg := strings.TrimSpace(string(out))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if runErr != nil {
		return errors.New(msg)
	}
	return errors.New("claude did not renew the sign-in: " + msg)
}

// report records the vault's accounts as signed in on the vault device.
func (v *vault) report(ctx context.Context, owner string) error {
	rep := accounts.Report{Profiles: []accounts.Profile{}}
	for _, dir := range v.accountDirs(owner) {
		p, err := accounts.ReadLocal("vault", filepath.Join(dir, ".claude.json"))
		if err != nil || p == nil {
			continue
		}
		if c, err := readCredentials(dir); err != nil || c.RefreshToken == "" {
			continue // signed out; nothing left to share
		}
		p.Profile = profileName(p.Email)
		if u := liveUsage(ctx, profile{Name: p.Profile, Dir: dir}); u != nil {
			p.Usage = u
		}
		rep.Profiles = append(rep.Profiles, *p)
	}
	_, err := v.store.Report(owner, vaultDevice, rep)
	return err
}

func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(0600)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
