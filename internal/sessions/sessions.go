package sessions

import (
	"bytes"
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
	"regexp"
	"sort"
	"strings"
	"time"
)

const MaxBytes = 64 << 20

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var reserved = regexp.MustCompile(`^(COM|LPT)[1-9]$`)

func ID(s string) bool     { return uuid.MatchString(s) }
func Digest(s string) bool { return digest.MatchString(s) }

type Bundle struct {
	DeviceID  string            `json:"device_id"`
	SessionID string            `json:"session_id"`
	Project   string            `json:"project"`
	Files     map[string][]byte `json:"files"`
}
type Snapshot struct {
	DeviceID  string    `json:"device_id"`
	SessionID string    `json:"session_id"`
	Project   string    `json:"project"`
	Revision  string    `json:"revision"`
	StoredAt  time.Time `json:"stored_at"`
	Bytes     int64     `json:"bytes"`
	FileCount int       `json:"file_count"`
	Title     string    `json:"title,omitempty"`
}
type Record struct {
	Snapshot Snapshot `json:"snapshot"`
	Bundle   Bundle   `json:"bundle"`
}

func portablePath(p string) bool {
	if !fs.ValidPath(p) || p == "." || strings.ContainsAny(p, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || reserved.MatchString(stem) {
			return false
		}
	}
	return true
}
func (b Bundle) Validate() error {
	if !ID(b.DeviceID) || !ID(b.SessionID) || !portablePath(b.Project) || strings.Contains(b.Project, "/") {
		return errors.New("invalid session identity or project")
	}
	main := b.SessionID + ".jsonl"
	if len(b.Files[main]) == 0 || len(b.Files) > 512 {
		return errors.New("missing transcript or too many files")
	}
	total := 0
	for p, data := range b.Files {
		if !portablePath(p) || (p != main && !strings.HasPrefix(p, b.SessionID+"/")) || !strings.HasSuffix(p, ".jsonl") {
			return errors.New("invalid transcript path")
		}
		total += len(data)
		if total > MaxBytes {
			return errors.New("session is too large")
		}
		if len(data) == 0 {
			continue
		}
		if data[len(data)-1] != '\n' {
			return errors.New("incomplete JSONL record")
		}
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			if line[0] != '{' || !json.Valid(line) {
				return errors.New("invalid JSONL object")
			}
		}
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if len(encoded) > MaxBytes {
		return errors.New("encoded session is too large")
	}
	return nil
}

// Title returns the newest AI-generated or summary title in the main transcript.
func (b Bundle) Title() string {
	title, prompt := "", ""
	for _, line := range bytes.Split(b.Files[b.SessionID+".jsonl"], []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"ai-title"`)) && !bytes.Contains(line, []byte(`"summary"`)) && (prompt != "" || !bytes.Contains(line, []byte(`"user"`))) {
			continue
		}
		var rec struct {
			Type    string `json:"type"`
			AITitle string `json:"aiTitle"`
			Summary string `json:"summary"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch {
		case rec.Type == "ai-title" && rec.AITitle != "":
			title = rec.AITitle
		case rec.Type == "summary" && rec.Summary != "" && title == "":
			title = rec.Summary
		case rec.Type == "user" && !rec.IsMeta && prompt == "":
			// The first typed prompt is the fallback; tool results and injected tags are not.
			var text string
			if json.Unmarshal(rec.Message.Content, &text) == nil && !strings.HasPrefix(strings.TrimSpace(text), "<") {
				prompt = strings.Join(strings.Fields(text), " ")
			}
		}
	}
	if title == "" {
		title = prompt
	}
	if r := []rune(strings.TrimSpace(title)); len(r) > 200 {
		return string(r[:200])
	}
	return strings.TrimSpace(title)
}
func (b Bundle) Revision() string {
	encoded, _ := json.Marshal(b)
	h := sha256.Sum256(encoded)
	return hex.EncodeToString(h[:])
}

func (r Record) Validate() error {
	if err := r.Bundle.Validate(); err != nil {
		return err
	}
	m, b := r.Snapshot, r.Bundle
	if m.DeviceID != b.DeviceID || m.SessionID != b.SessionID || m.Project != b.Project || m.Revision != b.Revision() || m.FileCount != len(b.Files) {
		return errors.New("snapshot integrity check failed")
	}
	var size int64
	for _, data := range b.Files {
		size += int64(len(data))
	}
	if m.Bytes != size {
		return errors.New("snapshot size check failed")
	}
	return nil
}
func readLimited(r *os.Root, p string) ([]byte, error) {
	f, err := r.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if len(data) > MaxBytes {
		return nil, errors.New("transcript is too large")
	}
	return data, err
}
func readComplete(r *os.Root, p string) ([]byte, error) {
	data, err := readLimited(r, p)
	if err != nil {
		return nil, err
	}
	end := bytes.LastIndexByte(data, '\n')
	return data[:end+1], nil
}

