package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	usageInterval = time.Minute
	usageRetry    = 30 * time.Second
	usageRetryMax = 15 * time.Minute
)

type usageReading struct {
	token    [32]byte
	next     time.Time
	value    *accounts.Usage
	done     chan struct{}
	failures int
}

type usageRequestError struct {
	status  int
	retryAt time.Time
}

func (e *usageRequestError) Error() string {
	return fmt.Sprintf("usage request refused (HTTP %d)", e.status)
}

// Retry-After may be seconds or an HTTP date. It is a lower bound, even
// when the provider asks for longer than our own maximum backoff.
func usageRetryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at
	}
	return time.Time{}
}

func usageBackoff(failures, status int) time.Duration {
	delay := usageRetry
	if status == http.StatusTooManyRequests {
		delay = 2 * time.Minute
	}
	for i := 1; i < failures && delay < usageRetryMax; i++ {
		delay = min(delay*2, usageRetryMax)
	}
	// Spread accounts' retries across the last fifth of the backoff window.
	return delay - time.Duration(rand.Int64N(int64(delay/5)))
}

var usageCache = struct {
	sync.Mutex
	entries map[string]*usageReading
}{entries: map[string]*usageReading{}}

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
	file := credentialsFile(p)
	token, err := accessToken(file)
	if err != nil {
		return nil
	}
	hash := sha256.Sum256([]byte(token))
	for {
		if ctx.Err() != nil {
			return nil
		}
		usageCache.Lock()
		cached := usageCache.entries[p.Dir]
		if cached != nil {
			if cached.done != nil {
				done := cached.done
				usageCache.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return nil
				}
			}
			// Token rotation must not bypass a provider cooldown.
			if cached.failures > 0 && time.Now().Before(cached.next) {
				usageCache.Unlock()
				return nil
			}
			if cached.token == hash && time.Now().Before(cached.next) && (cached.value == nil || cached.value.Fresh(time.Now())) {
				u := cached.value
				usageCache.Unlock()
				return u
			}
		}
		reading := &usageReading{token: hash, done: make(chan struct{})}
		if cached != nil {
			reading.failures = cached.failures
		}
		usageCache.entries[p.Dir] = reading
		usageCache.Unlock()

		failures := reading.failures
		u, err := fetchUsage(ctx, file)
		next := time.Now().Add(usageInterval)
		if err != nil {
			u = nil
			if ctx.Err() == nil {
				failures++
				var refused *usageRequestError
				status := 0
				if errors.As(err, &refused) {
					status = refused.status
				}
				next = time.Now().Add(usageBackoff(failures, status))
				if refused != nil && refused.retryAt.After(next) {
					next = refused.retryAt
				}
				// Never log the request, token, or response body.
				log.Printf("Usage refresh failed for %s: %v; retrying in %s", p.Name, err, time.Until(next).Round(time.Second))
			} else {
				next = time.Now()
			}
		} else {
			failures = 0
		}
		usageCache.Lock()
		reading.failures = failures
		reading.value, reading.next = u, next
		if u != nil {
			for _, l := range u.Limits {
				if l.ResetsAt != nil && l.ResetsAt.After(u.ObservedAt) && l.ResetsAt.Before(reading.next) {
					reading.next = *l.ResetsAt
				}
			}
		}
		close(reading.done)
		reading.done = nil
		usageCache.Unlock()
		return u
	}
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
func anthropic(ctx context.Context, file, method, url string, body io.Reader) ([]byte, int, http.Header, error) {
	token, err := accessToken(file)
	if err != nil {
		return nil, 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := usageClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return data, res.StatusCode, res.Header, err
}

// fetchUsage reads usage windows and, with them, the account's reset offers.
func fetchUsage(ctx context.Context, file string) (*accounts.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, status, headers, err := anthropic(ctx, file, "GET", usageURL, nil)
	if status != 0 && status != http.StatusOK {
		return nil, &usageRequestError{status: status, retryAt: usageRetryAfter(headers.Get("Retry-After"), time.Now())}
	}
	if err != nil {
		return nil, err
	}
	u, err := accounts.ParseUsage(body, time.Now())
	if err != nil {
		return nil, err
	}
	u.Grants = accounts.ParseGrants(body)
	return u, nil
}
