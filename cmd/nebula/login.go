package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
	"github.com/olivermarcusson/nebula/internal/logins"
)

// Sign-in runs Claude Code's own `claude auth login` into a fresh profile, so
// accounts connect exactly as with /login and credentials stay in that
// profile on this device. Nebula only learns the resulting identity.

const signInTimeout = 10 * time.Minute

var (
	ansi     = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	visitURL = regexp.MustCompile(`visit:\s*(https://\S+)`)
)

// newPendingProfile creates a hidden profile directory for a sign-in in progress.
func newPendingProfile() (string, error) {
	managed, err := managedDir()
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(managed, 0700); err != nil {
		return "", err
	}
	b := make([]byte, 6)
	if _, err = rand.Read(b); err != nil {
		return "", err
	}
	dir := filepath.Join(managed, ".pending-"+hex.EncodeToString(b))
	return dir, os.Mkdir(dir, 0700)
}

func claudeLogin(ctx context.Context, claude, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, claude, "auth", "login", "--claudeai")
	env := []string{}
	for _, kv := range os.Environ() {
		// Inherited credentials must not stand in for the new account's sign-in.
		if !strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") && !strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "CLAUDE_CONFIG_DIR="+dir)
	return cmd
}

// finalize names a completed sign-in's profile after its account. Signing in
// to an account another profile already holds keeps the existing profile.
func finalize(pending, list string) (*accounts.Profile, bool, error) {
	signed, err := accounts.ReadLocal("new", filepath.Join(pending, ".claude.json"))
	if err != nil {
		return nil, false, err
	}
	if signed == nil {
		return nil, false, errors.New("Claude did not record a signed-in account")
	}
	existing, err := claudeProfiles(list)
	if err != nil {
		return nil, false, err
	}
	for _, p := range existing {
		if q, err := accounts.ReadLocal(p.Name, p.ConfigFile); err == nil && q != nil && q.AccountUUID == signed.AccountUUID {
			signed.Profile = p.Name
			return signed, true, os.RemoveAll(pending)
		}
	}
	base := signed.AccountUUID[:8]
	if at := strings.IndexByte(signed.Email, '@'); at > 0 {
		base = signed.Email[:at]
	}
	base = profileName(base)
	managed := filepath.Dir(pending)
	name := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(managed, name)); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	dir := filepath.Join(managed, name)
	if err = os.Rename(pending, dir); err != nil {
		return nil, false, err
	}
	if err = linkShared(dir); err != nil {
		log.Print("Profile created without shared settings: ", err)
	}
	if err = seedConfig(filepath.Join(dir, ".claude.json")); err != nil {
		log.Print("Profile created without onboarding state: ", err)
	}
	signed.Profile = name
	return signed, false, nil
}

// connectSignedIn reports this device's profiles and connects the new account.
func connectSignedIn(ctx context.Context, c *client, device, list string, accountID string) error {
	rep, err := localAccounts(list)
	if err != nil {
		return err
	}
	if err = sendReport(ctx, c, device, rep); err != nil {
		return err
	}
	var out []accounts.Account
	return c.call(ctx, "POST", "/v1/accounts/"+accountID+"/connect", nil, &out)
}

// login is the terminal flow: Claude's own sign-in, then connect.
func login(args []string) error {
	f := flags("login")
	profiles := f.String("profiles", os.Getenv("NEBULA_PROFILES"), "other Claude Code config directories, to detect accounts already signed in")
	if err := parse(f, args); err != nil {
		return err
	}
	claude, err := realClaude()
	if err != nil {
		return err
	}
	device, err := deviceID()
	if err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pending, err := newPendingProfile()
	if err != nil {
		return err
	}
	defer os.RemoveAll(pending)
	ctx, cancel := context.WithTimeout(ctx, signInTimeout)
	defer cancel()
	cmd := claudeLogin(ctx, claude, pending)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		return errors.New("Claude sign-in did not complete")
	}
	p, already, err := finalize(pending, *profiles)
	if err != nil {
		return err
	}
	if err = connectSignedIn(ctx, c, device, *profiles, p.AccountUUID); err != nil {
		return err
	}
	if already {
		fmt.Printf("%s was already signed in (profile %s); it is connected.\n", p.Email, p.Profile)
	} else {
		fmt.Printf("Connected %s (profile %s).\n", p.Email, p.Profile)
	}
	return nil
}

// loginAgent runs sign-ins started from the dashboard for this device. It
// polls for work, launches claude, relays the sign-in URL out and a pasted
// code into claude's stdin, and reports the result.
type loginAgent struct {
	c                *client
	device, profiles string
	mu               sync.Mutex
	active           map[string]*signIn
}

