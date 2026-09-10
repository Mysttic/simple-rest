//go:build !windows

package main

import (
	"os"
	"syscall"
)

// relaunch replaces the running process with the new executable in place: the
// same PID, the same terminal and the same foreground process group, so a copy
// started from a shell stays under that shell's control. Go opens every file
// with close-on-exec, so the listening socket is released by the kernel at
// exec time. It only returns when exec itself fails.
func relaunch(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
