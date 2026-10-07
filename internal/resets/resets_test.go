package resets

import (
	"testing"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
)

func TestLifecycle(t *testing.T) {
	now := time.Now()
	m := New()
	m.now = func() time.Time { return now }
	a := accounts.Account{ID: "acc", State: accounts.Connected, Identity: accounts.Identity{OrganizationUUID: "org"},
		Usage: &accounts.Usage{Grants: []accounts.Grant{{ID: "g1", ResetsLeft: 1}, {ID: "spent", ResetsLeft: 0}}}}
	holds := func(id string) bool { return id == "acc" }

	if _, err := m.Create("o", a, "spent"); err == nil {
		t.Fatal("request for a grant with no resets left")
	}
	r, err := m.Create("o", a, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("o", a, "g1"); err == nil {
		t.Fatal("second open request for one account")
	}
	if got := m.Claim("o", "dev", holds); len(got) != 0 {
		t.Fatal("claimed before approval")
	}
	if err := m.Approve("other", r.ID); err == nil {
		t.Fatal("approved for another owner")
	}
	if err := m.Approve("o", r.ID); err != nil {
		t.Fatal(err)
	}
	if got := m.Claim("o", "dev", func(string) bool { return false }); len(got) != 0 {
		t.Fatal("claimed by a device without the account")
	}
	if got := m.Claim("o", "dev", holds); len(got) != 1 || got[0].OrganizationID != "org" || got[0].GrantID != "g1" {
		t.Fatalf("claim: %+v", got)
	}
	if got := m.Claim("o", "dev2", holds); len(got) != 0 {
		t.Fatal("claimed twice")
	}
	if _, err := m.Finish("o", "dev2", r.ID, Succeeded, ""); err == nil {
		t.Fatal("finished by another device")
	}
	now = now.Add(lease + time.Second)
	if m.List("o")[0].State != Unknown {
		t.Fatal("lease ran out without becoming unknown")
	}
	if got, _ := m.Finish("o", "dev", r.ID, Succeeded, ""); got.State != Succeeded {
		t.Fatalf("late result: %+v", got)
	}
	if err := m.Approve("o", r.ID); err == nil {
		t.Fatal("finished request approved again")
	}
}
