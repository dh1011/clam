// clam answers natural-language requests typed at a shell prompt while
// leaving ordinary commands alone.
//
// Usage:
//
//	clam init bash|zsh|pwsh      print the shell integration to eval
//	clam check [--exists] -- LINE
//	                             exit 0 if LINE reads as English, else 1
//	clam ask -- LINE             answer LINE
//	clam reset                   forget this terminal's conversation
//	clam agents [NAME]           list the agents clam can use, or check one
//	clam shell [-c LINE]         a shell for editors (see plugin/clam.vim)
//	clam words                   print the common-word list
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

var version = "dev"

const usage = `usage: clam init bash|zsh|pwsh
       clam check [--exists] -- LINE
       clam ask -- LINE
       clam reset
       clam agents [NAME]
       clam shell [-c LINE]
       clam words
       clam version`

func afterDashes(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "check":
		// Never hold up the prompt: if anything is slow, give up and let
		// the shell run the line as usual.
		time.AfterFunc(time.Second, func() { os.Exit(1) })
		exists := len(rest) > 0 && rest[0] == "--exists"
		if exists {
			rest = rest[1:]
		}
		if line := strip(strings.Join(afterDashes(rest), " ")); line != "" && classify(line, exists) {
			return 0
		}
		return 1
	case "ask":
		return ask(strip(strings.Join(afterDashes(rest), " ")))
	case "reset":
		return reset()
	case "words":
		words := make([]string, 0, len(functionWords))
		for w := range functionWords {
			words = append(words, w)
		}
		slices.Sort(words)
		fmt.Println(strings.Join(words, "\n"))
		return 0
	case "agents":
		return listAgents(rest)
	case "init":
		return initShell(rest)
	case "shell":
		return editorShell(rest)
	case "version", "--version":
		fmt.Println("clam", version)
		return 0
	case "help", "--help", "-h":
		fmt.Println(usage)
		return 0
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}

// listAgents shows each agent and whether it is installed, marking the one
// in use. With a name it only checks that clam knows that agent.
func listAgents(args []string) int {
	if len(args) > 0 {
		if slices.Contains(agentNames(), args[0]) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "clam: unknown agent %q (want %s)\n", args[0], strings.Join(agentNames(), ", "))
		return 2
	}
	current := selectedAgent()
	for _, name := range agentNames() {
		status := ""
		switch name {
		case "builtin":
			status = "OpenAI " + cmpOr(os.Getenv("CLAM_MODEL"), defaultModel)
		case "custom":
			status = cmpOr(os.Getenv("CLAM_AGENT_CMD"), "set CLAM_AGENT_CMD")
		default:
			if path, err := exec.LookPath(backends[slices.IndexFunc(backends, func(b backend) bool { return b.name == name })].bin); err == nil {
				status = path
			} else {
				status = "not installed"
			}
		}
		mark := " "
		if name == current {
			mark = "*"
		}
		fmt.Printf("%s %-9s %s\n", mark, name, status)
	}
	return 0
}

func reset() int {
	file, err := openSessions(sessionID())
	if err == nil {
		err = os.Remove(file)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "clam:", err)
		return 1
	}
	fmt.Println("clam: conversation reset")
	return 0
}

func systemContext() (osName, shell string) {
	switch runtime.GOOS {
	case "darwin":
		out, _ := exec.Command("sw_vers", "-productVersion").Output()
		osName = fmt.Sprintf("macOS %s (BSD userland, not GNU)", strings.TrimSpace(string(out)))
	case "linux":
		osName = "Linux"
	case "windows":
		osName = "Windows"
	case "freebsd":
		osName = "FreeBSD (BSD userland, not GNU)"
	default:
		osName = runtime.GOOS
	}
	// The PowerShell integration sets CLAM_SHELL; SHELL is unset on Windows.
	shell = os.Getenv("CLAM_SHELL")
	if shell == "" {
		shell = filepath.Base(os.Getenv("SHELL"))
		if shell == "." {
			shell = "sh"
		}
	}
	return osName, shell
}

// terminal shows the agent's progress: answer text, tool calls, and
// approval prompts.
type terminal struct {
	root        string
	useUI       bool
	out         output
	onInterrupt func()
	failed      bool
}

func (t *terminal) open() {
	if t.out != nil {
		return
	}
	if t.useUI {
		if out, err := newUIOutput(t.onInterrupt); err == nil {
			t.out = out
			return
		}
	}
	t.out = newPlainOutput()
}

func (t *terminal) closeOutput() {
	if t.out != nil {
		t.out.close()
		t.out = nil
	}
}

func (t *terminal) text(delta string) {
	t.open()
	t.out.write(delta)
}

func (t *terminal) tool(name string, args toolArgs) {
	t.closeOutput()
	// Agents that report absolute paths get them shown relative to here.
	if rel, err := filepath.Rel(t.root, args.Path); filepath.IsAbs(args.Path) && err == nil && isInside(t.root, args.Path) {
		args.Path = rel
	}
	detail := ""
	if d := cmpOr(args.Command, args.Path); d != "" {
		detail = ": " + strings.ReplaceAll(d, "\n", " ")
	}
	fmt.Fprintf(os.Stderr, "clam: %s%s\n", name, detail)
}

