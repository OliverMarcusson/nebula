package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
)

// `nebula switch` chooses the account by hand, for when accounts differ in
// more than their limits (one may have access the others lack). The choice
// is pinned on this device: the launcher starts new sessions on it while it
// has room, then falls back to the usual order. Given the running session
// under the launcher in a terminal, it also hands that session over, so it
// resumes on the chosen account once Claude Code exits.

func pinFile() string { return filepath.Join(filepath.Dir(lastChoiceFile()), "pinned-account") }

func pinnedAccount() string {
	raw, err := os.ReadFile(pinFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// findAccount matches an account by id, email, or a part of the email that
// only one account has.
func findAccount(list []accounts.Account, name string) (accounts.Account, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	var partial []accounts.Account
	for _, a := range list {
		email := strings.ToLower(a.Email)
		if a.ID == name || email == name {
			return a, nil
		}
		if name != "" && strings.Contains(email, name) {
			partial = append(partial, a)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	if len(partial) > 1 {
		return accounts.Account{}, fmt.Errorf("%q matches several accounts; use the full email", name)
	}
	return accounts.Account{}, fmt.Errorf("no connected account matches %q", name)
}

func switchCommand(args []string) error {
	f := flags("switch")
	session := f.String("session", "", "running session to resume on the account (under the launcher)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return errors.New("usage: nebula switch [--session ID] [ACCOUNT | auto]")
	}
	ctx := context.Background()
	list := serverAccounts(ctx)
	if list == nil {
		return errors.New("could not read accounts from the Nebula server")
	}
	local, _, err := localProfiles()
	if err != nil {
		return err
	}
	current, pinned := os.Getenv("NEBULA_ACCOUNT_ID"), pinnedAccount()
	if f.NArg() == 0 {
		now := time.Now()
		for _, a := range list {
			if a.State != accounts.Connected {
				continue
			}
			notes := []string{}
			if a.ID == current {
				notes = append(notes, "in use by this session")
			}
			if a.ID == pinned {
				notes = append(notes, "pinned")
			}
			if _, ok := local[a.ID]; !ok {
				notes = append(notes, "not signed in on this device")
			}
			if a.Limited(now) {
				notes = append(notes, "at its usage limit")
			}
			if !a.Enabled {
				notes = append(notes, "left out of automatic switching")
			}
			line := a.Email
			if a.OrganizationName != "" {
				line += " (" + a.OrganizationName + ")"
			}
			if len(notes) > 0 {
				line += ": " + strings.Join(notes, ", ")
			}
			fmt.Println(line)
		}
		if pinned == "" {
			fmt.Println("No account is pinned; new sessions use the first connected account with room.")
		}
		return nil
	}
	if strings.EqualFold(f.Arg(0), "auto") {
		if err := os.Remove(pinFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		fmt.Println("Unpinned; new sessions use the first connected account with room.")
		return nil
	}
	connected := []accounts.Account{}
	for _, a := range list {
		if a.State == accounts.Connected {
			connected = append(connected, a)
		}
	}
	a, err := findAccount(connected, f.Arg(0))
	if err != nil {
		return err
	}
	if _, ok := local[a.ID]; !ok {
		return fmt.Errorf("%s is not signed in on this device; connect it here first (nebula login)", a.Email)
	}
	if a.Limited(time.Now()) {
		return fmt.Errorf("%s is at its usage limit", a.Email)
	}
	if err = os.MkdirAll(filepath.Dir(pinFile()), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(pinFile(), []byte(a.ID+"\n"), 0600); err != nil {
		return err
	}
	switchFile := os.Getenv("NEBULA_SWITCH_FILE")
	switch {
	case a.ID == current:
		fmt.Printf("Pinned %s; this session already uses it.\n", a.Email)
	case *session != "" && switchFile != "" && filepath.Dir(switchFile) == filepath.Join(filepath.Dir(lastChoiceFile()), "switch"):
		raw, _ := json.Marshal(map[string]string{"session_id": *session, "account": a.ID})
		if err = os.WriteFile(switchFile, raw, 0600); err != nil {
			return err
		}
		fmt.Printf("Switching to %s: this session resumes on it when the current turn ends.\n", a.Email)
	default:
		fmt.Printf("Pinned %s; new sessions start on it. This session keeps its account.\n", a.Email)
	}
	return nil
}