// ScanOptions drives Scan. Skip, when set, is asked with a cheap signature of
// a session's files (names, sizes, modification times) before they are read;
// Visit's error aborts the scan; a session that cannot be read or validated is
// reported to Invalid and skipped, so one bad session does not stop the rest.
type ScanOptions struct {
	Skip    func(sessionID, signature string) bool
	Visit   func(b Bundle, signature string) error
	Invalid func(sessionID string, err error)
}

// Scan reads only native session JSONL files, never credentials or settings.
func Scan(projects, device string, opt ScanOptions) error {
	if !ID(device) {
		return errors.New("invalid device UUID")
	}
	r, err := os.OpenRoot(projects)
	if err != nil {
		return err
	}
	defer r.Close()
	return fs.WalkDir(r.FS(), ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		depth := strings.Count(p, "/")
		if e.IsDir() {
			if depth >= 1 {
				return fs.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() || depth != 1 || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		id := strings.TrimSuffix(path.Base(p), ".jsonl")
		if !ID(id) {
			return nil
		}
		project := path.Dir(p)
		files := []string{p}
		_ = fs.WalkDir(r.FS(), path.Join(project, id), func(q string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && entry.Type().IsRegular() && strings.HasSuffix(q, ".jsonl") {
				files = append(files, q)
			}
			return nil
		})
		var sig strings.Builder
		for _, f := range files {
			if info, err := r.Stat(f); err == nil {
				fmt.Fprintf(&sig, "%s:%d:%d;", f, info.Size(), info.ModTime().UnixNano())
			}
		}
		if opt.Skip != nil && opt.Skip(id, sig.String()) {
			return nil
		}
		b, err := readSession(r, device, project, id, files)
		if err == nil && b == nil {
			return nil // nothing complete written yet
		}
		if err == nil {
			err = b.Validate()
		}
		if err != nil {
			if opt.Invalid != nil {
				opt.Invalid(id, err)
			}
			return nil
		}
		return opt.Visit(*b, sig.String())
	})
}

func readSession(r *os.Root, device, project, id string, files []string) (*Bundle, error) {
	b := Bundle{DeviceID: device, SessionID: id, Project: project, Files: map[string][]byte{}}
	if len(files) > 512 {
		return nil, errors.New("too many transcript files")
	}
	total := 0
	for i, f := range files {
		data, err := readComplete(r, f)
		if err != nil {
			return nil, err
		}
		if i == 0 && len(data) == 0 {
			return nil, nil
		}
		total += len(data)
		if total > MaxBytes {
			return nil, errors.New("session is too large")
		}
		b.Files[strings.TrimPrefix(f, project+"/")] = data
	}
	return &b, nil
}

// Restore never overwrites local files; main transcript is published last.
func Restore(projects string, rec Record) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(projects, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(projects)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("projects directory is a symlink")
	}
	r, err := os.OpenRoot(projects)
	if err != nil {
		return err
	}
	defer r.Close()
	pending := []string{}
	for p, data := range rec.Bundle.Files {
		target := path.Join(rec.Bundle.Project, p)
		parts := strings.Split(target, "/")
		for i := range parts {
			info, err := r.Lstat(strings.Join(parts[:i+1], "/"))
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("restore path contains a symlink")
			}
			if i < len(parts)-1 && !info.IsDir() {
				return errors.New("restore parent is not a directory")
			}
		}
		info, err := r.Lstat(target)
		if err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("restore target is not a regular file")
			}
			old, err := readLimited(r, target)
			if err != nil {
				return err
			}
			if !bytes.Equal(old, data) {
				return errors.New("restore would overwrite an existing transcript")
			}
		} else if errors.Is(err, fs.ErrNotExist) {
			pending = append(pending, p)
		} else {
			return err
		}
	}
	main := rec.Bundle.SessionID + ".jsonl"
	sort.Slice(pending, func(i, j int) bool {
		if pending[i] == main {
			return false
		}
		if pending[j] == main {
			return true
		}
		return pending[i] < pending[j]
	})
	stage, err := os.MkdirTemp(projects, ".nebula-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	stageName := filepath.Base(stage)
	created := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, p := range created {
				_ = r.Remove(p)
			}
		}
	}()
	for i, p := range pending {
		temp := path.Join(stageName, fmt.Sprint(i))
		f, err := r.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(rec.Bundle.Files[p])
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		target := path.Join(rec.Bundle.Project, p)
		if err = r.MkdirAll(path.Dir(target), 0700); err != nil {
			return err
		}
		// A same-filesystem hard link atomically refuses an existing destination.
		if err = r.Link(temp, target); err != nil {
			return err
		}
		created = append(created, target)
	}
	committed = true
	return nil
}
