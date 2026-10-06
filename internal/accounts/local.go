package accounts

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"
)

// ReadLocal reads the signed-in account and the native client's cached usage
// from a Claude Code global config file (.claude.json). It returns nil when the
// profile is not signed in. OAuth tokens live elsewhere and are never read.
func ReadLocal(name, configFile string) (*Profile, error) {
	f, err := os.Open(configFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil {
		return nil, err
	}
	var cfg struct {
		OAuthAccount *struct {
			AccountUUID      string `json:"accountUuid"`
			Email            string `json:"emailAddress"`
			DisplayName      string `json:"displayName"`
			OrganizationUUID string `json:"organizationUuid"`
			OrganizationName string `json:"organizationName"`
			OrganizationType string `json:"organizationType"`
			RateLimitTier    string `json:"organizationRateLimitTier"`
		} `json:"oauthAccount"`
		Usage *struct {
			FetchedAtMs int64           `json:"fetchedAtMs"`
			AccountUUID string          `json:"accountUuid"`
			Utilization json.RawMessage `json:"utilization"`
		} `json:"cachedUsageUtilization"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, errors.New("unreadable Claude config " + configFile)
	}
	a := cfg.OAuthAccount
	if a == nil || !uuid.MatchString(a.AccountUUID) {
		return nil, nil
	}
	p := &Profile{Profile: name, Identity: Identity{
		AccountUUID: a.AccountUUID, Email: a.Email, DisplayName: a.DisplayName,
		OrganizationUUID: a.OrganizationUUID, OrganizationName: a.OrganizationName,
		OrganizationType: a.OrganizationType, RateLimitTier: a.RateLimitTier,
	}}
	if !uuid.MatchString(p.OrganizationUUID) {
		p.OrganizationUUID = ""
	}
	// Only trust a usage reading cached for this same account.
	if u := cfg.Usage; u != nil && u.AccountUUID == a.AccountUUID && u.FetchedAtMs > 0 {
		if usage, err := ParseUsage(u.Utilization, time.UnixMilli(u.FetchedAtMs)); err == nil {
			p.Usage = usage
		}
	}
	return p, nil
}

// ParseUsage reads the `limits` list of Claude's usage response, the shape
// both GET /api/oauth/usage and Claude Code's cached copy share.
func ParseUsage(raw []byte, observed time.Time) (*Usage, error) {
	var body struct {
		Limits []struct {
			Kind     string     `json:"kind"`
			Group    string     `json:"group"`
			Percent  *float64   `json:"percent"`
			Severity string     `json:"severity"`
			ResetsAt *time.Time `json:"resets_at"`
			IsActive bool       `json:"is_active"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	usage := &Usage{ObservedAt: observed.UTC(), Limits: []Limit{}}
	for _, l := range body.Limits {
		if l.Kind == "" || l.Percent == nil || len(usage.Limits) == 32 {
			continue
		}
		usage.Limits = append(usage.Limits, Limit{Kind: l.Kind, Group: l.Group, Percent: *l.Percent, Severity: l.Severity, ResetsAt: l.ResetsAt, Active: l.IsActive})
	}
	return usage, nil
}

// Exhausted reports whether a reading shows any limit used up and not yet reset.
func (u *Usage) Exhausted(now time.Time) bool {
	if u == nil {
		return false
	}
	for _, l := range u.Limits {
		if l.Percent >= 100 && (l.ResetsAt == nil || l.ResetsAt.After(now)) {
			return true
		}
	}
	return false
}
