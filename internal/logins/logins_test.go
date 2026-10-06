package logins

import (
	"errors"
	"testing"
	"time"
)

const dev = "11111111-1111-4111-8111-111111111111"

func TestFlow(t *testing.T) {
	m := New()
	if _, err := m.Create("o", dev); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline device accepted: %v", err)
	}
	m.Poll("o", dev, "laptop")
	l, err := m.Create("o", dev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Get("other", l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("sign-in visible to another owner")
	}
	w := m.Poll("o", dev, "laptop")
	if len(w.Start) != 1 || w.Start[0].ID != l.ID {
		t.Fatalf("not started: %+v", w)
	}
	if len(m.Poll("o", dev, "").Start) != 0 {
		t.Fatal("started twice")
	}
	if _, err = m.Update("o", dev, l.ID, Waiting, "https://evil.example/login", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign sign-in URL accepted")
	}
	if _, err = m.Update("o", "22222222-2222-4222-8222-222222222222", l.ID, Waiting, "https://claude.com/cai/oauth/authorize", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("another device updated the sign-in")
	}
	if _, err = m.Update("o", dev, l.ID, Waiting, "https://claude.com/cai/oauth/authorize?x=1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SubmitCode("o", l.ID, "abc\ndef"); !errors.Is(err, ErrInvalid) {
		t.Fatal("code with newline accepted")
	}
	if _, err = m.SubmitCode("o", l.ID, "abc#def"); err != nil {
		t.Fatal(err)
	}
	if w = m.Poll("o", dev, ""); w.Codes[l.ID] != "abc#def" {
		t.Fatalf("code not delivered: %+v", w)
	}
	if w = m.Poll("o", dev, ""); len(w.Codes) != 0 {
		t.Fatal("code delivered twice")
	}
	got, _ := m.Update("o", dev, l.ID, Completed, "", "", "acct")
	if got.State != Completed {
		t.Fatalf("not completed: %+v", got)
	}
	if got, _ = m.Update("o", dev, l.ID, Failed, "", "late", ""); got.State != Completed {
		t.Fatal("finished sign-in reopened")
	}
}

func TestExpiryAndCancel(t *testing.T) {
	m := New()
	now := time.Now()
	m.now = func() time.Time { return now }
	m.Poll("o", dev, "")
	a, _ := m.Create("o", dev)
	b, _ := m.Create("o", dev)
	m.Poll("o", dev, "")
	m.Cancel("o", a.ID)
	if w := m.Poll("o", dev, ""); len(w.Cancel) != 1 || w.Cancel[0] != a.ID {
		t.Fatalf("cancel not delivered: %+v", w)
	}
	now = now.Add(lifetime + time.Second)
	if got, _ := m.Get("o", b.ID); got.State != Expired {
		t.Fatalf("not expired: %+v", got)
	}
	if len(m.Devices("o")) != 0 {
		t.Fatal("stale device listed as online")
	}
}
