package main

import (
	_ "embed"
	"regexp"
)

// The shell integrations `clam init` prints, with '@CLAM@' standing for the
// quoted path of this executable.
var (
	//go:embed shell/clam.bash
	bashScript string
	//go:embed shell/clam.zsh
	zshScript string
	//go:embed shell/clam.ps1
	pwshScript string
)

var vimRedirectRE = regexp.MustCompile(`(?s)^\((.*)\)(\s*>.*)$`)
