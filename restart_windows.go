//go:build windows

package main

import (
	"os"
	"os/exec"
)

// relaunch starts the new executable as a separate process that inherits the
// console, then the caller exits. Windows has no exec, and the console stays
// open as long as any process is attached to it, so a copy started by a double
// click keeps its window.
func relaunch(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Start()
}
