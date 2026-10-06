//go:build !linux && !windows

package main

import "os"

func isTerminal(fd uintptr) bool {
	info, err := os.NewFile(fd, "").Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && info.Name() != "null"
}
