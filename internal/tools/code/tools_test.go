package code_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func tempSandbox(t *testing.T) code.Config {
	t.Helper()
	dir := t.TempDir()
	return code.Config{SandboxDir: dir}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func decodeMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode map: %v — raw: %s", err, s)
	}
	return m
}

func decodeSlice(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("decode slice: %v — raw: %s", err, s)
	}
	return out
}

// ── file_read ─────────────────────────────────────────────────────────────────

func TestReadTool_Definition(t *testing.T) {
	def := code.NewReadTool(tempSandbox(t)).Definition()
	if def.Name != "file_read" {
		t.Errorf("name = %q, want file_read", def.Name)
	}
}

func TestReadTool_ReadsExistingFile(t *testing.T) {
	cfg := tempSandbox(t)
	content := "hello, world\n"
	if err := os.WriteFile(filepath.Join(cfg.SandboxDir, "hello.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	result, err := code.NewReadTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "hello.txt"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["content"] != content {
		t.Errorf("content = %q, want %q", m["content"], content)
	}
}

func TestReadTool_MissingPath(t *testing.T) {
	_, err := code.NewReadTool(tempSandbox(t)).Execute(context.Background(),
		mustMarshal(t, map[string]string{}))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestReadTool_PathEscapeRejected(t *testing.T) {
	_, err := code.NewReadTool(tempSandbox(t)).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "../../etc/passwd"}))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	_, err := code.NewReadTool(tempSandbox(t)).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "nonexistent.txt"}))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadTool_InvalidJSON(t *testing.T) {
	_, err := code.NewReadTool(tempSandbox(t)).Execute(context.Background(), []byte(`{bad`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ── file_write ────────────────────────────────────────────────────────────────

func TestWriteTool_Definition(t *testing.T) {
	def := code.NewWriteTool(tempSandbox(t)).Definition()
	if def.Name != "file_write" {
		t.Errorf("name = %q, want file_write", def.Name)
	}
}

func TestWriteTool_WritesFile(t *testing.T) {
	cfg := tempSandbox(t)
	content := "package main\n"

	result, err := code.NewWriteTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "main.go", "content": content}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["path"] != "main.go" {
		t.Errorf("path = %v", m["path"])
	}

	got, err := os.ReadFile(filepath.Join(cfg.SandboxDir, "main.go"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != content {
		t.Errorf("on-disk content = %q, want %q", got, content)
	}
}

func TestWriteRead_RoundTrip(t *testing.T) {
	cfg := tempSandbox(t)
	content := "round trip content"

	_, err := code.NewWriteTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "rt.txt", "content": content}))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := code.NewReadTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "rt.txt"}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if decodeMap(t, result)["content"] != content {
		t.Error("round-trip content mismatch")
	}
}

func TestWriteTool_CreatesParentDirs(t *testing.T) {
	cfg := tempSandbox(t)
	_, err := code.NewWriteTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "a/b/c.txt", "content": "nested"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SandboxDir, "a/b/c.txt")); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestWriteTool_PathEscapeRejected(t *testing.T) {
	_, err := code.NewWriteTool(tempSandbox(t)).Execute(context.Background(),
		mustMarshal(t, map[string]string{"path": "../escape.txt", "content": "bad"}))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestWriteTool_MissingPath(t *testing.T) {
	_, err := code.NewWriteTool(tempSandbox(t)).Execute(context.Background(),
		mustMarshal(t, map[string]string{"content": "hi"}))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

// ── file_list ─────────────────────────────────────────────────────────────────

func TestListTool_Definition(t *testing.T) {
	def := code.NewListTool(tempSandbox(t)).Definition()
	if def.Name != "file_list" {
		t.Errorf("name = %q, want file_list", def.Name)
	}
}

func TestListTool_ListsFiles(t *testing.T) {
	cfg := tempSandbox(t)
	os.WriteFile(filepath.Join(cfg.SandboxDir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(cfg.SandboxDir, "b.txt"), []byte("b"), 0o644)

	result, err := code.NewListTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"dir": "."}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) != 2 {
		t.Errorf("got %d items, want 2", len(items))
	}
}

func TestListTool_EmptyDir(t *testing.T) {
	cfg := tempSandbox(t)
	result, err := code.NewListTool(cfg).Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) != 0 {
		t.Errorf("expected empty list, got %d items", len(items))
	}
}

