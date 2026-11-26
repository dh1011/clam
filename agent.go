package main

// The agent: a tool-calling loop over the Responses API, the tools it may
// use, and the per-terminal conversation history.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultModel = "gpt-6-luna"
	maxResult    = 20000
	maxWrite     = 100000
	maxTurns     = 50
)

var ignored = map[string]bool{".git": true, "node_modules": true, ".venv": true}

type toolArgs struct {
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Command string `json:"command,omitempty"`
	Text    string `json:"text,omitempty"`
}

// agentEvents is how the agent reports to the terminal.
type agentEvents interface {
	text(delta string)
	tool(name string, args toolArgs)
	// thinking is called while waiting on the model.
	thinking()
	// approve asks the user to allow a tool call.
	approve(name string, args toolArgs) bool
}

type agent struct {
	root   string
	events agentEvents
}

func isInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// checkedPath resolves name under root, refusing anything that is, or
// resolves through a symlink to, a path outside it.
func checkedPath(root, name string, writing bool) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("Use a path relative to the current directory")
	}
	target := filepath.Join(root, name)
	if !isInside(root, target) {
		return "", errors.New("Path is outside the current directory tree")
	}
	resolve := target
	if writing {
		resolve = filepath.Dir(target)
	}
	parent, err := filepath.EvalSymlinks(resolve)
	if err != nil {
		return "", err
	}
	if !isInside(root, parent) {
		return "", errors.New("Path resolves outside the current directory tree")
	}
	if writing {
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			return target, nil
		} else if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", errors.New("Writing through a symlink is not allowed")
		}
		real, err := filepath.EvalSymlinks(target)
		if err != nil {
			return "", err
		}
		if !isInside(root, real) {
			return "", errors.New("Path resolves outside the current directory tree")
		}
	}
	return target, nil
}

// truncate cuts text to at most n characters.
func truncate(text string, n int) string {
	if len(text) <= n {
		return text
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}

func walk(root, relative string, files []string, limit int) []string {
	entries, err := os.ReadDir(filepath.Join(root, relative))
	if err != nil {
		return files
	}
	for _, entry := range entries {
		if len(files) >= limit {
			break
		}
		if ignored[entry.Name()] || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		child := filepath.Join(relative, entry.Name())
		if entry.IsDir() {
			files = walk(root, child, files, limit)
		} else if entry.Type().IsRegular() {
			files = append(files, child)
		}
	}
	return files
}

func safeInspection(command string) bool {
	return slices.Contains([]string{"pwd", "ls", "ls -la", "git status --short", "git diff --no-ext-diff"}, strings.TrimSpace(command))
}

// policy decides whether a tool call may run: it returns a reason when the
// call is blocked, and an error when its arguments are invalid.
func (a *agent) policy(name string, args toolArgs) (string, error) {
	switch name {
	case "write_file":
		if _, err := checkedPath(a.root, args.Path, true); err != nil {
			return "", err
		}
		if len([]rune(args.Content)) > maxWrite {
			return "File too large", nil
		}
	case "run_command":
		if safeInspection(args.Command) {
			return "", nil
		}
	default:
		return "", nil
	}
	if !a.events.approve(name, args) {
		return "User denied this action", nil
	}
	return "", nil
}

func isDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("Not a directory")
	}
	return nil
}

// cappedBuffer collects command output and calls full once it holds more
// than maxResult bytes. (The buffer is a named field: embedded, its
// ReadFrom would let io.Copy bypass Write.)
type cappedBuffer struct {
	buf      bytes.Buffer
	full     func()
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.buf.Write(p)
	if b.buf.Len() > maxResult && !b.overflow {
		b.overflow = true
		b.full()
	}
	return len(p), nil
}

func runCommand(ctx context.Context, command, dir string) (string, error) {
	if len(command) > 4000 {
		return "", errors.New("Command is too long")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", command)
	} else {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = exec.CommandContext(ctx, shell, "-c", command)
	}
	cmd.Dir = dir
	output := &cappedBuffer{full: cancel}
	cmd.Stdout, cmd.Stderr = output, output
	// Don't wait forever on a background process that kept the pipes open.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	switch {
	case output.overflow:
		return "", errors.New("Command output is too large")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", errors.New("Command timed out")
	case ctx.Err() != nil:
		return "", ctx.Err()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", err
	}
	return fmt.Sprintf("Exit %d\n%s", cmd.ProcessState.ExitCode(), truncate(output.buf.String(), maxResult)), nil
}

func (a *agent) execute(ctx context.Context, name string, args toolArgs) (string, error) {
	switch name {
	case "read_file":
		target, err := checkedPath(a.root, args.Path, false)
		if err != nil {
			return "", err
		}
		if info, err := os.Stat(target); err != nil {
			return "", err
		} else if info.Size() > 1_000_000 {
			return "", errors.New("File is too large")
		}
		data, err := os.ReadFile(target)
		return string(data), err
	case "list_files", "search_files":
		dir := args.Path
		if dir == "" {
			dir = "."
		}
		checked, err := checkedPath(a.root, dir, false)
		if err != nil {
			return "", err
		}
		if err := isDir(checked); err != nil {
			return "", err
		}
		files := walk(checked, "", nil, 1000)
		if name == "list_files" {
			for i, file := range files {
				files[i] = filepath.Join(dir, file)
			}
			return strings.Join(files, "\n"), nil
		}
		return search(checked, dir, files, args.Text)
	case "write_file":
		if len([]rune(args.Content)) > maxWrite {
			return "", errors.New("File content is too large")
		}
		target, err := checkedPath(a.root, args.Path, true)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(target, []byte(args.Content), 0o600); err != nil {
			return "", err
		}
		return "Wrote " + args.Path, nil
	case "run_command":
		return runCommand(ctx, args.Command, a.root)
	}
	return "", fmt.Errorf("Unknown tool %s", name)
}

