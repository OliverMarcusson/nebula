package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
)

// The launcher runs the real Claude Code CLI with the best available account:
// connected, enabled, in priority order, skipping accounts at a usage limit.
// Install it as `claude` (or point T3 Code's Claude binary path at it) and
// nothing above it needs to know which account is in use.
//
// In an interactive terminal it also supervises: when the Nebula mod sees the
// account hit its usage limit it records the session in NEBULA_SWITCH_FILE
// and exits Claude Code; the launcher then resumes that session on the next
// account. It never resubmits a prompt; the user continues the conversation.

// realClaude finds the Claude Code CLI, skipping this executable when it is
// installed under the same name.
func realClaude() (string, error) {
	if p := os.Getenv("NEBULA_CLAUDE_PATH"); p != "" {
		if !filepath.IsAbs(p) {
			return "", errors.New("NEBULA_CLAUDE_PATH must be absolute")
		}
		return p, nil
	}
	self, _ := os.Executable()
	selfInfo, _ := os.Stat(self)
	names := []string{"claude"}
	if runtime.GOOS == "windows" {
		names = []string{"claude.exe", "claude.cmd"}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		for _, name := range names {
			p := filepath.Join(dir, name)
			info, err := os.Stat(p)
			if err != nil || info.IsDir() || (selfInfo != nil && os.SameFile(info, selfInfo)) {
				continue
			}
			return p, nil
		}
	}
	return "", errors.New("the Claude Code CLI (claude) was not found on this device")
}

type choice struct {
	profile profile
	account *accounts.Account // nil when no connected account applies
}

func lastChoiceFile() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "Nebula", "last-profile")
}

// pick chooses the profile to run, skipping excluded accounts. Without the
// server it reuses the last choice, then falls back to the home profile.
func pick(ctx context.Context, exclude map[string]bool) (choice, error) {
	profiles, err := claudeProfiles(os.Getenv("NEBULA_PROFILES"))
	if err != nil {
		return choice{}, err
	}
	local := map[string]profile{}
	for _, p := range profiles {
		if q, err := accounts.ReadLocal(p.Name, p.ConfigFile); err == nil && q != nil {
			if _, dup := local[q.AccountUUID]; !dup {
				local[q.AccountUUID] = p
			}
		}
	}
	home := profiles[0]
	var list []accounts.Account
	if c, err := newClient(); err == nil {
		qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = c.call(qctx, "GET", "/v1/accounts", nil, &list)
		cancel()
		if err != nil {
			list = nil
		}
	}
	if list == nil {
		if raw, err := os.ReadFile(lastChoiceFile()); err == nil {
			dir := strings.TrimSpace(string(raw))
			for _, p := range profiles {
				if p.Dir == dir {
					return choice{profile: p}, nil
				}
			}
		}
		return choice{profile: home}, nil
	}
	usable, limited := accounts.Candidates(list, func(id string) bool { _, ok := local[id]; return ok && !exclude[id] }, time.Now())
	for i := range usable {
		a := usable[i]
		p := local[a.ID]
		// Recheck immediately before use with a live reading when one is available.
		if u := liveUsage(ctx, p); u != nil && u.Exhausted(time.Now()) {
			limited = append(limited, a)
			continue
		}
		return choice{profile: p, account: &a}, nil
	}
	if len(limited) > 0 {
		a := limited[0]
		return choice{profile: local[a.ID], account: &a}, errAllLimited
	}
	return choice{profile: home}, nil
}

var errAllLimited = errors.New("every connected account is at its usage limit")

func (c choice) label() string {
	if c.account != nil && c.account.Email != "" {
		return c.account.Email
	}
	return "profile " + c.profile.Name
}

func (c choice) env(switchFile string) []string {
	homeDir, _ := homeProfile()
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") && !strings.HasPrefix(kv, "NEBULA_SWITCH_FILE=") && !strings.HasPrefix(kv, "NEBULA_ACCOUNT_ID=") {
			env = append(env, kv)
		}
	}
	home, _ := os.UserHomeDir()
	if c.profile.Dir != filepath.Join(home, ".claude") || os.Getenv("CLAUDE_CONFIG_DIR") != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+c.profile.Dir)
	}
	env = append(env, "NEBULA_HOME_PROFILE="+homeDir)
	if c.account != nil {
		env = append(env, "NEBULA_ACCOUNT_ID="+c.account.ID)
	}
	if switchFile != "" {
		env = append(env, "NEBULA_SWITCH_FILE="+switchFile)
	}
	return env
}

func interactive(args []string) bool {
	if !isTerminal(os.Stdin.Fd()) || !isTerminal(os.Stdout.Fd()) {
		return false
	}
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if slices.Contains([]string{"-p", "--print", "--output-format", "--input-format"}, name) {
			return false
		}
	}
	return true
}