func TestListTool_PathEscapeRejected(t *testing.T) {
	_, err := code.NewListTool(tempSandbox(t)).Execute(context.Background(),
		mustMarshal(t, map[string]string{"dir": "../../"}))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// ── shell_run ─────────────────────────────────────────────────────────────────

func TestShellTool_Definition(t *testing.T) {
	def := code.NewShellTool(tempSandbox(t)).Definition()
	if def.Name != "shell_run" {
		t.Errorf("name = %q, want shell_run", def.Name)
	}
}

func TestShellTool_RunsCommand(t *testing.T) {
	cfg := tempSandbox(t)
	result, err := code.NewShellTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"command": "echo hello"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if !strings.Contains(m["output"].(string), "hello") {
		t.Errorf("output = %q, want it to contain 'hello'", m["output"])
	}
	if m["exit_code"] != float64(0) {
		t.Errorf("exit_code = %v, want 0", m["exit_code"])
	}
}

func TestShellTool_DisallowedCommand(t *testing.T) {
	cfg := tempSandbox(t)
	result, err := code.NewShellTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"command": "rm -rf /"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["allowed"] != false {
		t.Error("expected allowed=false for disallowed command")
	}
	if m["error"] == "" {
		t.Error("expected non-empty error message")
	}
}

func TestShellTool_DisallowedNetwork(t *testing.T) {
	cfg := tempSandbox(t)
	for _, cmd := range []string{"curl http://example.com", "wget http://example.com"} {
		result, err := code.NewShellTool(cfg).Execute(context.Background(),
			mustMarshal(t, map[string]string{"command": cmd}))
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", cmd, err)
		}
		m := decodeMap(t, result)
		if m["allowed"] != false {
			t.Errorf("command %q should be blocked", cmd)
		}
	}
}

func TestShellTool_ExtraDisallowed(t *testing.T) {
	cfg := tempSandbox(t)
	cfg.ExtraDisallowed = []string{"git push"}

	result, err := code.NewShellTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"command": "git push origin main"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decodeMap(t, result)["allowed"] != false {
		t.Error("expected git push to be blocked via ExtraDisallowed")
	}
}

func TestShellTool_Timeout(t *testing.T) {
	cfg := tempSandbox(t)
	cfg.ShellTimeout = 100 * time.Millisecond

	result, err := code.NewShellTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"command": "sleep 10"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	errMsg, _ := m["error"].(string)
	if !strings.Contains(errMsg, "timed out") {
		t.Errorf("expected timeout error, got: %v", m)
	}
}

func TestShellTool_NonZeroExit(t *testing.T) {
	cfg := tempSandbox(t)
	result, err := code.NewShellTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"command": "exit 1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["exit_code"] == float64(0) {
		t.Error("expected non-zero exit code")
	}
}

func TestShellTool_MissingCommand(t *testing.T) {
	_, err := code.NewShellTool(tempSandbox(t)).Execute(context.Background(),
		mustMarshal(t, map[string]string{}))
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestShellTool_RunsInSandboxDir(t *testing.T) {
	cfg := tempSandbox(t)
	result, err := code.NewShellTool(cfg).Execute(context.Background(),
		mustMarshal(t, map[string]string{"command": "pwd"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	output := strings.TrimSpace(m["output"].(string))
	// On macOS TempDir returns /var/... which symlinks to /private/var/...
	// so we compare the real paths.
	realSandbox, _ := filepath.EvalSymlinks(cfg.SandboxDir)
	realOutput, _ := filepath.EvalSymlinks(output)
	if realOutput != realSandbox {
		t.Errorf("pwd = %q, want %q", realOutput, realSandbox)
	}
}
