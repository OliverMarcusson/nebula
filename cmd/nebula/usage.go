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
// itself polls). The token is used only while still valid and is never
// refreshed, logged, or sent anywhere but Anthropic, so Claude Code's own
// sign-in is never disturbed.

const (
	usageURL      = "https://api.anthropic.com/api/oauth/usage"
	usageInterval = 5 * time.Minute
)

var usageCache = struct {
	sync.Mutex
	at    map[string]time.Time
	value map[string]*accounts.Usage
}{at: map[string]time.Time{}, value: map[string]*accounts.Usage{}}

var usageClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

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

func fetchUsage(ctx context.Context, file string) (*accounts.Usage, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var creds struct {
		OAuth struct {
			AccessToken string   `json:"accessToken"`
			ExpiresAt   int64    `json:"expiresAt"`
			Scopes      []string `json:"scopes"`
		} `json:"claudeAiOauth"`
	}
	if err = json.Unmarshal(raw, &creds); err != nil {
		return nil, err
	}
	o := creds.OAuth
	if o.AccessToken == "" || time.UnixMilli(o.ExpiresAt).Before(time.Now().Add(time.Minute)) || !slices.Contains(o.Scopes, "user:profile") {
		return nil, errors.New("no usable access token")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", usageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	res, err := usageClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("usage request refused")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return accounts.ParseUsage(body, time.Now())
}
