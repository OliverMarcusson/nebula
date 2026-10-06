package sessions

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Storage layout, per owner (directory named by a hash of the owner):
//
//	chunks/<aa>/<sha256>.gz            gzip of one chunk, addressed by its plain SHA-256
//	<device>/<session>/<rev>.manifest  the revision's snapshot and per-file chunk lists
//	index.json                         every stored snapshot, so listing reads one file
//
// Transcripts are append-only JSONL, so chunks cut at line boundaries after a
// fixed offset stay identical between revisions: a new revision stores only
// its changed tail. Old revisions are thinned and unreferenced chunks swept.

const (
	chunkTarget = 256 << 10
	// ChunkMax bounds one chunk; a single longer line is cut mid-line.
	ChunkMax = 16 << 20
	// keepRecent revisions of a session are always kept; older ones are thinned
	// to one per hour for two days, then one per day.
	keepRecent = 10
	hourly     = 48 * time.Hour
	// sweepAfter deleted manifests trigger a chunk sweep; sweepGrace protects
	// chunks uploaded or confirmed for a commit that has not happened yet.
	sweepAfter = 64
	sweepGrace = time.Hour
)

var (
	ErrMissingChunk = errors.New("missing chunk")
	// ErrInvalid marks a rejected upload, as opposed to a storage failure.
	ErrInvalid = errors.New("invalid session")
)

func invalid(err error) error { return fmt.Errorf("%w: %v", ErrInvalid, err) }

