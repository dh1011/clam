//go:build !windows

package main

// setupConsole has nothing to do outside Windows.
func setupConsole() func() { return func() {} }
