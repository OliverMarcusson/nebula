// Package accounts records which Claude accounts a user's devices are signed
// into and the user's preferences for them. Devices report identity and cached
// usage only; Claude credentials never leave the device.
package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var label = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

var (
	ErrNotFound = errors.New("account not found")
	ErrInvalid  = errors.New("invalid request")
)

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

const (
	Detected  = "detected"  // reported by a device, not yet connected by the user
	Connected = "connected" // connected by the user and ordered for switching
)

type Identity struct {
	AccountUUID      string `json:"account_uuid"`
	Email            string `json:"email,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
	OrganizationUUID string `json:"organization_uuid,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	OrganizationType string `json:"organization_type,omitempty"`
	RateLimitTier    string `json:"rate_limit_tier,omitempty"`
}

type Limit struct {
	Kind     string     `json:"kind"`
	Group    string     `json:"group,omitempty"`
	Percent  float64    `json:"percent"`
	Severity string     `json:"severity,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Active   bool       `json:"active"`
}

// Grant is a usage reset offer: a number of resets, each clearing the listed
// usage windows. UsableNow is Claude's own verdict at the time of reading.
type Grant struct {
	ID          string     `json:"id"`
	Label       string     `json:"label,omitempty"`
	ResetsTotal int        `json:"resets_total"`
	ResetsLeft  int        `json:"resets_left"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
	Clears      []string   `json:"clears"`
	Paused      bool       `json:"paused,omitempty"`
	UsableNow   bool       `json:"usable_now"`
	NeedsLimit  bool       `json:"needs_limit,omitempty"`
}

// GrantID matches the grant ids Claude accepts when a reset is redeemed.
var GrantID = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// Usage is the native client's own cached usage reading, attributed to the
// reporting device rather than independently verified. Grants is nil when the
// reading did not include reset offers (Claude Code's cache never does).
type Usage struct {
	ObservedAt time.Time `json:"observed_at"`
	DeviceID   string    `json:"device_id"`
	Limits     []Limit   `json:"limits"`
	Grants     []Grant   `json:"grants"`
}

type Profile struct {
	Profile string `json:"profile"`
	Identity
	Usage *Usage `json:"usage,omitempty"`
}

type Report struct {
	Profiles []Profile `json:"profiles"`
}

type Sighting struct {
	DeviceID   string    `json:"device_id"`
	Profile    string    `json:"profile"`
	ReportedAt time.Time `json:"reported_at"`
}

