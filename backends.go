package main

// Other agents clam can hand a request to instead of its own: Codex,
// Claude Code, OpenCode, pi, or any command. Each runs in its own
// non-interactive mode under its own permission settings; clam shows its
// answer and tool calls, and keeps one conversation per terminal by
// resuming the agent's session.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// backend runs one external agent.
type backend struct {
	name, bin string
	// args builds the command line; session is the agent's session id
	// from the last request in this terminal, or empty.
	args func(prompt, session string) []string
	// handle reads one line of the agent's JSON output.
	handle func(line []byte, run *externalRun)
}

// externalRun tracks one request to an external agent.
type externalRun struct {
	events  agentEvents
	session string
	err     string
	wrote   bool // some answer text has been shown
}

// paragraph shows text as a new paragraph of the answer.
func (r *externalRun) paragraph(text string) {
	if text == "" {
		return
	}
	if r.wrote {
		text = "\n\n" + text
	}
	r.events.text(text)
	r.wrote = true
}

func (r *externalRun) delta(text string) {
	if text != "" {
		r.events.text(text)
		r.wrote = true
	}
}

func (r *externalRun) tool(name string, args toolArgs) {
	r.events.tool(name, args)
	r.events.thinking()
	r.wrote = false // the answer resumes in a fresh output
}

// newSessionID makes an id for agents that let the caller name sessions.
func newSessionID() string { return "clam-" + strings.ToLower(rand.Text()[:16]) }

