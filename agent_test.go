package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempRoot makes a directory the way ask sees the current one: with
// symlinks resolved.
func tempRoot(t *testing.T) string {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// recorder stands in for the terminal.
type recorder struct {
	allow    bool
	out      strings.Builder
	tools    []string
	approved []string
}

func (r *recorder) text(delta string)               { r.out.WriteString(delta) }
func (r *recorder) tool(name string, args toolArgs) { r.tools = append(r.tools, name) }
func (r *recorder) thinking()                       {}
func (r *recorder) approve(name string, args toolArgs) bool {
	r.approved = append(r.approved, name)
	return r.allow
}

func newAgent(root string, allow bool) (*agent, *recorder) {
	r := &recorder{allow: allow}
	return &agent{root: root, events: r}, r
}

func TestFileToolsStayInTheCurrentDirectoryTree(t *testing.T) {
	root, outside := tempRoot(t), tempRoot(t)
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := checkedPath(root, "nested", false); err != nil || got != filepath.Join(root, "nested") {
		t.Errorf("nested: got %q, %v", got, err)
	}
	if _, err := checkedPath(root, "../outside", false); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("../outside: got %v", err)
	}
	if _, err := checkedPath(root, "/etc/passwd", false); err == nil {
		t.Error("absolute path allowed")
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedPath(root, "link", false); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("link: got %v", err)
	}
	if _, err := checkedPath(root, "link/new.txt", true); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("write under link: got %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "new.txt"), filepath.Join(root, "new.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedPath(root, "new.txt", true); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("write through symlink: got %v", err)
	}
}

func TestStateChangingToolsRequireApproval(t *testing.T) {
	root := tempRoot(t)
	a, r := newAgent(root, false)
	if reason, err := a.policy("run_command", toolArgs{Command: "pwd"}); reason != "" || err != nil {
		t.Errorf("pwd: got %q, %v", reason, err)
	}
	if len(r.approved) != 0 {
		t.Errorf("pwd asked for approval")
	}
	if reason, _ := a.policy("run_command", toolArgs{Command: "touch x"}); reason == "" {
		t.Error("touch x was not blocked")
	}
	if reason, _ := a.policy("write_file", toolArgs{Path: "x", Content: "hello"}); reason == "" {
		t.Error("write was not blocked")
	}
	if len(r.approved) != 2 {
		t.Errorf("approvals asked: %v", r.approved)
	}
	// A denied call never runs.
	if got := a.callTool(context.Background(), "write_file", toolArgs{Path: "x", Content: "hello"}); got != "User denied this action" {
		t.Errorf("denied write returned %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Error("denied write created the file")
	}
	if !safeInspection("git status --short") || safeInspection("ls; touch x") {
		t.Error("safeInspection")
	}
	if _, err := a.execute(context.Background(), "write_file", toolArgs{Path: "x", Content: "approved"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "x")); string(data) != "approved" {
		t.Errorf("file holds %q", data)
	}
}

func TestAutoApprovalKeepsPathChecks(t *testing.T) {
	a, _ := newAgent(tempRoot(t), true)
	if reason, err := a.policy("run_command", toolArgs{Command: "touch x"}); reason != "" || err != nil {
		t.Errorf("touch x: got %q, %v", reason, err)
	}
	if reason, err := a.policy("write_file", toolArgs{Path: "x", Content: "hello"}); reason != "" || err != nil {
		t.Errorf("write x: got %q, %v", reason, err)
	}
	if _, err := a.policy("write_file", toolArgs{Path: "../x", Content: "hello"}); err == nil {
		t.Error("write outside the tree allowed")
	}
}

func TestReadListSearchAndRun(t *testing.T) {
	root := tempRoot(t)
	os.MkdirAll(filepath.Join(root, "src", "node_modules"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("one\nneedle here\n"), 0o644)
	os.WriteFile(filepath.Join(root, "src", "node_modules", "b.txt"), []byte("needle"), 0o644)
	a, _ := newAgent(root, true)
	ctx := context.Background()
	if got, _ := a.execute(ctx, "list_files", toolArgs{}); got != filepath.Join("src", "a.txt") {
		t.Errorf("list_files: %q", got)
	}
	if got, _ := a.execute(ctx, "search_files", toolArgs{Text: "needle", Path: "src"}); got != filepath.Join("src", "a.txt")+":2:needle here" {
		t.Errorf("search_files: %q", got)
	}
	if got, _ := a.execute(ctx, "read_file", toolArgs{Path: "src/a.txt"}); got != "one\nneedle here\n" {
		t.Errorf("read_file: %q", got)
	}
	if got, err := a.execute(ctx, "run_command", toolArgs{Command: "echo hi; exit 3"}); err != nil || got != "Exit 3\nhi\n" {
		t.Errorf("run_command: %q, %v", got, err)
	}
	if _, err := a.execute(ctx, "run_command", toolArgs{Command: "yes"}); err == nil || err.Error() != "Command output is too large" {
		t.Errorf("run_command with endless output: %v", err)
	}
}

// sse writes a Responses API stream.
func sse(w http.ResponseWriter, events ...any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		data, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: x\ndata: %s\n\n", data)
	}
}

func TestRunLoopsThroughToolCalls(t *testing.T) {
	root := tempRoot(t)
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("buy milk"), 0o644)
	var requests []responseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req responseRequest
		json.NewDecoder(r.Body).Decode(&req)
		requests = append(requests, req)
		if len(requests) == 1 {
			call := map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_file", "arguments": `{"path":"notes.txt"}`}
			sse(w, map[string]any{"type": "response.output_item.done", "item": call},
				map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{call}}})
			return
		}
		msg := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Buy milk."}}}
		sse(w, map[string]any{"type": "response.output_text.delta", "delta": "Buy "},
			map[string]any{"type": "response.output_text.delta", "delta": "milk."},
			map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{msg}}})
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)

	a, r := newAgent(root, true)
	history, err := a.run(context.Background(), "what do my notes say?", "Linux", "bash", "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.out.String() != "Buy milk." || strings.Join(r.tools, ",") != "read_file" {
		t.Errorf("text %q, tools %v", r.out.String(), r.tools)
	}
	if len(requests) != 2 || requests[0].Store || requests[0].PromptCacheKey != "s1" || requests[0].Reasoning["effort"] != "low" {
		t.Fatalf("requests: %+v", requests)
	}
	// The second request carries the call and its result after the prompt.
	if len(requests[1].Input) != 3 || !strings.Contains(string(requests[1].Input[2]), `"output":"buy milk"`) {
		t.Errorf("second input: %s", requests[1].Input)
	}
	if len(history) != 4 {
		t.Errorf("history has %d items", len(history))
	}
}

func TestRunReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided"}}`)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)
	a, _ := newAgent(tempRoot(t), true)
	if _, err := a.run(context.Background(), "hi there", "Linux", "bash", "s", nil); err == nil || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Errorf("got %v", err)
	}
}

func TestSessionsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	file, err := openSessions("terminal-1")
	if err != nil {
		t.Fatal(err)
	}
	saved := &session{
		Input:  []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)},
		Agents: map[string]string{"codex": "thread-1"},
	}
	if err := saveSession(file, saved); err != nil {
		t.Fatal(err)
	}
	got, err := loadSession(file)
	if err != nil || len(got.Input) != 1 || string(got.Input[0]) != string(saved.Input[0]) || got.Agents["codex"] != "thread-1" {
		t.Errorf("got %+v, %v", got, err)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("session mode %v", info.Mode())
	}
}
