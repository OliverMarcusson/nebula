package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Nebula's profiles differ only in which account is signed in. Everything
// else lives in the home profile and is linked in, so any account can resume
// any session with the same settings, skills, and history. Credentials,
// caches, and per-process state stay per profile.

var sharedDirs = []string{"projects", "file-history", "skills", "plugins", "agents", "commands", "hooks", "output-styles", "todos", "plans"}
var sharedFiles = []string{"settings.json", "CLAUDE.md", "history.jsonl"}

// homeProfile is the profile everything is shared from: CLAUDE_CONFIG_DIR as
// the user runs Claude Code, or ~/.claude.
func homeProfile() (dir, config string) {
	home, _ := os.UserHomeDir()
	if d := os.Getenv("NEBULA_HOME_PROFILE"); d != "" {
		return d, filepath.Join(d, ".claude.json")
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, filepath.Join(d, ".claude.json")
	}
	return filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")
}

// linkShared links the home profile's shared items into dir. Existing items
// in dir are left alone, so it is safe to run repeatedly.
func linkShared(dir string) error {
	home, _ := homeProfile()
	if filepath.Clean(home) == filepath.Clean(dir) {
		return nil
	}
	for _, name := range sharedDirs {
		src := filepath.Join(home, name)
		if err := os.MkdirAll(src, 0700); err != nil {
			return err
		}
		if err := link(src, filepath.Join(dir, name), true); err != nil {
			return fmt.Errorf("sharing %s: %w", name, err)
		}
	}
	for _, name := range sharedFiles {
		src := filepath.Join(home, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := link(src, filepath.Join(dir, name), false); err != nil {
			log.Printf("Not sharing %s with %s: %v", name, filepath.Base(dir), err)
		}
	}
	return nil
}

func link(src, dst string, isDir bool) error {
	if info, err := os.Lstat(dst); err == nil {
		// An empty directory (as a fresh sign-in may create) is replaced.
		entries, _ := os.ReadDir(dst)
		if !isDir || !info.IsDir() || len(entries) > 0 {
			return nil
		}
		if err = os.Remove(dst); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	err := os.Symlink(src, dst)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	// Windows symlinks need Developer Mode; junctions and hard links do not.
	if isDir {
		return exec.Command("cmd", "/c", "mklink", "/J", dst, src).Run()
	}
	return os.Link(src, dst)
}

// seedConfig copies onboarding and per-folder trust from the home profile's
// .claude.json into a new profile's, so a fresh account does not re-prompt.
// Identity, tokens, and caches are never copied.
func seedConfig(file string) error {
	_, homeConfig := homeProfile()
	raw, err := os.ReadFile(homeConfig)
	if err != nil {
		return nil
	}
	var src map[string]json.RawMessage
	if json.Unmarshal(raw, &src) != nil {
		return nil
	}
	raw, err = os.ReadFile(file)
	if err != nil {
		return err
	}
	var dst map[string]json.RawMessage
	if err = json.Unmarshal(raw, &dst); err != nil {
		return err
	}
	for _, k := range []string{"hasCompletedOnboarding", "lastOnboardingVersion", "theme", "autoUpdates", "installMethod"} {
		if v, ok := src[k]; ok {
			if _, set := dst[k]; !set {
				dst[k] = v
			}
		}
	}
	var srcProjects map[string]map[string]json.RawMessage
	if json.Unmarshal(src["projects"], &srcProjects) == nil {
		dstProjects := map[string]map[string]json.RawMessage{}
		_ = json.Unmarshal(dst["projects"], &dstProjects)
		for path, settings := range srcProjects {
			trust := map[string]json.RawMessage{}
			for _, k := range []string{"hasTrustDialogAccepted", "hasCompletedProjectOnboarding"} {
				if v, ok := settings[k]; ok {
					trust[k] = v
				}
			}
			if len(trust) > 0 && dstProjects[path] == nil {
				dstProjects[path] = trust
			}
		}
		if b, err := json.Marshal(dstProjects); err == nil {
			dst["projects"] = b
		}
	}
	out, err := json.MarshalIndent(dst, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".nebula-tmp"
	if err = os.WriteFile(tmp, out, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
