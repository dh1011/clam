package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// feed runs an agent's handler over lines shaped like its real output.
func feed(t *testing.T, name string, lines ...string) (*externalRun, *recorder) {
	t.Helper()
	i := slices.IndexFunc(backends, func(b backend) bool { return b.name == name })
	r := &recorder{}
	run := &externalRun{events: r}
	for _, line := range lines {
		backends[i].handle([]byte(line), run)
	}
	return run, r
}

func TestCodexEvents(t *testing.T) {
	run, r := feed(t, "codex",
		`{"type":"thread.started","thread_id":"t-1"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"I'll read hello.txt."}}`,
		`{"type":"item.started","item":{"type":"command_execution","command":"/bin/bash -lc 'cat hello.txt'","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"The word is pelican."}}`,
		`{"type":"turn.completed"}`)
	// After a tool call the answer resumes in a fresh output, so no
	// paragraph break is added.
	if run.session != "t-1" || r.out.String() != "I'll read hello.txt.The word is pelican." || strings.Join(r.tools, ",") != "command" {
		t.Errorf("session %q, text %q, tools %v", run.session, r.out.String(), r.tools)
	}
	run, _ = feed(t, "codex", `{"type":"turn.failed","error":{"message":"usage limit reached"}}`)
	if run.err != "usage limit reached" {
		t.Errorf("err %q", run.err)
	}
	if got := unwrapShell("/bin/bash -lc 'ls -la'"); got != "ls -la" {
		t.Errorf("unwrapShell: %q", got)
	}
}

func TestClaudeEvents(t *testing.T) {
	run, r := feed(t, "claude",
		`{"type":"system","subtype":"init","session_id":"s-1"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"hello.txt"}}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"The word "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"is pelican."}}}`,
		`{"type":"result","subtype":"success","session_id":"s-1","result":"The word is pelican."}`)
	if run.session != "s-1" || r.out.String() != "The word is pelican." || strings.Join(r.tools, ",") != "Read" || run.err != "" {
		t.Errorf("session %q, text %q, tools %v, err %q", run.session, r.out.String(), r.tools, run.err)
	}
	run, _ = feed(t, "claude", `{"type":"result","subtype":"error_max_turns","is_error":true}`)
	if run.err != "error_max_turns" {
		t.Errorf("err %q", run.err)
	}
}

func TestOpenCodeEvents(t *testing.T) {
	run, r := feed(t, "opencode",
		`{"type":"step_start","sessionID":"ses_1","part":{"type":"step-start"}}`,
		`{"type":"tool_use","sessionID":"ses_1","part":{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"/w/hello.txt"}}}}`,
		`{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"The word is pelican."}}`)
	if run.session != "ses_1" || r.out.String() != "The word is pelican." || strings.Join(r.tools, ",") != "read" {
		t.Errorf("session %q, text %q, tools %v", run.session, r.out.String(), r.tools)
	}
	run, _ = feed(t, "opencode", `{"type":"error","error":{"name":"APIError","data":{"message":"bad key"}}}`)
	if run.err != "bad key" {
		t.Errorf("err %q", run.err)
	}
}

func TestPiEvents(t *testing.T) {
	_, r := feed(t, "pi",
		`{"type":"tool_execution_start","toolName":"read","args":{"path":"hello.txt"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"The word "}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"is pelican."}}`)
	if r.out.String() != "The word is pelican." || strings.Join(r.tools, ",") != "read" {
		t.Errorf("text %q, tools %v", r.out.String(), r.tools)
	}
	run, _ := feed(t, "pi", `{"type":"turn_end","message":{"stopReason":"error","errorMessage":"no API key"}}`)
	if run.err != "no API key" {
		t.Errorf("err %q", run.err)
	}
}

func TestSelectedAgent(t *testing.T) {
	t.Setenv("CLAM_AGENT", "")
	t.Setenv("CLAM_AGENT_CMD", "")
	if got := selectedAgent(); got != "builtin" {
		t.Errorf("default: %q", got)
	}
	t.Setenv("CLAM_AGENT_CMD", "my-agent")
	if got := selectedAgent(); got != "custom" {
		t.Errorf("with CLAM_AGENT_CMD: %q", got)
	}
	t.Setenv("CLAM_AGENT", "codex")
	if got := selectedAgent(); got != "codex" {
		t.Errorf("with CLAM_AGENT: %q", got)
	}
}

func TestCustomAgentAnswersWithItsOutput(t *testing.T) {
	dir := tempRoot(t)
	t.Setenv("CLAM_AGENT_CMD", `printf '%s in %s' "$CLAM_SESSION_ID"`)
	t.Setenv("CLAM_SESSION_ID", "term-1")
	r := &recorder{}
	if _, err := runExternal(context.Background(), "custom", filepath.Base(dir), dir, "", r); err != nil {
		t.Fatal(err)
	}
	if want := "term-1 in " + filepath.Base(dir); r.out.String() != want {
		t.Errorf("got %q, want %q", r.out.String(), want)
	}
}

func TestMissingAgentIsReported(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := runExternal(context.Background(), "codex", "hi", os.TempDir(), "", &recorder{})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("got %v", err)
	}
}