type Account struct {
	ID string `json:"id"`
	Identity
	State       string     `json:"state"`
	Enabled     bool       `json:"enabled"`
	Priority    int        `json:"priority"`
	Sightings   []Sighting `json:"sightings"`
	Usage       *Usage     `json:"usage,omitempty"`
	FirstSeen   time.Time  `json:"first_seen"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	// LimitedUntil is set when a device saw this account hit a usage limit.
	LimitedUntil *time.Time `json:"limited_until,omitempty"`
}

func short(s string, n int) bool { return utf8.ValidString(s) && len(s) <= n }

func (r Report) Validate() error {
	if len(r.Profiles) > 32 {
		return invalid("too many profiles")
	}
	seen := map[string]bool{}
	for _, p := range r.Profiles {
		if !label.MatchString(p.Profile) || seen[p.Profile] {
			return invalid("invalid or duplicate profile name")
		}
		seen[p.Profile] = true
		id := p.Identity
		if !uuid.MatchString(id.AccountUUID) || (id.OrganizationUUID != "" && !uuid.MatchString(id.OrganizationUUID)) {
			return invalid("invalid account identity")
		}
		for _, s := range []string{id.Email, id.DisplayName, id.OrganizationName, id.OrganizationType, id.RateLimitTier} {
			if !short(s, 256) {
				return invalid("account field too long")
			}
		}
		if p.Usage != nil {
			if len(p.Usage.Limits) > 32 || p.Usage.ObservedAt.IsZero() || p.Usage.ObservedAt.After(time.Now().Add(5*time.Minute)) {
				return invalid("invalid usage observation")
			}
			for _, l := range p.Usage.Limits {
				if !short(l.Kind, 64) || l.Kind == "" || !short(l.Group, 64) || !short(l.Severity, 32) || l.Percent < 0 || l.Percent > 1000 {
					return invalid("invalid usage limit")
				}
			}
			if len(p.Usage.Grants) > 16 {
				return invalid("too many reset grants")
			}
			for _, g := range p.Usage.Grants {
				if !GrantID.MatchString(g.ID) || !short(g.Label, 128) || g.ResetsLeft < 0 || g.ResetsTotal < 0 || len(g.Clears) > 16 {
					return invalid("invalid reset grant")
				}
				for _, c := range g.Clears {
					if !short(c, 64) {
						return invalid("invalid reset grant")
					}
				}
			}
		}
	}
	return nil
}

type Store struct {
	root *os.Root
	mu   sync.Mutex
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Store{root: r}, nil
}

func (s *Store) Close() error { return s.root.Close() }

func file(owner string) string {
	h := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(h[:]) + ".json"
}

func (s *Store) load(owner string) (map[string]*Account, error) {
	m := map[string]*Account{}
	data, err := s.root.ReadFile(file(owner))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(data, &m)
}

func (s *Store) save(owner string, m map[string]*Account) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.root.Name(), ".accounts-")
	if err != nil {
		return err
	}
	tmp := filepath.Base(f.Name())
	defer s.root.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return s.root.Rename(tmp, file(owner))
}

func sorted(m map[string]*Account) []Account {
	out := make([]Account, 0, len(m))
	for _, a := range m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.State != b.State {
			return a.State == Connected
		}
		if a.State == Connected && a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.FirstSeen.Before(b.FirstSeen)
	})
	return out
}

// renumber keeps connected priorities dense: 1..n in their current order.
func renumber(m map[string]*Account) {
	list := sorted(m)
	n := 0
	for _, a := range list {
		if a.State == Connected {
			n++
			m[a.ID].Priority = n
		} else {
			m[a.ID].Priority = 0
		}
	}
}

func (s *Store) update(owner string, fn func(map[string]*Account) error) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load(owner)
	if err != nil {
		return nil, err
	}
	if err = fn(m); err != nil {
		return nil, err
	}
	renumber(m)
	if err = s.save(owner, m); err != nil {
		return nil, err
	}
	return sorted(m), nil
}

func (s *Store) List(owner string) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load(owner)
	if err != nil {
		return nil, err
	}
	return sorted(m), nil
}

// Report replaces the set of profiles a device is signed into. New accounts
// start as detected; the user must connect them explicitly.
func (s *Store) Report(owner, device string, r Report) ([]Account, error) {
	if !uuid.MatchString(device) {
		return nil, invalid("invalid device")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return s.update(owner, func(m map[string]*Account) error {
		for _, a := range m {
			kept := []Sighting{}
			for _, v := range a.Sightings {
				if v.DeviceID != device {
					kept = append(kept, v)
				}
			}
			a.Sightings = kept
		}
		for _, p := range r.Profiles {
			a := m[p.AccountUUID]
			if a == nil {
				a = &Account{ID: p.AccountUUID, State: Detected, FirstSeen: now, Sightings: []Sighting{}}
				m[a.ID] = a
			}
			a.Identity = p.Identity
			a.Sightings = append(a.Sightings, Sighting{DeviceID: device, Profile: p.Profile, ReportedAt: now})
			if p.Usage != nil && (a.Usage == nil || p.Usage.ObservedAt.After(a.Usage.ObservedAt)) {
				u := *p.Usage
				u.DeviceID = device
				if u.Grants == nil && a.Usage != nil {
					u.Grants = a.Usage.Grants // a cached reading knows nothing of resets
				}
				a.Usage = &u
				// A reading after the limit was hit that shows room clears it.
				if a.LimitedUntil != nil && !u.Exhausted(now) && u.ObservedAt.After(a.LimitedUntil.Add(-limitedFor)) {
					a.LimitedUntil = nil
				}
			}
		}
		for id, a := range m {
			if len(a.Sightings) == 0 && a.State == Detected {
				delete(m, id) // a detected account no device is signed into anymore
			}
		}
		return nil
	})
}

func (s *Store) Connect(owner, id string) ([]Account, error) {
	return s.update(owner, func(m map[string]*Account) error {
		a := m[id]
		if a == nil {
			return ErrNotFound
		}
		if a.State != Connected {
			now := time.Now().UTC()
			a.State, a.Enabled, a.ConnectedAt, a.Priority = Connected, true, &now, 1<<30
		}
		return nil
	})
}

// Disconnect forgets the user's preferences for an account. If a device is
// still signed into it, it remains listed as detected.
func (s *Store) Disconnect(owner, id string) ([]Account, error) {
	return s.update(owner, func(m map[string]*Account) error {
		a := m[id]
		if a == nil {
			return ErrNotFound
		}
		if len(a.Sightings) == 0 {
			delete(m, id)
			return nil
		}
		a.State, a.Enabled, a.ConnectedAt = Detected, false, nil
		return nil
	})
}

func (s *Store) SetEnabled(owner, id string, enabled bool) ([]Account, error) {
	return s.update(owner, func(m map[string]*Account) error {
		a := m[id]
		if a == nil || a.State != Connected {
			return ErrNotFound
		}
		a.Enabled = enabled
		return nil
	})
}

// limitedFor is how long a limit without a known reset time is assumed to last.
const limitedFor = time.Hour

// SetLimited records that a device saw the account hit a usage limit. Only an
// enabled, connected account is tracked; a missing reset time assumes an hour.
func (s *Store) SetLimited(owner, id string, until *time.Time) ([]Account, error) {
	now := time.Now().UTC()
	if until == nil || until.Before(now) || until.After(now.Add(8*24*time.Hour)) {
		t := now.Add(limitedFor)
		until = &t
	}
	return s.update(owner, func(m map[string]*Account) error {
		a := m[id]
		if a == nil {
			return ErrNotFound
		}
		u := until.UTC()
		a.LimitedUntil = &u
		return nil
	})
}

// staleAfter bounds how long a usage reading counts as evidence.
const staleAfter = 6 * time.Hour

// Limited reports whether the account should be avoided right now.
func (a Account) Limited(now time.Time) bool {
	if a.LimitedUntil != nil && a.LimitedUntil.After(now) {
		return true
	}
	return a.Usage != nil && now.Sub(a.Usage.ObservedAt) < staleAfter && a.Usage.Exhausted(now)
}

// Candidates orders the accounts a device may use: connected and enabled ones
// it has a profile for, those with room first in priority order, then limited
// ones by soonest expected reset as a last resort.
func Candidates(list []Account, local func(id string) bool, now time.Time) (usable, limited []Account) {
	for _, a := range list {
		if a.State != Connected || !a.Enabled || !local(a.ID) {
			continue
		}
		if a.Limited(now) {
			limited = append(limited, a)
		} else {
			usable = append(usable, a)
		}
	}
	sort.SliceStable(usable, func(i, j int) bool { return usable[i].Priority < usable[j].Priority })
	// An account is available again once its last exhausted window resets.
	reset := func(a Account) time.Time {
		var t time.Time
		if a.LimitedUntil != nil {
			t = *a.LimitedUntil
		}
		if a.Usage != nil {
			for _, l := range a.Usage.Limits {
				if l.Percent >= 100 && l.ResetsAt != nil && l.ResetsAt.After(t) {
					t = *l.ResetsAt
				}
			}
		}
		if t.IsZero() {
			t = now.Add(8 * 24 * time.Hour)
		}
		return t
	}
	sort.SliceStable(limited, func(i, j int) bool { return reset(limited[i]).Before(reset(limited[j])) })
	return usable, limited
}

// Reorder sets the switching priority; ids must list every connected account once.
func (s *Store) Reorder(owner string, ids []string) ([]Account, error) {
	return s.update(owner, func(m map[string]*Account) error {
		connected := 0
		for _, a := range m {
			if a.State == Connected {
				connected++
			}
		}
		if len(ids) != connected {
			return invalid("order must list every connected account")
		}
		seen := map[string]bool{}
		for i, id := range ids {
			a := m[id]
			if a == nil || a.State != Connected || seen[id] {
				return invalid("order must list every connected account")
			}
			seen[id] = true
			a.Priority = i + 1
		}
		return nil
	})
}
