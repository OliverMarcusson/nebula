package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testDevice  = "11111111-1111-4111-8111-111111111111"
	testSession = "22222222-2222-4222-8222-222222222222"
)

// transcript builds n JSONL records of about 1 KiB each.
func transcript(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"type":"user","n":%d,"pad":%q}`+"\n", i, strings.Repeat("x", 1000))
	}
	return []byte(b.String())
}

func bundle(data []byte) Bundle {
	return Bundle{DeviceID: testDevice, SessionID: testSession, Project: "-home-me-proj", Files: map[string][]byte{testSession + ".jsonl": data}}
}

func chunkFiles(t *testing.T, dir string) int {
	n := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".gz") {
			n++
		}
		return nil
	})
	return n
}

func TestAppendOnlyRevisionsShareChunks(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := transcript(1200) // ~1.2 MiB, several chunks
	if _, err = s.Save("me", bundle(first)); err != nil {
		t.Fatal(err)
	}
	before := chunkFiles(t, dir)
	second := append(append([]byte{}, first...), transcript(3)...)
	m, chunks := bundle(second).Split()
	ids := make([]string, 0, len(chunks))
	for id := range chunks {
		ids = append(ids, id)
	}
	missing, err := s.Missing("me", ids)
	if err != nil || len(missing) != 1 {
		t.Fatalf("appending should need exactly one new chunk, got %d (%v)", len(missing), err)
	}
	if _, err = s.Commit("me", m); !errors.Is(err, ErrMissingChunk) {
		t.Fatalf("commit before upload: %v", err)
	}
	if err = s.PutChunk("me", missing[0], chunks[missing[0]]); err != nil {
		t.Fatal(err)
	}
	if err = s.PutChunk("me", missing[0], []byte("tampered\n")); err == nil {
		t.Fatal("chunk not matching its id accepted")
	}
	snap, err := s.Commit("me", m)
	if err != nil {
		t.Fatal(err)
	}
	if got := chunkFiles(t, dir) - before; got != 1 {
		t.Fatalf("stored %d new chunks", got)
	}
	rec, err := s.Get("me", testDevice, testSession, "")
	if err != nil || rec.Snapshot.Revision != snap.Revision || string(rec.Bundle.Files[testSession+".jsonl"]) != string(second) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err = s.Get("other", testDevice, testSession, ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("revision visible to another owner")
	}
	if list, _ := s.List("me", true); len(list) != 1 || list[0].Revision != snap.Revision {
		t.Fatalf("latest list: %+v", list)
	}
}

func TestThinningAndSweep(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now().Add(-10 * 24 * time.Hour)
	clock := start
	s.now = func() time.Time { return clock }
	data := transcript(1)
	// One revision every 30 minutes for 10 days.
	for i := 0; i < 2*24*10; i++ {
		data = append(data, transcript(1)...)
		if _, err := s.Save("me", bundle(data)); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(30 * time.Minute)
	}
	list, _ := s.List("me", false)
	// 10 recent + ~48 hourly + ~8 daily, give or take bucket edges.
	if len(list) < 50 || len(list) > 75 {
		t.Fatalf("kept %d revisions", len(list))
	}
	if list[0].Revision != bundle(data).Revision() {
		t.Fatal("newest revision was thinned")
	}
	// Past the grace period, chunks only thinned revisions used are swept.
	before := chunkFiles(t, dir)
	clock = time.Now().Add(2 * sweepGrace)
	s.mu.Lock()
	s.sweep(ownerPath("me"))
	s.mu.Unlock()
	if after := chunkFiles(t, dir); after >= before {
		t.Fatalf("sweep removed nothing (%d chunks)", after)
	}
	// Every kept revision still assembles after the sweep.
	for _, m := range list {
		if _, err := s.Get("me", m.DeviceID, m.SessionID, m.Revision); err != nil {
			t.Fatalf("revision %s: %v", m.Revision[:8], err)
		}
	}
	// The index survives a reopen and is rebuilt if lost.
	s.Close()
	os.Remove(filepath.Join(dir, ownerPath("me"), "index.json"))
	s, _ = Open(dir)
	if again, _ := s.List("me", false); len(again) != len(list) {
		t.Fatalf("rebuilt index has %d, want %d", len(again), len(list))
	}
}

func TestMigratesLegacyLayout(t *testing.T) {
	dir := t.TempDir()
	b := bundle(transcript(10))
	meta := Snapshot{DeviceID: b.DeviceID, SessionID: b.SessionID, Project: b.Project, Revision: b.Revision(), StoredAt: time.Now().Add(-time.Hour).UTC(), FileCount: 1, Bytes: int64(len(b.Files[testSession+".jsonl"]))}
	base := filepath.Join(dir, ownerPath("me"), testDevice, testSession, meta.Revision)
	os.MkdirAll(filepath.Dir(base), 0700)
	raw, _ := json.Marshal(b)
	os.WriteFile(base+".json", raw, 0600)
	raw, _ = json.Marshal(meta)
	os.WriteFile(base+".meta.json", raw, 0600)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec, err := s.Get("me", testDevice, testSession, meta.Revision)
	if err != nil || !rec.Snapshot.StoredAt.Equal(meta.StoredAt) {
		t.Fatalf("migrated revision: %v %+v", err, rec.Snapshot)
	}
	if _, err := os.Stat(base + ".json"); !os.IsNotExist(err) {
		t.Fatal("legacy bundle left behind")
	}
}

func TestChunksAreLineAligned(t *testing.T) {
	data := transcript(700)
	var joined []byte
	for i, c := range Chunks(data) {
		if c[len(c)-1] != '\n' && i != len(Chunks(data))-1 {
			t.Fatal("chunk not cut at a line boundary")
		}
		joined = append(joined, c...)
	}
	if string(joined) != string(data) {
		t.Fatal("chunks do not reassemble the input")
	}
}
