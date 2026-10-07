//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// execClaude runs Claude with this process's stdio and returns its exit
// code; Windows has no exec, so the launcher waits in between.
func execClaude(claude string, args, env []string) int {
	signal.Ignore(os.Interrupt)
	cmd := exec.Command(claude, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "nebula:", err)
		return 1
	}
	return 0
}

// restartSelf starts the updated companion, hidden like the startup entry
// starts it, and exits.
func restartSelf(self string) {
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Dir, _ = os.UserHomeDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "nebula: restart failed:", err)
		return
	}
	os.Exit(0)
}

// startDetached starts the companion detached and hidden, so it outlives the
// Claude Code process that ran it.
func startDetached(args []string, env []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, args...)
	cmd.Env = env
	// CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | 0x00000200 | 0x00000008}
	return cmd.Start()
}

// endProcess ends a process; Windows has no polite signal for a console one.
// Claude Code's transcript is append-only, so a finished turn is kept.
func endProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// headlessClaude cannot inspect another process's arguments here; the mod
// only asks from a headless session.
func headlessClaude(int) bool { return true }

// parentOf is not needed here: the mod's command is Claude Code's own child.
func parentOf(int) int { return 0 }
