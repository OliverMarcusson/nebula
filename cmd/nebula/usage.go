package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
)

// Fresh usage for every profile, including idle ones, read with the profile's
// own OAuth access token from Claude's usage endpoint (the one Claude Code
// itself polls), asking for reset offers the way Claude Code's /usage does. The token is used only while still valid and is never
// refreshed, logged, or sent anywhere but Anthropic, so Claude Code's own
// sign-in is never disturbed.

const (
	usageURL      = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"
	usageInterval = 5 * time.Minute
)

var usageCache = struct {
	sync.Mutex
	at    map[string]time.Time
	value map[string]*accounts.Usage
}{at: map[string]time.Time{}, value: map[string]*accounts.Usage{}}

// Requests are bounded by their context instead; a reset may take longer.
var usageClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func credentialsFile(p profile) string {
	home, _ := os.UserHomeDir()
	if p.Name == "default" && p.ConfigFile == filepath.Join(home, ".claude.json") {
		return filepath.Join(home, ".claude", ".credentials.json")
	}
	return filepath.Join(p.Dir, ".credentials.json")
}

// liveUsage returns a reading at most usageInterval old, or nil when the
// profile has no usable token (expired, missing scope, or kept in a keychain).
func liveUsage(ctx context.Context, p profile) *accounts.Usage {
	usageCache.Lock()
	if time.Since(usageCache.at[p.Dir]) < usageInterval {
		u := usageCache.value[p.Dir]
		usageCache.Unlock()
		return u
	}
	usageCache.at[p.Dir] = time.Now()
	usageCache.Unlock()

	u, err := fetchUsage(ctx, credentialsFile(p))
	if err != nil {
		u = nil
	}
	usageCache.Lock()
	usageCache.value[p.Dir] = u
	usageCache.Unlock()
	return u
}

// accessToken returns a profile's access token while it is still valid.
func accessToken(file string) (string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	var creds struct {
		OAuth struct {
			AccessToken string   `json:"accessToken"`
			ExpiresAt   int64    `json:"expiresAt"`
			Scopes      []string `json:"scopes"`
		} `json:"claudeAiOauth"`
	}
	if err = json.Unmarshal(raw, &creds); err != nil {
		return "", err
	}
	o := creds.OAuth
	if o.AccessToken == "" || time.UnixMilli(o.ExpiresAt).Before(time.Now().Add(time.Minute)) || !slices.Contains(o.Scopes, "user:profile") {
		return "", errors.New("no usable access token")
	}
	return o.AccessToken, nil
}

// anthropic sends one request to Claude's API with a profile's access token.
func anthropic(ctx context.Context, file, method, url string, body io.Reader) ([]byte, int, error) {
	token, err := accessToken(file)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := usageClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return data, res.StatusCode, err
}

// fetchUsage reads usage windows and, with them, the account's reset offers.
func fetchUsage(ctx context.Context, file string) (*accounts.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, status, err := anthropic(ctx, file, "GET", usageURL, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, errors.New("usage request refused")
	}
	u, err := accounts.ParseUsage(body, time.Now())
	if err != nil {
		return nil, err
	}
	u.Grants = accounts.ParseGrants(body)
	return u, nil
}
