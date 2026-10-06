package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivermarcusson/nebula/internal/accounts"
)

// profile is one native Claude Code profile: a CLAUDE_CONFIG_DIR and the
// global config file holding its signed-in account.
type profile struct {
	Name, Dir, ConfigFile string
}

// managedDir holds the profiles `nebula login` creates, one per account.
func managedDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Nebula", "profiles"), nil
}

func profileName(s string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.TrimPrefix(s, "."))
	if len(name) > 48 {
		name = name[:48]
	}
	if name == "" {
		name = "profile"
	}
	return name
}

// claudeProfiles lists the home profile, the directories in NEBULA_PROFILES,
// and every Nebula-managed profile.
func claudeProfiles(list string) ([]profile, error) {
	home, _ := os.UserHomeDir()
	defaultDir := filepath.Join(home, ".claude")
	homeDir, _ := homeProfile()
	dirs := append([]string{homeDir}, filepath.SplitList(list)...)
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			return nil, errors.New("Claude profile directories must be absolute paths")
		}
	}
	if managed, err := managedDir(); err == nil {
		entries, err := os.ReadDir(managed)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			// Dot-prefixed directories are sign-ins still in progress.
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirs = append(dirs, filepath.Join(managed, e.Name()))
			}
		}
	}
	out := []profile{}
	seenDir, seenName := map[string]bool{}, map[string]bool{}
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		p := profile{Name: profileName(filepath.Base(dir)), Dir: dir, ConfigFile: filepath.Join(dir, ".claude.json")}
		if dir == defaultDir {
			// Claude Code keeps the default profile's config beside, not inside, ~/.claude.
			p.Name, p.ConfigFile = "default", filepath.Join(home, ".claude.json")
		}
		base := p.Name
		for i := 2; seenName[p.Name]; i++ {
			p.Name = fmt.Sprintf("%s-%d", base, i)
		}
		seenName[p.Name] = true
		out = append(out, p)
	}
	return out, nil
}

// projectDirs lists every profile's transcript directory that exists, plus
// the explicitly configured one.
func projectDirs(projects, list string) ([]string, error) {
	profiles, err := claudeProfiles(list)
	if err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(dir string) {
		real, err := filepath.EvalSymlinks(dir) // shared profiles link one directory
		if err != nil {
			return
		}
		if info, err := os.Stat(real); err == nil && info.IsDir() && !seen[real] {
			seen[real] = true
			out = append(out, dir)
		}
	}
	add(projects)
	for _, p := range profiles {
		add(filepath.Join(p.Dir, "projects"))
	}
	return out, nil
}

// localAccounts reads each profile's signed-in identity and cached usage.
func localAccounts(list string) (accounts.Report, error) {
	rep := accounts.Report{Profiles: []accounts.Profile{}}
	profiles, err := claudeProfiles(list)
	if err != nil {
		return rep, err
	}
	for _, pr := range profiles {
		p, err := accounts.ReadLocal(pr.Name, pr.ConfigFile)
		if err != nil {
			return rep, err
		}
		if p == nil {
			continue
		}
		// Prefer a live reading over Claude Code's cache when it is newer.
		if u := liveUsage(context.Background(), pr); u != nil && (p.Usage == nil || u.ObservedAt.After(p.Usage.ObservedAt)) {
			p.Usage = u
		}
		rep.Profiles = append(rep.Profiles, *p)
	}
	return rep, nil
}

func sendReport(ctx context.Context, c *client, device string, rep accounts.Report) error {
	var list []accounts.Account
	return c.call(ctx, "PUT", "/v1/devices/"+device+"/accounts", rep, &list)
}

func reportAccounts(args []string) error {
	f := flags("accounts")
	profiles := f.String("profiles", os.Getenv("NEBULA_PROFILES"), "Claude Code config directories, separated by the OS path list separator")
	if err := parse(f, args); err != nil {
		return err
	}
	device, err := deviceID()
	if err != nil {
		return err
	}
	rep, err := localAccounts(*profiles)
	if err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	if err = sendReport(context.Background(), c, device, rep); err != nil {
		return err
	}
	for _, p := range rep.Profiles {
		fmt.Printf("%s: signed in as %s\n", p.Profile, p.Email)
	}
	if len(rep.Profiles) == 0 {
		fmt.Println("No signed-in Claude Code profiles found.")
	}
	return nil
}