func (t *terminal) approve(name string, args toolArgs) bool {
	// `clam auto on` sets this to skip approval prompts; path and size
	// checks still apply.
	if os.Getenv("CLAM_AUTO_APPROVE") == "1" {
		return true
	}
	t.closeOutput()
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "clam: action denied (no interactive terminal)")
		return false
	}
	fmt.Fprintf(os.Stderr, "\nclam requests %s in %s:\n", name, t.root)
	if name == "write_file" {
		fmt.Fprintf(os.Stderr, "path: %s\ncontent:\n%s\n", args.Path, args.Content)
	} else {
		fmt.Fprintln(os.Stderr, args.Command)
	}
	fmt.Fprint(os.Stderr, "Allow this action? [y/N] ")
	answer := strings.ToLower(strings.TrimSpace(readTerminalLine()))
	return answer == "y" || answer == "yes"
}

// thinking shows the waiting animation until the model's next answer.
func (t *terminal) thinking() { t.open() }

// readTerminalLine reads one line from the terminal a byte at a time, so
// nothing typed after it is swallowed.
func readTerminalLine() string {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n == 0 || err != nil || buf[0] == '\n' {
			return string(line)
		}
		line = append(line, buf[0])
	}
}

func ask(line string) int {
	if selectedAgent() == "builtin" && os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "clam: OPENAI_API_KEY is not set")
		return 1
	}
	defer setupConsole()()
	// The UI only enables escape codes on the Windows console while it
	// runs; final renders are printed after that, so keep them on.
	if restore, err := termenv.EnableVirtualTerminalProcessing(termenv.NewOutput(os.Stdout)); err == nil {
		defer restore() //nolint:errcheck
	}

	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	id := sessionID()
	file, err2 := openSessions(id)
	if err = errors.Join(err, err2); err != nil {
		fmt.Fprintln(os.Stderr, "clam:", err)
		return 1
	}
	sess, err := loadSession(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "clam: ignoring unreadable conversation:", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	t := &terminal{
		root:        cwd,
		useUI:       term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd())),
		onInterrupt: cancel,
	}
	if name := selectedAgent(); name != "builtin" {
		if sess.Agents == nil {
			sess.Agents = map[string]string{}
		}
		t.open()
		sess.Agents[name], err = runExternal(ctx, name, line, cwd, sess.Agents[name], t)
	} else {
		a := &agent{root: cwd, events: t}
		osName, shell := systemContext()
		sess.Input, err = a.run(ctx, line, osName, shell, id, sess.Input)
	}
	t.closeOutput()
	if ctx.Err() != nil {
		return 130
	}
	if saveErr := saveSession(file, sess); saveErr != nil && err == nil {
		err = saveErr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "clam:", err)
		return 1
	}
	return 0
}

// --- Editor shell ------------------------------------------------------------

// Builtins and reserved words of common shells, which count as commands
// like anything on PATH.
var builtins = strings.Fields(`! [[ ]] { } alias autoload bg bind bindkey break builtin case cd command
	compgen complete continue declare dirs disown do done echo elif else enable esac eval exec exit export
	false fc fg fi for function getopts hash help history if in jobs kill let local logout popd print printf
	pushd pwd read readonly return select set setopt shift shopt source suspend test then time times trap
	true type typeset ulimit umask unalias unset unsetopt until wait whence where which while`)

func commandExists(name string) bool {
	if slices.Contains(builtins, name) {
		return true
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// editorShell stands in for the shell in Vim and Neovim, which run `:!LINE`
// as `shell -c LINE`: an English sentence gets an answer like at the
// prompt, and anything else runs in $SHELL untouched.
func editorShell(args []string) int {
	real := os.Getenv("SHELL")
	if self, err := os.Executable(); real == "" || err == nil && sameFile(real, self) {
		real = "/bin/sh"
	}
	// Only `-c COMMAND` is a command line; `:terminal` and the rest go
	// straight through.
	if len(args) != 2 || args[0] != "-c" {
		return execShell(real, args)
	}
	cmd := args[1]

	// Vim wraps commands whose output it reads (:r !, system()) as
	// "(LINE) >FILE 2>&1"; check LINE and keep the redirection.
	line, redir := cmd, ""
	if m := vimRedirectRE.FindStringSubmatch(cmd); m != nil {
		line, redir = m[1], m[2]
	}
	line = strip(line)
	words := split(line)
	if len(words) == 0 {
		return execShell(real, args)
	}
	exists := commandExists(words[0])
	// Known command that isn't a common English word: run it right away, so
	// plugins calling system() stay fast.
	if (!exists || functionWords[strings.ToLower(words[0])]) && classify(line, exists) {
		self, err := os.Executable()
		if err == nil {
			return execShell(real, []string{"-c", shellQuote(self) + " ask -- " + shellQuote(line) + redir})
		}
	}
	return execShell(real, args)
}

func sameFile(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

// --- Shell integration -------------------------------------------------------

func initShell(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: clam init bash|zsh|pwsh")
		return 2
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "clam:", err)
		return 1
	}
	var script string
	switch args[0] {
	case "bash":
		script = strings.ReplaceAll(bashScript, "'@CLAM@'", shellQuote(self))
	case "zsh":
		script = strings.ReplaceAll(zshScript, "'@CLAM@'", shellQuote(self))
	case "pwsh", "powershell":
		script = strings.ReplaceAll(pwshScript, "'@CLAM@'", "'"+strings.ReplaceAll(self, "'", "''")+"'")
	default:
		fmt.Fprintf(os.Stderr, "clam: unknown shell %q (want bash, zsh or pwsh)\n", args[0])
		return 2
	}
	w := bufio.NewWriter(os.Stdout)
	w.WriteString(script)
	w.Flush()
	return 0
}
