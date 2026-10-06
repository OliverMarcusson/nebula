//go:build windows

package main

import "syscall"

// isTerminal reports whether the handle is a console.
func isTerminal(fd uintptr) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(fd), &mode) == nil
}