// Chunks splits data at the first newline at or after each chunkTarget bytes.
func Chunks(data []byte) [][]byte {
	out := [][]byte{}
	for len(data) > 0 {
		n := len(data)
		if n > chunkTarget {
			if i := bytes.IndexByte(data[chunkTarget:], '\n'); i >= 0 {
				n = chunkTarget + i + 1
			}
		}
		if n > ChunkMax {
			n = ChunkMax
		}
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

func ChunkID(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Manifest is a bundle by reference: each file as its ordered chunk IDs.
type Manifest struct {
	DeviceID  string              `json:"device_id"`
	SessionID string              `json:"session_id"`
	Project   string              `json:"project"`
	Files     map[string][]string `json:"files"`
}

// Split returns the bundle's manifest and the chunks it references.
func (b Bundle) Split() (Manifest, map[string][]byte) {
	m := Manifest{DeviceID: b.DeviceID, SessionID: b.SessionID, Project: b.Project, Files: map[string][]string{}}
	chunks := map[string][]byte{}
	for p, data := range b.Files {
		ids := []string{}
		for _, c := range Chunks(data) {
			id := ChunkID(c)
			chunks[id] = c
			ids = append(ids, id)
		}
		m.Files[p] = ids
	}
	return m, chunks
}

type stored struct {
	Snapshot Snapshot            `json:"snapshot"`
	Files    map[string][]string `json:"files"`
}

type Store struct {
	root    *os.Root
	mu      sync.Mutex
	index   map[string]map[string]Snapshot // owner dir -> device/session/revision -> snapshot
	deleted map[string]int
	now     func() time.Time
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{root: r, index: map[string]map[string]Snapshot{}, deleted: map[string]int{}, now: time.Now}
	if err = s.migrate(); err != nil {
		r.Close()
		return nil, fmt.Errorf("migrating archive: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.root.Close() }

func ownerPath(owner string) string {
	h := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(h[:])
}

func key(device, session, rev string) string { return device + "/" + session + "/" + rev }

func chunkPath(oh, id string) string { return path.Join(oh, "chunks", id[:2], id+".gz") }

func (s *Store) write(p string, data []byte) error {
	if err := s.root.MkdirAll(path.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Join(s.root.Name(), filepath.FromSlash(path.Dir(p))), ".tmp-")
	if err != nil {
		return err
	}
	tmp, err := filepath.Rel(s.root.Name(), f.Name())
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	tmp = filepath.ToSlash(tmp)
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
	if err = s.root.Rename(tmp, p); err != nil {
		return err
	}
	d, err := s.root.Open(path.Dir(p))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// loadIndex reads an owner's index, rebuilding it from manifests if absent.
func (s *Store) loadIndex(oh string) (map[string]Snapshot, error) {
	if idx := s.index[oh]; idx != nil {
		return idx, nil
	}
	idx := map[string]Snapshot{}
	data, err := s.root.ReadFile(path.Join(oh, "index.json"))
	switch {
	case err == nil:
		var list []Snapshot
		if err = json.Unmarshal(data, &list); err != nil {
			return nil, err
		}
		for _, m := range list {
			idx[key(m.DeviceID, m.SessionID, m.Revision)] = m
		}
	case errors.Is(err, fs.ErrNotExist):
		err = fs.WalkDir(s.root.FS(), oh, func(p string, e fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if e.IsDir() && path.Base(p) == "chunks" {
				return fs.SkipDir
			}
			if !e.Type().IsRegular() || !strings.HasSuffix(p, ".manifest") {
				return nil
			}
			var st stored
			raw, err := s.root.ReadFile(p)
			if err == nil {
				err = json.Unmarshal(raw, &st)
			}
			if err != nil {
				return err
			}
			m := st.Snapshot
			idx[key(m.DeviceID, m.SessionID, m.Revision)] = m
			return nil
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	s.index[oh] = idx
	return idx, nil
}

func (s *Store) saveIndex(oh string) error {
	list := make([]Snapshot, 0, len(s.index[oh]))
	for _, m := range s.index[oh] {
		list = append(list, m)
	}
	sortSnapshots(list)
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.write(path.Join(oh, "index.json"), data)
}

func sortSnapshots(list []Snapshot) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].StoredAt.Equal(list[j].StoredAt) {
			return list[i].Revision > list[j].Revision
		}
		return list[i].StoredAt.After(list[j].StoredAt)
	})
}

// Missing returns the chunk IDs the owner has not stored. Chunks it already
// has are touched so a sweep cannot remove them before the commit.
func (s *Store) Missing(owner string, ids []string) ([]string, error) {
	oh := ownerPath(owner)
	now := s.now()
	missing := []string{}
	for _, id := range ids {
		if !Digest(id) {
			return nil, invalid(errors.New("invalid chunk id"))
		}
		p := chunkPath(oh, id)
		if _, err := s.root.Stat(p); err == nil {
			_ = s.root.Chtimes(p, now, now)
		} else if errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, id)
		} else {
			return nil, err
		}
	}
	return missing, nil
}

// PutChunk stores one chunk after checking it matches its ID.
func (s *Store) PutChunk(owner, id string, data []byte) error {
	if !Digest(id) || len(data) == 0 || len(data) > ChunkMax {
		return invalid(errors.New("invalid chunk"))
	}
	if ChunkID(data) != id {
		return invalid(errors.New("chunk does not match its id"))
	}
	return s.putChunkOwner(ownerPath(owner), id, data)
}

func (s *Store) readChunk(oh, id string) ([]byte, error) {
	f, err := s.root.Open(chunkPath(oh, id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w %s", ErrMissingChunk, id[:12])
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(zr, ChunkMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > ChunkMax || ChunkID(data) != id {
		return nil, errors.New("stored chunk is corrupt")
	}
	return data, nil
}

// assemble rebuilds a bundle from its chunks, bounded by MaxBytes.
func (s *Store) assemble(oh string, m Manifest) (Bundle, error) {
	if len(m.Files) > 512 {
		return Bundle{}, invalid(errors.New("too many files"))
	}
	b := Bundle{DeviceID: m.DeviceID, SessionID: m.SessionID, Project: m.Project, Files: map[string][]byte{}}
	total := 0
	for p, ids := range m.Files {
		var buf bytes.Buffer
		for _, id := range ids {
			if !Digest(id) {
				return Bundle{}, invalid(errors.New("invalid chunk id"))
			}
			c, err := s.readChunk(oh, id)
			if err != nil {
				return Bundle{}, err
			}
			total += len(c)
			if total > MaxBytes {
				return Bundle{}, invalid(errors.New("session is too large"))
			}
			buf.Write(c)
		}
		b.Files[p] = buf.Bytes()
	}
	return b, nil
}

// Commit stores a revision whose chunks are already uploaded. Committing an
// existing revision returns its snapshot unchanged.
func (s *Store) Commit(owner string, m Manifest) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(ownerPath(owner), m)
}

func (s *Store) commit(oh string, m Manifest) (Snapshot, error) {
	if !ID(m.DeviceID) || !ID(m.SessionID) {
		return Snapshot{}, invalid(errors.New("invalid session identity"))
	}
	b, err := s.assemble(oh, m)
	if err != nil {
		return Snapshot{}, err
	}
	if err = b.Validate(); err != nil {
		return Snapshot{}, invalid(err)
	}
	idx, err := s.loadIndex(oh)
	if err != nil {
		return Snapshot{}, err
	}
	rev := b.Revision()
	if existing, ok := idx[key(b.DeviceID, b.SessionID, rev)]; ok {
		return existing, nil
	}
	snap := Snapshot{DeviceID: b.DeviceID, SessionID: b.SessionID, Project: b.Project, Revision: rev, StoredAt: s.now().UTC(), FileCount: len(b.Files), Title: b.Title()}
	for _, data := range b.Files {
		snap.Bytes += int64(len(data))
	}
	data, err := json.Marshal(stored{Snapshot: snap, Files: m.Files})
	if err != nil {
		return Snapshot{}, err
	}
	if err = s.write(path.Join(oh, b.DeviceID, b.SessionID, rev+".manifest"), data); err != nil {
		return Snapshot{}, err
	}
	idx[key(b.DeviceID, b.SessionID, rev)] = snap
	s.thin(oh, b.DeviceID, b.SessionID)
	if err = s.saveIndex(oh); err != nil {
		return Snapshot{}, err
	}
	if s.deleted[oh] >= sweepAfter {
		s.sweep(oh)
	}
	return snap, nil
}

// Save stores a whole bundle (migration, tests, and local callers).
func (s *Store) Save(owner string, b Bundle) (Snapshot, error) {
	if err := b.Validate(); err != nil {
		return Snapshot{}, err
	}
	m, chunks := b.Split()
	for id, c := range chunks {
		if err := s.PutChunk(owner, id, c); err != nil {
			return Snapshot{}, err
		}
	}
	return s.Commit(owner, m)
}

// thin keeps a session's newest revisions, then the newest per hour for two
// days, then the newest per day. Callers hold mu.
func (s *Store) thin(oh, device, session string) {
	list := []Snapshot{}
	for _, m := range s.index[oh] {
		if m.DeviceID == device && m.SessionID == session {
			list = append(list, m)
		}
	}
	sortSnapshots(list)
	now := s.now()
	buckets := map[string]bool{}
	for i, m := range list {
		bucket := m.StoredAt.UTC().Format("2006-01-02")
		if now.Sub(m.StoredAt) < hourly {
			bucket = m.StoredAt.UTC().Format("2006-01-02T15")
		}
		if i < keepRecent || !buckets[bucket] {
			buckets[bucket] = true
			continue
		}
		if err := s.root.Remove(path.Join(oh, device, session, m.Revision+".manifest")); err == nil || errors.Is(err, fs.ErrNotExist) {
			delete(s.index[oh], key(device, session, m.Revision))
			s.deleted[oh]++
		}
	}
}

// sweep removes chunks no manifest references, sparing recently touched ones.
func (s *Store) sweep(oh string) {
	referenced := map[string]bool{}
	for _, m := range s.index[oh] {
		raw, err := s.root.ReadFile(path.Join(oh, m.DeviceID, m.SessionID, m.Revision+".manifest"))
		if err != nil {
			return // never sweep on an incomplete view
		}
		var st stored
		if json.Unmarshal(raw, &st) != nil {
			return
		}
		for _, ids := range st.Files {
			for _, id := range ids {
				referenced[id] = true
			}
		}
	}
	cutoff := s.now().Add(-sweepGrace)
	_ = fs.WalkDir(s.root.FS(), path.Join(oh, "chunks"), func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		id := strings.TrimSuffix(path.Base(p), ".gz")
		if referenced[id] {
			return nil
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = s.root.Remove(p)
		}
		return nil
	})
	s.deleted[oh] = 0
}

// List returns snapshots newest first; latest keeps only each session's newest.
func (s *Store) List(owner string, latest bool) ([]Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.loadIndex(ownerPath(owner))
	if err != nil {
		return nil, err
	}
	list := make([]Snapshot, 0, len(idx))
	for _, m := range idx {
		list = append(list, m)
	}
	sortSnapshots(list)
	if latest {
		seen := map[string]bool{}
		kept := list[:0]
		for _, m := range list {
			if k := m.DeviceID + "/" + m.SessionID; !seen[k] {
				seen[k] = true
				kept = append(kept, m)
			}
		}
		list = kept
	}
	return list, nil
}

func (s *Store) Get(owner, device, session, revision string) (Record, error) {
	if !ID(device) || !ID(session) || (revision != "" && !Digest(revision)) {
		return Record{}, fs.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oh := ownerPath(owner)
	idx, err := s.loadIndex(oh)
	if err != nil {
		return Record{}, err
	}
	if revision == "" {
		var newest *Snapshot
		for _, m := range idx {
			if m.DeviceID == device && m.SessionID == session && (newest == nil || m.StoredAt.After(newest.StoredAt)) {
				m := m
				newest = &m
			}
		}
		if newest == nil {
			return Record{}, fs.ErrNotExist
		}
		revision = newest.Revision
	}
	raw, err := s.root.ReadFile(path.Join(oh, device, session, revision+".manifest"))
	if err != nil {
		return Record{}, err
	}
	var st stored
	if err = json.Unmarshal(raw, &st); err != nil {
		return Record{}, err
	}
	b, err := s.assemble(oh, Manifest{DeviceID: device, SessionID: session, Project: st.Snapshot.Project, Files: st.Files})
	if err != nil {
		return Record{}, err
	}
	r := Record{Snapshot: st.Snapshot, Bundle: b}
	if err = r.Validate(); err != nil {
		return Record{}, err
	}
	return r, nil
}

// migrate converts the original whole-bundle layout (<rev>.json beside
// <rev>.meta.json) into chunks and manifests, keeping each revision's time.
func (s *Store) migrate() error {
	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !Digest(e.Name()) {
			continue
		}
		oh := e.Name()
		var legacy []string
		err := fs.WalkDir(s.root.FS(), oh, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && path.Base(p) == "chunks" {
				return fs.SkipDir
			}
			if d.Type().IsRegular() && strings.HasSuffix(p, ".meta.json") {
				legacy = append(legacy, strings.TrimSuffix(p, ".meta.json"))
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(legacy) == 0 {
			continue
		}
		for _, base := range legacy {
			var meta Snapshot
			var b Bundle
			raw, err := s.root.ReadFile(base + ".meta.json")
			if err == nil {
				err = json.Unmarshal(raw, &meta)
			}
			if err == nil {
				raw, err = s.root.ReadFile(base + ".json")
			}
			if err == nil {
				err = json.Unmarshal(raw, &b)
			}
			if err == nil {
				err = Record{Snapshot: meta, Bundle: b}.Validate()
			}
			if err != nil {
				return fmt.Errorf("%s: %w", base, err)
			}
			m, chunks := b.Split()
			for id, c := range chunks {
				if err := s.putChunkOwner(oh, id, c); err != nil {
					return err
				}
			}
			stamp := meta.StoredAt
			s.now = func() time.Time { return stamp }
			_, err = s.commit(oh, m)
			s.now = time.Now
			if err != nil {
				return err
			}
			_ = s.root.Remove(base + ".json")
			_ = s.root.Remove(base + ".meta.json")
		}
	}
	return nil
}

func (s *Store) putChunkOwner(oh, id string, data []byte) error {
	p := chunkPath(oh, id)
	if _, err := s.root.Stat(p); err == nil {
		now := s.now()
		return s.root.Chtimes(p, now, now) // keep it past the next sweep
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return s.write(p, buf.Bytes())
}
