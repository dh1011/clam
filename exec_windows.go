package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// execShell runs shell with args and passes on its exit code; Windows has
// no exec.
func execShell(shell string, args []string) int {
	cmd := exec.Command(shell, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "clam:", err)
		return 127
	}
	return 0
}
