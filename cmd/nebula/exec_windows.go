//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
