//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// execClaude replaces this process with Claude, so callers such as T3 Code
// see Claude's own pid, signals, and exit status.
func execClaude(claude string, args, env []string) int {
	err := syscall.Exec(claude, append([]string{claude}, args...), env)
	fmt.Fprintln(os.Stderr, "nebula:", err)
	return 126
}
