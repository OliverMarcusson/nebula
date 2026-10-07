//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// execClaude replaces this process with Claude, so callers such as T3 Code
// see Claude's own pid, signals, and exit status.
func execClaude(claude string, args, env []string) int {
	err := syscall.Exec(claude, append([]string{claude}, args...), env)
	fmt.Fprintln(os.Stderr, "nebula:", err)
	return 126
}

// restartSelf replaces the companion with its updated executable, keeping
// its pid for systemd.
func restartSelf(self string) {
	err := syscall.Exec(self, os.Args, os.Environ())
	fmt.Fprintln(os.Stderr, "nebula: restart failed:", err)
}

// startDetached starts the companion in a session of its own, so it outlives
// the Claude Code process that ran it.
func startDetached(args []string, env []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// endProcess asks a process to exit the way a terminal close would.
func endProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// headlessClaude reports whether pid is a Claude Code process driven over
// stream-json (T3 Code, the Agent SDK) rather than a person at a terminal.
// Without /proc there is nothing to check.
func headlessClaude(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return runtime.GOOS != "linux"
	}
	cmdline := strings.ReplaceAll(string(raw), "\x00", " ")
	return strings.Contains(cmdline, "claude") && strings.Contains(cmdline, "stream-json")
}

// parentOf returns a process's parent pid, or 0 when it cannot be read.
func parentOf(pid int) int {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// The command name is parenthesised and may contain spaces.
	fields := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
