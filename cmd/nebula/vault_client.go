package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
	"github.com/olivermarcusson/nebula/internal/sessions"
)

// Devices keep one managed profile per account the server shares, holding
// that account's current access token and no refresh token. The marker file
// names the account; a profile someone signs into directly (its credentials
// gain a refresh token) stops being shared and is left alone.

const sharedMarker = ".nebula-shared"

// sharedAccount reports the account a shared profile holds, or "".
func sharedAccount(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, sharedMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// usableToken is false for a profile Claude would only ask to sign in again:
// one signed out (no token left, though .claude.json still names the
// account), or a shared one whose access token has expired, since it cannot
// renew. Without a credentials file the sign-in may be in the macOS keychain.
func usableToken(p profile) bool {
	c, err := readCredentials(p.Dir)
	if sharedAccount(p.Dir) == "" {
		if errors.Is(err, fs.ErrNotExist) {
			return runtime.GOOS == "darwin"
		}
		if err == nil && c.RefreshToken != "" {
			return true
		}
	}
	return err == nil && time.UnixMilli(c.ExpiresAt).After(time.Now().Add(time.Minute))
}

// syncShared brings this device's shared profiles in line with the server.
func syncShared(ctx context.Context, c *client, list string) error {
	var resp struct {
		Accounts []vaultToken `json:"accounts"`
	}
	if err := c.call(ctx, "GET", "/v1/vault", nil, &resp); err != nil {
		return err
	}
	managed, err := managedDir()
	if err != nil {
		return err
	}
	profiles, err := claudeProfiles(list)
	if err != nil {
		return err
	}
	own, shared := map[string]bool{}, map[string]string{}
	for _, p := range profiles {
		if id := sharedAccount(p.Dir); id != "" {
			shared[id] = p.Dir
		} else if q, err := accounts.ReadLocal(p.Name, p.ConfigFile); err == nil && q != nil {
			own[q.AccountUUID] = true
		}
	}
	want := map[string]bool{}
	for _, t := range resp.Accounts {
		if !sessions.ID(t.AccountID) || len(t.OAuthAccount) == 0 {
			continue
		}
		want[t.AccountID] = true
		dir := shared[t.AccountID]
		if dir == "" {
			if own[t.AccountID] {
				continue // already signed in here directly
			}
			if dir, err = newSharedProfile(managed, t); err != nil {
				log.Printf("Could not add shared account %s: %v", t.Email, err)
				continue
			}
			log.Printf("Added shared account %s", t.Email)
		}
		if err := writeSharedToken(dir, t); err != nil {
			log.Printf("Could not update shared account %s: %v", t.Email, err)
		}
	}
	for id, dir := range shared {
		if !want[id] {
			retireShared(dir)
		}
	}
	return nil
}

func newSharedProfile(managed string, t vaultToken) (string, error) {
	if err := os.MkdirAll(managed, 0700); err != nil {
		return "", err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	pending := filepath.Join(managed, ".pending-"+hex.EncodeToString(b))
	if err := os.Mkdir(pending, 0700); err != nil {
		return "", err
	}
	defer os.RemoveAll(pending)
	cfg, err := json.Marshal(map[string]json.RawMessage{"oauthAccount": t.OAuthAccount})
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(pending, ".claude.json"), cfg, 0600); err != nil {
		return "", err
	}
	if err = seedConfig(filepath.Join(pending, ".claude.json")); err != nil {
		return "", err
	}
	if err = writeSharedToken(pending, t); err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(pending, sharedMarker), []byte(t.AccountID+"\n"), 0600); err != nil {
		return "", err
	}
	base := t.AccountID[:8]
	if at := strings.IndexByte(t.Email, '@'); at > 0 {
		base = t.Email[:at]
	}
	base = profileName(base)
	name := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(managed, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	dir := filepath.Join(managed, name)
	if err = os.Rename(pending, dir); err != nil {
		return "", err
	}
	if err = linkShared(dir); err != nil {
		log.Print("Shared profile created without shared settings: ", err)
	}
	return dir, nil
}

// writeSharedToken stores the account's current access token, without a
// refresh token, unless the profile already holds it.
func writeSharedToken(dir string, t vaultToken) error {
	file := filepath.Join(dir, ".credentials.json")
	if c, err := readCredentials(dir); err == nil {
		if c.RefreshToken != "" {
			// Signed in here directly; it renews itself and is no longer shared.
			return os.Remove(filepath.Join(dir, sharedMarker))
		}
		if c.AccessToken == t.AccessToken && c.ExpiresAt == t.ExpiresAt {
			return nil
		}
	}
	oauth := map[string]any{"accessToken": t.AccessToken, "expiresAt": t.ExpiresAt, "scopes": t.Scopes}
	if t.SubscriptionType != "" {
		oauth["subscriptionType"] = t.SubscriptionType
	}
	if t.RateLimitTier != "" {
		oauth["rateLimitTier"] = t.RateLimitTier
	}
	data, err := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err != nil {
		return err
	}
	return writeFileAtomic(file, data)
}

// retireShared hides a profile the server no longer shares and deletes its
// token. The directory stays, since it links the shared session history.
func retireShared(dir string) {
	_ = os.Remove(filepath.Join(dir, ".credentials.json"))
	_ = os.Remove(filepath.Join(dir, sharedMarker))
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	retired := filepath.Join(filepath.Dir(dir), ".retired-"+filepath.Base(dir)+"-"+hex.EncodeToString(b))
	if err := os.Rename(dir, retired); err != nil {
		log.Print("Could not retire shared profile: ", err)
		return
	}
	log.Printf("Removed shared profile %s", filepath.Base(dir))
}
