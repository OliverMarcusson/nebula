package accounts

import (
	"errors"
	"testing"
	"time"
)

const (
	devA = "11111111-1111-4111-8111-111111111111"
	devB = "22222222-2222-4222-8222-222222222222"
	acc1 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	acc2 = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func profile(name, id string) Profile {
	return Profile{Profile: name, Identity: Identity{AccountUUID: id, Email: name + "@example.com"}}
}

func TestLifecycle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.Report("oliver", devA, Report{Profiles: []Profile{profile("default", acc1), profile("work", acc2)}})
	if err != nil || len(list) != 2 || list[0].State != Detected {
		t.Fatalf("report: %v %+v", err, list)
	}
	if other, _ := s.List("someone-else"); len(other) != 0 {
		t.Fatal("accounts leaked across owners")
	}
	s.Connect("oliver", acc2)
	list, _ = s.Connect("oliver", acc1)
	if list[0].ID != acc2 || list[0].Priority != 1 || list[1].ID != acc1 || list[1].Priority != 2 || !list[1].Enabled {
		t.Fatalf("connect order: %+v", list)
	}
	if list, _ = s.Reorder("oliver", []string{acc1, acc2}); list[0].ID != acc1 {
		t.Fatalf("reorder: %+v", list)
	}
	if _, err = s.Reorder("oliver", []string{acc1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial order accepted: %v", err)
	}
	// Usage only moves forward in time.
	now := time.Now().UTC()
	p := profile("default", acc1)
	p.Usage = &Usage{ObservedAt: now, Limits: []Limit{{Kind: "session", Percent: 40}}}
	s.Report("oliver", devB, Report{Profiles: []Profile{p}})
	p.Usage = &Usage{ObservedAt: now.Add(-time.Hour), Limits: []Limit{{Kind: "session", Percent: 90}}}
	list, _ = s.Report("oliver", devA, Report{Profiles: []Profile{p}})
	if list[0].Usage.Limits[0].Percent != 40 || list[0].Usage.DeviceID != devB || len(list[0].Sightings) != 2 {
		t.Fatalf("usage/sightings: %+v", list[0])
	}
	// acc2 is no longer signed in anywhere but stays connected until disconnected.
	if list[1].ID != acc2 || len(list[1].Sightings) != 0 || list[1].State != Connected {
		t.Fatalf("connected account dropped: %+v", list[1])
	}
	list, _ = s.Disconnect("oliver", acc2)
	if len(list) != 1 {
		t.Fatalf("disconnect of unsighted account: %+v", list)
	}
	list, _ = s.Disconnect("oliver", acc1)
	if list[0].State != Detected || list[0].Priority != 0 {
		t.Fatalf("disconnect of sighted account: %+v", list)
	}
	if _, err = s.Report("oliver", devA, Report{Profiles: []Profile{profile("../x", acc1)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad profile accepted: %v", err)
	}
}

func TestCandidates(t *testing.T) {
	now := time.Now()
	soon, later := now.Add(time.Hour), now.Add(3*time.Hour)
	list := []Account{
		{ID: "a", State: Connected, Enabled: true, Priority: 1, LimitedUntil: &later},
		{ID: "b", State: Connected, Enabled: true, Priority: 2},
		{ID: "c", State: Connected, Enabled: true, Priority: 3, Usage: &Usage{ObservedAt: now, Limits: []Limit{{Kind: "session", Percent: 100, ResetsAt: &soon}}}},
		{ID: "d", State: Connected, Enabled: false, Priority: 4},
		{ID: "e", State: Detected, Enabled: false},
		{ID: "f", State: Connected, Enabled: true, Priority: 0},
		// A stale exhausted reading is not evidence.
		{ID: "g", State: Connected, Enabled: true, Priority: 5, Usage: &Usage{ObservedAt: now.Add(-7 * time.Hour), Limits: []Limit{{Kind: "session", Percent: 100}}}},
	}
	local := func(id string) bool { return id != "f" }
	usable, limited := Candidates(list, local, now)
	ids := func(l []Account) (out []string) {
		for _, a := range l {
			out = append(out, a.ID)
		}
		return
	}
	if got := ids(usable); len(got) != 2 || got[0] != "b" || got[1] != "g" {
		t.Fatalf("usable %v", got)
	}
	if got := ids(limited); len(got) != 2 || got[0] != "c" || got[1] != "a" {
		t.Fatalf("limited %v", got)
	}
}

func TestLimitClearedByFreshReading(t *testing.T) {
	s, _ := Open(t.TempDir())
	defer s.Close()
	s.Report("me", devA, Report{Profiles: []Profile{profile("default", acc1)}})
	s.Connect("me", acc1)
	list, _ := s.SetLimited("me", acc1, nil)
	if !list[0].Limited(time.Now()) {
		t.Fatal("not limited")
	}
	p := profile("default", acc1)
	p.Usage = &Usage{ObservedAt: time.Now().Add(time.Minute), Limits: []Limit{{Kind: "session", Percent: 20}}}
	list, _ = s.Report("me", devA, Report{Profiles: []Profile{p}})
	if list[0].Limited(time.Now()) {
		t.Fatal("fresh reading with room did not clear the limit")
	}
}
