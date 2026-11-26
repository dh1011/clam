package main

import "golang.org/x/sys/windows"

const cpUTF8 = 65001

// setupConsole switches the console to UTF-8 output, since glamour and the
// animation print UTF-8 and the Windows default code page mangles it. The
// returned function restores the previous code page.
func setupConsole() func() {
	old, err := windows.GetConsoleOutputCP()
	if err != nil || old == cpUTF8 {
		return func() {}
	}
	if windows.SetConsoleOutputCP(cpUTF8) != nil {
		return func() {}
	}
	return func() { windows.SetConsoleOutputCP(old) } //nolint:errcheck
}
