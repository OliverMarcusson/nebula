// Package logins relays dashboard-initiated Claude sign-ins to a companion.
// The companion runs Claude Code's own `claude auth login` for a new profile;
// the server only carries the sign-in URL out and a pasted one-time code in.
// State is in memory: a restart abandons in-flight sign-ins.
package logins

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	Pending    = "pending"    // created, not yet claimed by the device
	Starting   = "starting"   // claimed; the device is launching claude
	Waiting    = "waiting"    // sign-in page is open; a code may be pasted
	Completing = "completing" // code delivered or browser finished; exchanging
	Completed  = "completed"
	Failed     = "failed"
	Cancelled  = "cancelled"
	Expired    = "expired"
)

const (
	lifetime   = 10 * time.Minute
	onlineFor  = 30 * time.Second
	keepFinish = 10 * time.Minute
)

var (
	ErrNotFound = errors.New("sign-in not found")
	ErrInvalid  = errors.New("invalid sign-in request")
	ErrOffline  = errors.New("device is offline")
)

type Login struct {
	ID        string    `json:"id"`
	DeviceID  string    `json:"device_id"`
	State     string    `json:"state"`
	URL       string    `json:"url,omitempty"`
	Message   string    `json:"message,omitempty"`
	AccountID string    `json:"account_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	code      string
	cancel    bool
}

type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}

// Work is what a polling device must act on.
type Work struct {
	Start  []Login           `json:"start"`
	Codes  map[string]string `json:"codes"`
	Cancel []string          `json:"cancel"`
}

type Manager struct {
	mu      sync.Mutex
	logins  map[string]map[string]*Login // owner -> id -> login
	devices map[string]map[string]*Device
	now     func() time.Time
}

func New() *Manager {
	return &Manager{logins: map[string]map[string]*Login{}, devices: map[string]map[string]*Device{}, now: time.Now}
}

// signInURL accepts only https links to Anthropic's own sign-in hosts, since
// the dashboard renders it as a link.
func signInURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || len(raw) > 4096 {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, d := range []string{"claude.com", "claude.ai", "anthropic.com"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

func final(state string) bool {
	return state == Completed || state == Failed || state == Cancelled || state == Expired
}

// prune expires stale sign-ins and forgets finished ones; callers hold mu.
func (m *Manager) prune(owner string) {
	now := m.now()
	for id, l := range m.logins[owner] {
		switch {
		case !final(l.State) && now.Sub(l.CreatedAt) > lifetime:
			l.State, l.Message, l.code, l.cancel, l.UpdatedAt = Expired, "Sign-in timed out", "", true, now
		case final(l.State) && now.Sub(l.UpdatedAt) > keepFinish:
			delete(m.logins[owner], id)
		}
	}
}

func (m *Manager) Devices(owner string) []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Device{}
	for _, d := range m.devices[owner] {
		if m.now().Sub(d.LastSeen) <= onlineFor {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (m *Manager) Create(owner, device string) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[owner][device]
	if d == nil || m.now().Sub(d.LastSeen) > onlineFor {
		return Login{}, ErrOffline
	}
	m.prune(owner)
	active := 0
	for _, l := range m.logins[owner] {
		if !final(l.State) {
			active++
		}
	}
	if active >= 4 {
		return Login{}, ErrInvalid
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return Login{}, err
	}
	now := m.now()
	l := &Login{ID: hex.EncodeToString(b), DeviceID: device, State: Pending, CreatedAt: now, UpdatedAt: now}
	if m.logins[owner] == nil {
		m.logins[owner] = map[string]*Login{}
	}
	m.logins[owner][l.ID] = l
	return *l, nil
}

func (m *Manager) Get(owner, id string) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(owner)
	l := m.logins[owner][id]
	if l == nil {
		return Login{}, ErrNotFound
	}
	return *l, nil
}

// SubmitCode stores a pasted code until the device collects it.
func (m *Manager) SubmitCode(owner, id, code string) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(owner)
	l := m.logins[owner][id]
	if l == nil {
		return Login{}, ErrNotFound
	}
	if l.State != Waiting || code == "" || len(code) > 1024 || !utf8.ValidString(code) {
		return Login{}, ErrInvalid
	}
	for _, r := range code {
		if r < 0x21 || r > 0x7e { // codes are printable ASCII; no newlines reach claude's stdin
			return Login{}, ErrInvalid
		}
	}
	l.code, l.State, l.UpdatedAt = code, Completing, m.now()
	return *l, nil
}

func (m *Manager) Cancel(owner, id string) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.logins[owner][id]
	if l == nil {
		return Login{}, ErrNotFound
	}
	if !final(l.State) {
		l.State, l.cancel, l.code, l.UpdatedAt = Cancelled, true, "", m.now()
	}
	return *l, nil
}

// Poll marks a device online and hands it new sign-ins, pasted codes, and
// cancellations. Each code and cancellation is delivered once.
func (m *Manager) Poll(owner, device, name string) Work {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.devices[owner] == nil {
		m.devices[owner] = map[string]*Device{}
	}
	if utf8.RuneCountInString(name) > 64 || !utf8.ValidString(name) {
		name = ""
	}
	m.devices[owner][device] = &Device{ID: device, Name: name, LastSeen: m.now()}
	m.prune(owner)
	w := Work{Start: []Login{}, Codes: map[string]string{}, Cancel: []string{}}
	for _, l := range m.logins[owner] {
		if l.DeviceID != device {
			continue
		}
		switch {
		case l.State == Pending:
			l.State, l.UpdatedAt = Starting, m.now()
			w.Start = append(w.Start, *l)
		case l.code != "":
			w.Codes[l.ID] = l.code
			l.code = ""
		case l.cancel:
			w.Cancel = append(w.Cancel, l.ID)
			l.cancel = false
		}
	}
	return w
}

// Update records the device's progress. Only the device the sign-in was
// created for may report it, and finished sign-ins cannot be reopened.
func (m *Manager) Update(owner, device, id, state, link, message, account string) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.logins[owner][id]
	if l == nil || l.DeviceID != device {
		return Login{}, ErrNotFound
	}
	if final(l.State) {
		return *l, nil
	}
	if len(message) > 300 || !utf8.ValidString(message) || len(account) > 64 {
		return Login{}, ErrInvalid
	}
	switch state {
	case Waiting:
		if !signInURL(link) {
			return Login{}, ErrInvalid
		}
		l.URL = link
		if l.code == "" && l.State != Completing {
			l.State = Waiting
		}
	case Completing:
		l.State = Completing
	case Completed:
		if account == "" {
			return Login{}, ErrInvalid
		}
		l.State, l.AccountID, l.code = Completed, account, ""
	case Failed:
		l.State, l.Message, l.code = Failed, message, ""
	default:
		return Login{}, ErrInvalid
	}
	l.UpdatedAt = m.now()
	return *l, nil
}