type signIn struct {
	codes  chan string
	ctx    context.Context
	cancel context.CancelFunc
}

func (a *loginAgent) run(ctx context.Context) {
	a.active = map[string]*signIn{}
	name, _ := os.Hostname()
	// WSL shares the Windows hostname; tell the two companions apart.
	if v, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil && strings.Contains(strings.ToLower(string(v)), "microsoft") {
		name += " (WSL)"
	}
	for {
		var w logins.Work
		if err := a.c.call(ctx, "POST", "/v1/devices/"+a.device+"/poll", map[string]string{"name": name}, &w); err == nil {
			a.handle(ctx, w)
		}
		a.mu.Lock()
		delay := 5 * time.Second
		if len(a.active) > 0 {
			delay = time.Second // a sign-in is waiting on the person; respond quickly
		}
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (a *loginAgent) handle(ctx context.Context, w logins.Work) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range w.Start {
		if a.active[l.ID] != nil || len(a.active) >= 4 {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, signInTimeout)
		s := &signIn{codes: make(chan string, 1), ctx: sctx, cancel: cancel}
		a.active[l.ID] = s
		go a.signIn(l.ID, s)
	}
	for id, code := range w.Codes {
		if s := a.active[id]; s != nil {
			select {
			case s.codes <- code:
			default:
			}
		}
	}
	for _, id := range w.Cancel {
		if s := a.active[id]; s != nil {
			s.cancel()
		}
	}
}

func (a *loginAgent) status(id, state, link, message, account string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out logins.Login
	body := map[string]string{"state": state, "url": link, "message": message, "account_id": account}
	if err := a.c.call(ctx, "PUT", "/v1/devices/"+a.device+"/logins/"+id, body, &out); err != nil {
		log.Print("Sign-in status update failed: ", err)
	}
}

func (a *loginAgent) signIn(id string, s *signIn) {
	defer func() {
		s.cancel()
		a.mu.Lock()
		delete(a.active, id)
		a.mu.Unlock()
	}()
	fail := func(msg string) { a.status(id, logins.Failed, "", msg, "") }
	claude, err := realClaude()
	if err != nil {
		fail(err.Error())
		return
	}
	pending, err := newPendingProfile()
	if err != nil {
		fail("Could not create a Claude profile on this device")
		return
	}
	defer os.RemoveAll(pending)

	cmd := claudeLogin(s.ctx, claude, pending)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail("Could not start Claude sign-in")
		return
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err = cmd.Start(); err != nil {
		fail("Could not start Claude sign-in")
		return
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		pw.Close()
	}()
	// Read claude's output: relay the manual sign-in URL once, keep the tail for errors.
	var tail strings.Builder
	var tailMu sync.Mutex
	read := make(chan struct{})
	go func() {
		defer close(read)
		var acc strings.Builder
		sent := false
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				text := ansi.ReplaceAllString(string(buf[:n]), "")
				if acc.Len() < 64<<10 {
					acc.WriteString(text)
				}
				tailMu.Lock()
				tail.WriteString(text)
				tailMu.Unlock()
				if m := visitURL.FindStringSubmatch(acc.String()); m != nil && !sent {
					sent = true
					a.status(id, logins.Waiting, m[1], "", "")
				}
			}
			if err != nil {
				return
			}
		}
	}()

	var waitErr error
loop:
	for {
		select {
		case code := <-s.codes:
			if _, err := io.WriteString(stdin, code+"\n"); err != nil {
				log.Print("Could not deliver sign-in code: ", err)
			}
		case waitErr = <-done:
			break loop
		}
	}
	<-read
	if s.ctx.Err() != nil {
		if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
			fail("Sign-in timed out")
		}
		return // cancelled from the dashboard; the server already shows it
	}
	if waitErr != nil {
		msg := "Claude sign-in did not complete"
		tailMu.Lock()
		for _, line := range strings.Split(tail.String(), "\n") {
			if i := strings.Index(line, "Login failed"); i >= 0 {
				msg = strings.TrimSpace(line[i:])
			}
		}
		tailMu.Unlock()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		fail(msg)
		return
	}
	p, _, err := finalize(pending, a.profiles)
	if err != nil {
		fail("Signed in, but the profile could not be saved: " + err.Error())
		return
	}
	rep, err := localAccounts(a.profiles)
	if err == nil {
		err = sendReport(context.Background(), a.c, a.device, rep)
	}
	if err != nil {
		fail("Signed in, but reporting the account failed")
		return
	}
	a.status(id, logins.Completed, "", "", p.AccountUUID)
}