var backends = []backend{
	{
		name: "codex", bin: "codex",
		args: func(prompt, session string) []string {
			if session != "" {
				return []string{"exec", "resume", "--json", "--skip-git-repo-check", session, prompt}
			}
			return []string{"exec", "--json", "--skip-git-repo-check", prompt}
		},
		handle: func(line []byte, r *externalRun) {
			var e struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread_id"`
				Message  string `json:"message"`
				Error    struct {
					Message string `json:"message"`
				} `json:"error"`
				Item struct {
					Type    string `json:"type"`
					Text    string `json:"text"`
					Command string `json:"command"`
					Changes []struct {
						Path string `json:"path"`
					} `json:"changes"`
				} `json:"item"`
			}
			if json.Unmarshal(line, &e) != nil {
				return
			}
			switch e.Type {
			case "thread.started":
				r.session = e.ThreadID
			case "item.started":
				if e.Item.Type == "command_execution" {
					r.tool("command", toolArgs{Command: unwrapShell(e.Item.Command)})
				}
			case "item.completed":
				switch e.Item.Type {
				case "agent_message":
					r.paragraph(e.Item.Text)
				case "file_change":
					for _, c := range e.Item.Changes {
						r.tool("edit", toolArgs{Path: c.Path})
					}
				}
			case "turn.failed":
				r.err = e.Error.Message
			case "error":
				r.err = e.Message
			}
		},
	},
	{
		name: "claude", bin: "claude",
		args: func(prompt, session string) []string {
			args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
			if session != "" {
				args = append(args, "--resume", session)
			}
			return append(args, "--", prompt)
		},
		handle: func(line []byte, r *externalRun) {
			var e struct {
				Type      string `json:"type"`
				Subtype   string `json:"subtype"`
				SessionID string `json:"session_id"`
				IsError   bool   `json:"is_error"`
				Result    string `json:"result"`
				Event     struct {
					Type         string `json:"type"`
					ContentBlock struct {
						Type string `json:"type"`
					} `json:"content_block"`
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				} `json:"event"`
				Message struct {
					Content []struct {
						Type  string `json:"type"`
						Name  string `json:"name"`
						Input struct {
							Command  string `json:"command"`
							FilePath string `json:"file_path"`
							Path     string `json:"path"`
						} `json:"input"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &e) != nil {
				return
			}
			if e.SessionID != "" {
				r.session = e.SessionID
			}
			switch e.Type {
			case "stream_event":
				switch {
				case e.Event.Type == "content_block_start" && e.Event.ContentBlock.Type == "text" && r.wrote:
					r.delta("\n\n")
				case e.Event.Type == "content_block_delta" && e.Event.Delta.Type == "text_delta":
					r.delta(e.Event.Delta.Text)
				}
			case "assistant":
				for _, c := range e.Message.Content {
					if c.Type == "tool_use" {
						r.tool(c.Name, toolArgs{Command: c.Input.Command, Path: cmpOr(c.Input.FilePath, c.Input.Path)})
					}
				}
			case "result":
				if e.IsError || e.Subtype != "success" {
					r.err = cmpOr(e.Result, e.Subtype)
				}
			}
		},
	},
	{
		name: "opencode", bin: "opencode",
		args: func(prompt, session string) []string {
			args := []string{"run", "--format", "json"}
			if session != "" {
				args = append(args, "--session", session)
			}
			return append(args, "--", prompt)
		},
		handle: func(line []byte, r *externalRun) {
			var e struct {
				Type      string `json:"type"`
				SessionID string `json:"sessionID"`
				Part      struct {
					Text  string `json:"text"`
					Tool  string `json:"tool"`
					State struct {
						Input struct {
							Command  string `json:"command"`
							FilePath string `json:"filePath"`
						} `json:"input"`
					} `json:"state"`
				} `json:"part"`
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(line, &e) != nil {
				return
			}
			if e.SessionID != "" {
				r.session = e.SessionID
			}
			switch e.Type {
			case "text":
				r.paragraph(e.Part.Text)
			case "tool_use":
				r.tool(e.Part.Tool, toolArgs{Command: e.Part.State.Input.Command, Path: e.Part.State.Input.FilePath})
			case "error":
				r.err = errorMessage(e.Error)
			}
		},
	},
	{
		name: "pi", bin: "pi",
		args: func(prompt, session string) []string {
			return []string{"-p", "--mode", "json", "--session-id", session, "--", prompt}
		},
		handle: func(line []byte, r *externalRun) {
			var e struct {
				Type  string `json:"type"`
				Event struct {
					Type  string `json:"type"`
					Delta string `json:"delta"`
				} `json:"assistantMessageEvent"`
				ToolName string `json:"toolName"`
				Args     struct {
					Command string `json:"command"`
					Path    string `json:"path"`
				} `json:"args"`
				Message struct {
					StopReason   string `json:"stopReason"`
					ErrorMessage string `json:"errorMessage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &e) != nil {
				return
			}
			switch e.Type {
			case "message_update":
				switch e.Event.Type {
				case "text_start":
					if r.wrote {
						r.delta("\n\n")
					}
				case "text_delta":
					r.delta(e.Event.Delta)
				}
			case "tool_execution_start":
				r.tool(e.ToolName, toolArgs{Command: e.Args.Command, Path: e.Args.Path})
			case "turn_end":
				if e.Message.StopReason == "error" {
					r.err = cmpOr(e.Message.ErrorMessage, "model request failed")
				}
			}
		},
	},
}

// Codex reports commands as run through a shell: /bin/bash -lc 'ls -la'.
var shellWrapRE = regexp.MustCompile(`^\S+ -l?c '(.*)'$`)

func unwrapShell(command string) string {
	if m := shellWrapRE.FindStringSubmatch(command); m != nil && !strings.Contains(m[1], "'") {
		return m[1]
	}
	return command
}

// errorMessage digs a message out of an error object of unknown shape.
func errorMessage(raw json.RawMessage) string {
	var e struct {
		Message string `json:"message"`
		Name    string `json:"name"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if m := cmpOr(e.Data.Message, e.Message, e.Name); m != "" {
			return m
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return "agent failed"
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// agentNames lists what CLAM_AGENT accepts.
func agentNames() []string {
	names := []string{"builtin"}
	for _, b := range backends {
		names = append(names, b.name)
	}
	return append(names, "custom")
}

// selectedAgent is the agent named by CLAM_AGENT, or custom when only
// CLAM_AGENT_CMD is set.
func selectedAgent() string {
	if name := strings.TrimSpace(os.Getenv("CLAM_AGENT")); name != "" {
		return name
	}
	if os.Getenv("CLAM_AGENT_CMD") != "" {
		return "custom"
	}
	return "builtin"
}

// tail keeps the last part of an agent's stderr, to show if it fails.
type tail struct{ buf []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

// runExternal answers prompt with the agent named name and returns the
// agent's session id to resume next time.
func runExternal(ctx context.Context, name, prompt, dir, session string, events agentEvents) (string, error) {
	var cmd *exec.Cmd
	run := &externalRun{events: events, session: session}
	var handle func([]byte, *externalRun)
	if name == "custom" {
		cmd = customCommand(ctx, prompt)
		if cmd == nil {
			return session, errors.New("CLAM_AGENT_CMD is not set")
		}
	} else {
		i := slices.IndexFunc(backends, func(b backend) bool { return b.name == name })
		if i < 0 {
			return session, fmt.Errorf("unknown agent %q (want %s)", name, strings.Join(agentNames(), ", "))
		}
		b := backends[i]
		path, err := exec.LookPath(b.bin)
		if err != nil {
			return session, fmt.Errorf("%s is not installed (CLAM_AGENT=%s)", b.bin, name)
		}
		if b.name == "pi" && session == "" {
			session = newSessionID()
			run.session = session
		}
		cmd = exec.CommandContext(ctx, path, b.args(prompt, session)...)
		handle = b.handle
	}
	cmd.Dir = dir
	stderr := &tail{}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return session, err
	}
	if err := cmd.Start(); err != nil {
		return session, err
	}
	reader := bufio.NewReaderSize(stdout, 64*1024)
	if handle == nil {
		// A custom agent's output is the answer itself.
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			run.delta(string(buf[:n]))
			if err != nil {
				break
			}
		}
	} else {
		for {
			line, err := reader.ReadBytes('\n')
			if line = bytes.TrimSpace(line); len(line) > 0 {
				handle(line, run)
			}
			if err != nil {
				break
			}
		}
	}
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return run.session, ctx.Err()
	case run.err != "":
		return run.session, errors.New(run.err)
	case err != nil:
		if msg := strings.TrimSpace(string(stderr.buf)); msg != "" {
			return run.session, fmt.Errorf("%s failed: %s", name, msg)
		}
		return run.session, fmt.Errorf("%s failed: %w", name, err)
	}
	return run.session, nil
}

// customCommand runs CLAM_AGENT_CMD with the request as its last argument.
func customCommand(ctx context.Context, prompt string) *exec.Cmd {
	command := strings.TrimSpace(os.Getenv("CLAM_AGENT_CMD"))
	if command == "" {
		return nil
	}
	if runtime.GOOS == "windows" {
		fields := strings.Fields(command)
		return exec.CommandContext(ctx, fields[0], append(fields[1:], prompt)...)
	}
	// sh rather than $SHELL, whose syntax for arguments may differ (fish).
	return exec.CommandContext(ctx, "/bin/sh", "-c", command+` "$1"`, "clam", prompt)
}
