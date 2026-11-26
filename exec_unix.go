//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// execShell replaces this process with shell, so the editor talks to the
// real shell directly.
func execShell(shell string, args []string) int {
	path, err := exec.LookPath(shell)
	if err == nil {
		err = syscall.Exec(path, append([]string{shell}, args...), os.Environ())
	}
	fmt.Fprintln(os.Stderr, "clam:", err)
	return 127
}
