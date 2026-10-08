package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/olivermarcusson/nebula/mods"
)

// Devices follow the server: it serves companion binaries built from its own
// source, and each companion replaces itself whenever the server's binary for
// its platform differs from its own. Deploying the server updates every
// device within the hour. The binary's SHA-256 is its ETag, so an up-to-date
// device gets 304 and downloads nothing.

const (
	updateEvery = time.Hour
	maxBinary   = 128 << 20
)

var platformPattern = regexp.MustCompile(`^(linux|windows)-(amd64|arm64)$`)

// downloadsDir is where the server finds companion binaries:
// nebula-<os>-<arch>[.exe]. The Nix package installs them next to its own.
func downloadsDir() string {
	if d := os.Getenv("NEBULA_DOWNLOADS"); d != "" {
		return d
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(self), "..", "share", "nebula", "downloads")
}

// downloads serves companion binaries; files are immutable, so each digest
// is computed once.
type downloads struct {
	dir     string
	mu      sync.Mutex
	digests map[string]string
}

func (d *downloads) serve(w http.ResponseWriter, r *http.Request, platform string) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if d.dir == "" || !platformPattern.MatchString(platform) {
		http.NotFound(w, r)
		return
	}
	name := "nebula-" + platform
	if strings.HasPrefix(platform, "windows-") {
		name += ".exe"
	}
	f, err := os.Open(filepath.Join(d.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	d.mu.Lock()
	digest := d.digests[name]
	d.mu.Unlock()
	if digest == "" {
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			http.Error(w, "binary unavailable", 500)
			return
		}
		digest = hex.EncodeToString(h.Sum(nil))
		d.mu.Lock()
		d.digests[name] = digest
		d.mu.Unlock()
	}
	w.Header().Set("ETag", `"`+digest+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, f)
}

func selfPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(self)
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// selfUpdate installs the server's binary for this platform when it differs
// from this one, and reports whether it did.
func selfUpdate(ctx context.Context, c *client) (bool, error) {
	self, err := selfPath()
	if err != nil {
		return false, err
	}
	have, err := fileDigest(self)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/v1/update/"+runtime.GOOS+"-"+runtime.GOARCH, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("If-None-Match", `"`+have+`"`)
	hc := *c.http
	hc.Timeout = 10 * time.Minute
	res, err := hc.Do(req)
	if err != nil {
		return false, errors.New("Nebula request failed; check the server connection")
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNotModified, http.StatusNotFound:
		return false, nil // up to date, or the server has no build for this platform
	case http.StatusOK:
	default:
		return false, fmt.Errorf("Nebula returned HTTP %d", res.StatusCode)
	}
	want := strings.Trim(res.Header.Get("ETag"), `"`)
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBinary+1))
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(data)
	if len(data) > maxBinary || hex.EncodeToString(sum[:]) != want {
		return false, errors.New("downloaded binary does not match its checksum")
	}
	if err = installBinary(self, data); err != nil {
		return false, err
	}
	// Windows keeps the launcher as a copy next to the companion.
	if runtime.GOOS == "windows" {
		launcher := filepath.Join(filepath.Dir(self), exe("nebula-claude"))
		if _, err := os.Stat(launcher); err == nil && launcher != self {
			if err = installBinary(launcher, data); err != nil {
				log.Print("Launcher not updated: ", err)
			}
		}
	}
	return true, nil
}

// installBinary replaces path with data once the new binary has run. A
// running executable cannot be overwritten on Windows but can be renamed, so
// the old one moves aside first.
func installBinary(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0755); err != nil {
		return err
	}
	cmd := exec.Command(tmp, "version")
	cmd.Dir = filepath.Dir(path)
	if out, err := cmd.Output(); err != nil || !bytes.HasPrefix(out, []byte("nebula ")) {
		os.Remove(tmp)
		return errors.New("downloaded binary does not run on this device")
	}
	if runtime.GOOS == "windows" {
		// Earlier updates leave .old files, free once their processes exit;
		// an open T3 Code thread can hold a launcher for days, so the one
		// moved aside now gets a fresh name when .old is still taken.
		leftovers, _ := filepath.Glob(path + ".old*")
		for _, f := range leftovers {
			os.Remove(f)
		}
		old := path + ".old"
		if _, err := os.Stat(old); err == nil {
			old = fmt.Sprintf("%s.old-%d", path, time.Now().UnixNano())
		}
		if err := os.Rename(path, old); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Rename(old, path)
			return err
		}
		return nil
	}
	return os.Rename(tmp, path)
}

// updater keeps a watching companion current: it updates from the server
// every hour and restarts whenever its executable changes, whether it
// updated itself or someone reinstalled it.
type updater struct {
	c       *client
	self    string
	started os.FileInfo
	checked time.Time
}

func newUpdater(c *client) *updater {
	u := &updater{c: c}
	if self, err := selfPath(); err == nil {
		u.self = self
		u.started, _ = os.Stat(self)
		refreshMod(self)
	}
	return u
}

// refreshMod reinstalls the Claude Code mod when this binary embeds a
// different one than setup installed, so an update brings its mod along.
// Devices set up with --no-mod have no marketplace and are left alone.
func refreshMod(bin string) {
	market := filepath.Join(configDir(), "marketplace")
	if _, err := os.Stat(market); err != nil {
		return
	}
	// ponytail: compares embedded files only; a file the mod dropped lingers until setup.
	stale := fs.WalkDir(mods.Nebula, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, _ := mods.Nebula.ReadFile(p)
		if have, err := os.ReadFile(filepath.Join(market, filepath.FromSlash(p))); err != nil || !bytes.Equal(have, want) {
			return fs.ErrInvalid
		}
		return nil
	})
	if stale == nil {
		return
	}
	if err := installMod(bin); err != nil {
		log.Print("Claude Code mod not updated: ", err)
		return
	}
	log.Print("Updated the Claude Code mod")
}

func (u *updater) tick(ctx context.Context) {
	if u.started == nil {
		return
	}
	// Development builds update only with `nebula update`.
	if version != "dev" && time.Since(u.checked) >= updateEvery {
		u.checked = time.Now()
		if updated, err := selfUpdate(ctx, u.c); err != nil {
			log.Print("Update check failed: ", err)
		} else if updated {
			log.Print("Updated Nebula from the server")
		}
	}
	if now, err := os.Stat(u.self); err == nil && (!os.SameFile(now, u.started) || !now.ModTime().Equal(u.started.ModTime())) {
		log.Print("Nebula was updated; restarting")
		restartSelf(u.self)
	}
}

func updateCommand(args []string) error {
	if err := parse(flags("update"), args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	updated, err := selfUpdate(context.Background(), c)
	if err != nil {
		return err
	}
	if !updated {
		fmt.Println("Nebula is up to date.")
		return nil
	}
	fmt.Println("Updated Nebula; the background companion restarts by itself.")
	return nil
}
