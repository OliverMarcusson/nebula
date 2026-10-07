// Package resets tracks usage reset requests. A request names one account and
// one of its reset grants; only a fresh Claustra sign-in bound to that request
// approves it. A device holding the account then claims it once and redeems
// it with Claude. Nothing here ever retries a redemption.
//
// ponytail: state is in memory like sign-ins; a server restart abandons open
// requests, and one being redeemed loses its result. Persist when that matters.
package resets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/olivermarcusson/nebula/internal/accounts"
)

const (
	Pending   = "pending"   // waiting for the passkey sign-in
	Approved  = "approved"  // signed for; waiting for a device to claim it
	Executing = "executing" // claimed by a device, which is redeeming it
	Succeeded = "succeeded"
	Failed    = "failed"
	Cancelled = "cancelled"
	Expired   = "expired"
	Unknown   = "unknown" // the device may or may not have redeemed it
)

const (
	approveWithin = 10 * time.Minute
	claimWithin   = 10 * time.Minute
	lease         = 2 * time.Minute
	keep          = 24 * time.Hour
)

var (
	ErrNotFound = errors.New("reset request not found")
	ErrInvalid  = errors.New("invalid reset request")
)

type Reset struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"account_id"`
	OrganizationID string    `json:"organization_id"`
	GrantID        string    `json:"grant_id"`
	GrantLabel     string    `json:"grant_label,omitempty"`
	Clears         []string  `json:"clears"`
	State          string    `json:"state"`
	Message        string    `json:"message,omitempty"`
	DeviceID       string    `json:"device_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func final(state string) bool {
	return state != Pending && state != Approved && state != Executing
}

type Manager struct {
	mu     sync.Mutex
	resets map[string]map[string]*Reset // owner -> id -> request
	now    func() time.Time
}

func New() *Manager { return &Manager{resets: map[string]map[string]*Reset{}, now: time.Now} }

func (m *Manager) set(owner string, r *Reset, state, msg string) {
	r.State, r.Message, r.UpdatedAt = state, msg, m.now()
	log.Printf("Reset %s for account %s (grant %s, device %s): %s %s", r.ID, r.AccountID, r.GrantID, r.DeviceID, state, msg)
}

// prune times requests out; callers hold mu.
func (m *Manager) prune(owner string) {
	now := m.now()
	for id, r := range m.resets[owner] {
		switch {
		case r.State == Pending && now.Sub(r.CreatedAt) > approveWithin:
			m.set(owner, r, Expired, "Not approved in time")
		case r.State == Approved && now.Sub(r.UpdatedAt) > claimWithin:
			m.set(owner, r, Expired, "No device holding the account came online")
		case r.State == Executing && now.Sub(r.UpdatedAt) > lease:
			m.set(owner, r, Unknown, "The device did not report back; check the account's usage")
		case final(r.State) && now.Sub(r.UpdatedAt) > keep:
			delete(m.resets[owner], id)
		}
	}
}

// Create opens a request for one reset of a grant the account last reported.
func (m *Manager) Create(owner string, a accounts.Account, grantID string) (Reset, error) {
	if a.State != accounts.Connected || a.OrganizationUUID == "" || a.Usage == nil {
		return Reset{}, ErrInvalid
	}
	var grant *accounts.Grant
	for i, g := range a.Usage.Grants {
		if g.ID == grantID && g.ResetsLeft > 0 {
			grant = &a.Usage.Grants[i]
		}
	}
	if grant == nil {
		return Reset{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(owner)
	for _, r := range m.resets[owner] {
		if r.AccountID == a.ID && !final(r.State) {
			return Reset{}, ErrInvalid // one open request per account
		}
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return Reset{}, err
	}
	now := m.now()
	r := &Reset{
		ID: hex.EncodeToString(b), AccountID: a.ID, OrganizationID: a.OrganizationUUID,
		GrantID: grant.ID, GrantLabel: grant.Label, Clears: grant.Clears, CreatedAt: now,
	}
	if m.resets[owner] == nil {
		m.resets[owner] = map[string]*Reset{}
	}
	m.resets[owner][r.ID] = r
	m.set(owner, r, Pending, "")
	return *r, nil
}

// List returns the owner's requests, newest first.
func (m *Manager) List(owner string) []Reset {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(owner)
	out := []Reset{}
	for _, r := range m.resets[owner] {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Pending reports whether a request is still waiting for its approval.
func (m *Manager) Pending(owner, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(owner)
	r := m.resets[owner][id]
	return r != nil && r.State == Pending
}

// Approve records the passkey sign-in bound to this request. The caller has
// verified that sign-in happened after the approval started.
func (m *Manager) Approve(owner, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(owner)
	r := m.resets[owner][id]
	if r == nil || r.State != Pending {
		return ErrNotFound
	}
	m.set(owner, r, Approved, "")
	return nil
}

func (m *Manager) Cancel(owner, id string) (Reset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.resets[owner][id]
	if r == nil {
		return Reset{}, ErrNotFound
	}
	if r.State == Pending || r.State == Approved {
		m.set(owner, r, Cancelled, "")
	}
	return *r, nil
}

// Revoke cancels every request not yet claimed for an account, as when it is
// disconnected.
func (m *Manager) Revoke(owner, account string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.resets[owner] {
		if r.AccountID == account && (r.State == Pending || r.State == Approved) {
			m.set(owner, r, Cancelled, "Account disconnected")
		}
	}
}

// Claim hands a device the approved requests for accounts it holds. Each
// request is handed out once.
func (m *Manager) Claim(owner, device string, holds func(account string) bool) []Reset {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(owner)
	out := []Reset{}
	for _, r := range m.resets[owner] {
		if r.State == Approved && holds(r.AccountID) {
			r.DeviceID = device
			m.set(owner, r, Executing, "")
			out = append(out, *r)
		}
	}
	return out
}

// Finish records the result from the device that claimed the request.
func (m *Manager) Finish(owner, device, id, state, msg string) (Reset, error) {
	if (state != Succeeded && state != Failed && state != Unknown) || len(msg) > 300 || !utf8.ValidString(msg) {
		return Reset{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.resets[owner][id]
	if r == nil || r.DeviceID != device {
		return Reset{}, ErrNotFound
	}
	// A result after the lease ran out still replaces unknown: it is the truth.
	if r.State == Executing || r.State == Unknown {
		m.set(owner, r, state, msg)
	}
	return *r, nil
}