func search(checked, dir string, files []string, text string) (string, error) {
	if text == "" {
		return "", errors.New("Search text is empty")
	}
	var matches []string
	for _, file := range files {
		full := filepath.Join(checked, file)
		if info, err := os.Stat(full); err != nil || info.Size() > 1_000_000 {
			continue
		}
		content, err := os.ReadFile(full)
		if err != nil || bytes.IndexByte(content, 0) >= 0 {
			continue
		}
		for i, line := range strings.Split(string(content), "\n") {
			if len(matches) < 100 && strings.Contains(line, text) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", filepath.Join(dir, file), i+1, truncate(line, 250)))
			}
		}
	}
	if len(matches) == 0 {
		return "No matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

func object(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

var stringType = map[string]any{"type": "string"}

var tools = []toolSpec{
	{"function", "read_file", "Read a UTF-8 file under the current directory.",
		object(map[string]any{"path": stringType}, "path")},
	{"function", "list_files", "List files under the current directory, excluding .git, node_modules and .venv.",
		object(map[string]any{"path": stringType})},
	{"function", "search_files", "Find literal text in UTF-8 files under the current directory.",
		object(map[string]any{"text": map[string]any{"type": "string", "minLength": 1}, "path": stringType}, "text")},
	{"function", "write_file", "Replace or create a UTF-8 file. Requires user approval.",
		object(map[string]any{"path": stringType, "content": stringType}, "path", "content")},
	{"function", "run_command", "Run a command in the current directory. Commands that may change state require user approval.",
		object(map[string]any{"command": stringType}, "command")},
}

// callTool runs one tool call and returns what the model sees as its
// result.
func (a *agent) callTool(ctx context.Context, name string, args toolArgs) string {
	a.events.tool(name, args)
	reason, err := a.policy(name, args)
	if err == nil && reason == "" {
		var out string
		if out, err = a.execute(ctx, name, args); err == nil {
			return truncate(out, maxResult)
		}
	}
	if err != nil {
		return "Error: " + err.Error()
	}
	return reason
}

func jsonItem(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// run answers prompt, continuing the conversation in history, and returns
// the updated history.
func (a *agent) run(ctx context.Context, prompt, osName, shell, sessionID string, history []json.RawMessage) ([]json.RawMessage, error) {
	model := os.Getenv("CLAM_MODEL")
	if model == "" {
		model = defaultModel
	}
	req := responseRequest{
		Model: model,
		Instructions: fmt.Sprintf("The user typed a natural-language request at their terminal. Answer briefly in markdown with no $ prompts in shell code blocks. You may use local tools to inspect or act. The operating system is %s, shell is %s, and current directory is %s. File tools are confined to this directory tree. Never claim an action succeeded before a tool confirms it. Ask for a tool action when needed; approval is handled by the app.",
			osName, shell, a.root),
		Tools:          tools,
		Reasoning:      map[string]string{"effort": "low"},
		Include:        []string{"reasoning.encrypted_content"},
		PromptCacheKey: sessionID,
		Stream:         true,
	}
	history = append(history, jsonItem(map[string]string{"role": "user", "content": prompt}))
	for range maxTurns {
		req.Input = history
		a.events.thinking()
		output, err := streamResponse(ctx, req, a.events.text)
		if err != nil {
			return history, err
		}
		history = append(history, output...)
		called := false
		for _, raw := range output {
			var item outputItem
			if json.Unmarshal(raw, &item) != nil || item.Type != "function_call" {
				continue
			}
			called = true
			var args toolArgs
			result := "Error: invalid tool arguments"
			if json.Unmarshal([]byte(item.Arguments), &args) == nil {
				result = a.callTool(ctx, item.Name, args)
			}
			if ctx.Err() != nil {
				return history, ctx.Err()
			}
			history = append(history, jsonItem(map[string]string{"type": "function_call_output", "call_id": item.CallID, "output": result}))
		}
		if !called {
			return history, nil
		}
	}
	return history, errors.New("Stopped after too many tool calls")
}

// --- Sessions ----------------------------------------------------------------

func sessionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "clam", "sessions"), nil
}

func sessionID() string {
	if id := os.Getenv("CLAM_SESSION_ID"); id != "" {
		return id
	}
	return "standalone-" + strconv.Itoa(os.Getppid())
}

// openSessions prepares the session directory, drops sessions idle for a
// day, and returns the file for id.
func openSessions(id string) (string, error) {
	dir, err := sessionDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if info, err := os.Lstat(dir); err != nil {
		return "", err
	} else if info.Mode()&fs.ModeSymlink != 0 {
		return "", errors.New("Session directory cannot be a symlink")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		// Another session may be cleaning it too.
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

// session is one terminal's conversation: the built-in agent's history,
// and the session ids of external agents to resume.
type session struct {
	Input  []json.RawMessage `json:"input,omitempty"`
	Agents map[string]string `json:"agents,omitempty"`
}

func loadSession(file string) (*session, error) {
	s := &session{}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return &session{}, err
	}
	return s, nil
}

func saveSession(file string, s *session) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	temporary := file + "." + rand.Text()
	defer os.Remove(temporary)
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, file)
}
