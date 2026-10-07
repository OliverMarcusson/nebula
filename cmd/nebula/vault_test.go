package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
	"github.com/olivermarcusson/nebula/internal/logins"
)

const sharedID = "11111111-2222-4333-8444-555555555555"

// fakeClaude signs in with the code "good-code" and renews on every print
// run, unless FAKE_CLAUDE_FAIL is set.
const fakeClaude = `#!/bin/sh
exp() { echo $(( ($(date +%s) + $1) * 1000 )); }
case "$1" in
auth)
  echo "Opening browser... If it did not open, visit: https://claude.ai/oauth/authorize?code=true"
  read code
  [ "$code" = "good-code" ] || { echo "Login failed: wrong code"; exit 1; }
  echo '{"oauthAccount":{"accountUuid":"` + sharedID + `","emailAddress":"shared@example.com"}}' > "$CLAUDE_CONFIG_DIR/.claude.json"
  printf '{"claudeAiOauth":{"accessToken":"at-1","refreshToken":"rt-1","expiresAt":%s,"scopes":["user:inference"]}}' "$(exp 3600)" > "$CLAUDE_CONFIG_DIR/.credentials.json"
  ;;
-p)
  [ -n "$FAKE_CLAUDE_FAIL" ] && { echo "API Error: 500"; exit 1; }
  printf '{"claudeAiOauth":{"accessToken":"at-2","refreshToken":"rt-2","expiresAt":%s,"scopes":["user:inference"]}}' "$(exp 28800)" > "$CLAUDE_CONFIG_DIR/.credentials.json"
  echo OK
  ;;
esac
`

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out waiting for ", what)
}

func TestVaultSignInRenewRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a shell script")
	}
	tmp := t.TempDir()
	claude := filepath.Join(tmp, "claude")
	if err := os.WriteFile(claude, []byte(fakeClaude), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(filepath.Join(tmp, "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := logins.New()
	v := &vault{dir: filepath.Join(tmp, "vault"), claude: claude, owners: []string{"me"}, signIns: m, store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go v.run(ctx)

	var l logins.Login
	waitFor(t, "the vault to come online", func() bool { l, err = m.Create("me", vaultDevice); return err == nil })
	waitFor(t, "the sign-in link", func() bool { l, _ = m.Get("me", l.ID); return l.State == logins.Waiting })
	if !strings.HasPrefix(l.URL, "https://claude.ai/") {
		t.Fatalf("sign-in link %q", l.URL)
	}
	if _, err = m.SubmitCode("me", l.ID, "good-code"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sign-in to finish", func() bool { l, _ = m.Get("me", l.ID); return l.State == logins.Completed })

	list, _ := store.List("me")
	if len(list) != 1 || list[0].ID != sharedID || list[0].State != accounts.Connected || list[0].Sightings[0].DeviceID != vaultDevice {
		t.Fatalf("accounts after sign-in: %+v", list)
	}
	tokens := v.Tokens("me")
	if len(tokens) != 1 || tokens[0].AccessToken != "at-1" || tokens[0].Email != "shared@example.com" {
		t.Fatalf("tokens: %+v", tokens)
	}
	dir := filepath.Join(v.ownerDir("me"), sharedID)

	// A failed renewal keeps sharing the current token with its real expiry.
	t.Setenv("FAKE_CLAUDE_FAIL", "1")
	if err = v.renew(ctx, dir); err == nil {
		t.Fatal("renewal reported success")
	}
	if got := v.Tokens("me"); got[0].AccessToken != "at-1" || got[0].ExpiresAt != tokens[0].ExpiresAt {
		t.Fatalf("after failed renewal: %+v", got)
	}
	os.Unsetenv("FAKE_CLAUDE_FAIL")
	if err = v.renew(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if got := v.Tokens("me"); got[0].AccessToken != "at-2" || time.Until(time.UnixMilli(got[0].ExpiresAt)) < 7*time.Hour {
		t.Fatalf("after renewal: %+v", got)
	}

	if err = v.Remove("me", sharedID); err != nil {
		t.Fatal(err)
	}
	if got := v.Tokens("me"); len(got) != 0 {
		t.Fatalf("tokens after removal: %+v", got)
	}
}

func TestSyncShared(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("APPDATA", filepath.Join(tmp, "config"))
	home := filepath.Join(tmp, "home-profile")
	t.Setenv("NEBULA_HOME_PROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0700); err != nil {
		t.Fatal(err)
	}
	const ownID = "99999999-2222-4333-8444-555555555555"
	_ = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"`+ownID+`","emailAddress":"own@example.com"}}`), 0600)

	var mu sync.Mutex
	serve := []vaultToken{}
	token := func(id, email, access string, expires time.Duration) vaultToken {
		return vaultToken{AccountID: id, Email: email, OAuthAccount: json.RawMessage(`{"accountUuid":"` + id + `","emailAddress":"` + email + `"}`),
			AccessToken: access, ExpiresAt: time.Now().Add(expires).UnixMilli(), Scopes: []string{"user:inference"}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": serve})
	}))
	defer srv.Close()
	c := &client{base: srv.URL, token: "t", http: srv.Client()}
	set := func(list ...vaultToken) { mu.Lock(); serve = list; mu.Unlock() }
	sync := func() {
		t.Helper()
		if err := syncShared(context.Background(), c, ""); err != nil {
			t.Fatal(err)
		}
	}
	managed, _ := managedDir()
	shared := func() map[string]string {
		out := map[string]string{}
		entries, _ := os.ReadDir(managed)
		for _, e := range entries {
			if id := sharedAccount(filepath.Join(managed, e.Name())); id != "" && !strings.HasPrefix(e.Name(), ".") {
				out[id] = filepath.Join(managed, e.Name())
			}
		}
		return out
	}

	// The account this device signed into itself is not duplicated.
	set(token(sharedID, "shared@example.com", "at-1", 8*time.Hour), token(ownID, "own@example.com", "x", 8*time.Hour))
	sync()
	got := shared()
	dir := got[sharedID]
	if len(got) != 1 || filepath.Base(dir) != "shared" {
		t.Fatalf("shared profiles: %v", got)
	}
	c1, err := readCredentials(dir)
	if err != nil || c1.AccessToken != "at-1" || c1.RefreshToken != "" {
		t.Fatalf("credentials: %+v %v", c1, err)
	}
	if p, _ := accounts.ReadLocal("shared", filepath.Join(dir, ".claude.json")); p == nil || p.Email != "shared@example.com" {
		t.Fatalf("identity: %+v", p)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, ".claude.json")); !strings.Contains(string(raw), "hasCompletedOnboarding") {
		t.Fatal("onboarding state not seeded")
	}
	if _, err := os.Lstat(filepath.Join(dir, "projects")); err != nil {
		t.Fatal("session history not linked: ", err)
	}
	if !usableToken(profile{Dir: dir}) {
		t.Fatal("fresh token not usable")
	}

	// Renewed tokens replace the old one; expired ones are not used.
	set(token(sharedID, "shared@example.com", "at-2", -time.Minute))
	sync()
	if c2, _ := readCredentials(dir); c2.AccessToken != "at-2" {
		t.Fatalf("token not updated: %+v", c2)
	}
	if usableToken(profile{Dir: dir}) {
		t.Fatal("expired token counted as usable")
	}

	// Signing in directly takes the profile out of sharing, untouched.
	direct := `{"claudeAiOauth":{"accessToken":"mine","refreshToken":"rt","expiresAt":1}}`
	_ = os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(direct), 0600)
	set(token(sharedID, "shared@example.com", "at-3", 8*time.Hour))
	sync()
	if raw, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); string(raw) != direct || sharedAccount(dir) != "" {
		t.Fatalf("direct sign-in overwritten: %s", raw)
	}

	// An account the server stops sharing is retired with its token deleted.
	const otherID = "22222222-2222-4333-8444-555555555555"
	set(token(otherID, "other@example.com", "at-o", 8*time.Hour))
	sync()
	other := shared()[otherID]
	if other == "" {
		t.Fatal("second shared account not added")
	}
	set()
	sync()
	if _, err := os.Stat(other); err == nil || len(shared()) != 0 {
		t.Fatalf("not retired: %v", shared())
	}
}

func TestUsableTokenSignedOut(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) { _ = os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(s), 0600) }
	// Claude's sign-out leaves empty tokens while .claude.json keeps the account.
	write(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0}}`)
	if usableToken(profile{Dir: dir}) {
		t.Fatal("signed-out profile counted as usable")
	}
	write(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"rt","expiresAt":1}}`)
	if !usableToken(profile{Dir: dir}) {
		t.Fatal("renewable sign-in not usable")
	}
}