// resumeArgs keeps the flags that shape a session and resumes it by id.
func resumeArgs(args []string, session string) []string {
	withValue := []string{"--model", "--permission-mode", "--add-dir", "--plugin-dir", "--settings", "--mcp-config", "--append-system-prompt", "--agent", "--fallback-model"}
	bare := []string{"--dangerously-skip-permissions", "--verbose", "--ide", "--strict-mcp-config"}
	out := []string{"--resume", session}
	for i := 0; i < len(args); i++ {
		name, _, hasEq := strings.Cut(args[i], "=")
		switch {
		case slices.Contains(bare, name):
			out = append(out, args[i])
		case slices.Contains(withValue, name) && hasEq:
			out = append(out, args[i])
		case slices.Contains(withValue, name) && i+1 < len(args):
			out = append(out, args[i], args[i+1])
			i++
		}
	}
	return out
}

func launch(args []string) int {
	claude, err := realClaude()
	if err != nil {
		fmt.Fprintln(os.Stderr, "nebula:", err)
		return 127
	}
	ctx := context.Background()
	exclude := map[string]bool{}
	ch, err := pick(ctx, exclude)
	if err != nil && !errors.Is(err, errAllLimited) {
		fmt.Fprintln(os.Stderr, "nebula:", err)
		return 1
	}
	_ = os.MkdirAll(filepath.Dir(lastChoiceFile()), 0700)
	_ = os.WriteFile(lastChoiceFile(), []byte(ch.profile.Dir+"\n"), 0600)
	if managed, _ := managedDir(); filepath.Dir(ch.profile.Dir) == managed {
		_ = linkShared(ch.profile.Dir)
	}
	if !interactive(args) {
		// T3 Code, scripts, and -p runs: hand over to Claude entirely.
		return execClaude(claude, args, ch.env(""))
	}
	if errors.Is(err, errAllLimited) {
		fmt.Fprintf(os.Stderr, "nebula: every connected account is at its usage limit; using %s\n", ch.label())
	} else if ch.account != nil {
		fmt.Fprintf(os.Stderr, "nebula: using %s\n", ch.label())
	}
	switchDir := filepath.Join(filepath.Dir(lastChoiceFile()), "switch")
	if err = os.MkdirAll(switchDir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "nebula:", err)
		return 1
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	switchFile := filepath.Join(switchDir, hex.EncodeToString(b)+".json")
	defer os.Remove(switchFile)

	// The terminal delivers Ctrl-C to Claude directly; the launcher stays up.
	signal.Ignore(os.Interrupt)
	for attempt := 0; ; attempt++ {
		cmd := exec.Command(claude, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = ch.env(switchFile)
		runErr := cmd.Run()
		code := 0
		if runErr != nil {
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) {
				fmt.Fprintln(os.Stderr, "nebula:", runErr)
				return 1
			}
			code = exit.ExitCode()
		}
		raw, err := os.ReadFile(switchFile)
		if err != nil || attempt >= 4 {
			return code
		}
		_ = os.Remove(switchFile)
		var req struct {
			SessionID string     `json:"session_id"`
			ResetsAt  *time.Time `json:"resets_at"`
		}
		if json.Unmarshal(raw, &req) != nil || req.SessionID == "" {
			return code
		}
		if ch.account != nil {
			exclude[ch.account.ID] = true
			reportLimited(ctx, ch.account.ID, req.ResetsAt)
		}
		next, err := pick(ctx, exclude)
		if err != nil || next.account == nil {
			fmt.Fprintf(os.Stderr, "nebula: %s reached its usage limit and no other connected account has room.\n", ch.label())
			return code
		}
		fmt.Fprintf(os.Stderr, "nebula: %s reached its usage limit; resuming this session on %s\n", ch.label(), next.label())
		if managed, _ := managedDir(); filepath.Dir(next.profile.Dir) == managed {
			_ = linkShared(next.profile.Dir)
		}
		ch, args = next, resumeArgs(args, req.SessionID)
	}
}

func reportLimited(ctx context.Context, account string, resetsAt *time.Time) {
	c, err := newClient()
	if err != nil {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out []accounts.Account
	_ = c.call(qctx, "POST", "/v1/accounts/"+account+"/limited", map[string]*time.Time{"resets_at": resetsAt}, &out)
}

// limitedCommand is called by the mod when the current account hits a usage
// limit. The account is the one the launcher chose, or the profile's own.
func limitedCommand(args []string) error {
	f := flags("limited")
	resets := f.String("resets-at", "", "when the exhausted window resets (RFC 3339)")
	if err := parse(f, args); err != nil {
		return err
	}
	var at *time.Time
	if *resets != "" {
		t, err := time.Parse(time.RFC3339, *resets)
		if err != nil {
			return errors.New("--resets-at must be RFC 3339")
		}
		at = &t
	}
	id := os.Getenv("NEBULA_ACCOUNT_ID")
	if id == "" {
		dir, config := homeProfile()
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			dir, config = d, filepath.Join(d, ".claude.json")
		}
		p, err := accounts.ReadLocal(filepath.Base(dir), config)
		if err != nil || p == nil {
			return errors.New("this Claude profile is not signed in")
		}
		id = p.AccountUUID
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var out []accounts.Account
	if err = c.call(context.Background(), "POST", "/v1/accounts/"+id+"/limited", map[string]*time.Time{"resets_at": at}, &out); err != nil {
		return err
	}
	fmt.Println("Recorded the usage limit; new sessions will use another account.")
	return nil
}
