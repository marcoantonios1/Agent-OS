// Package code implements the Builder Agent's file and shell tools.
//
// Safety model:
//   - All file operations are confined to a configurable sandbox directory;
//     paths that escape it (via ".." traversal) are rejected.
//   - shell_run checks every command against a disallowed-prefix list before
//     execution and enforces a configurable timeout (default 30 s).
//   - All file writes are logged with the session ID from context.
package code

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

const defaultShellTimeout = 30 * time.Second

// defaultDisallowed is the built-in list of command prefixes that are never
// allowed to run, regardless of configuration.
var defaultDisallowed = []string{
	"rm -rf",
	"rm -r",
	"mkfs",
	"dd ",
	":(){ :|:& };:", // fork bomb
	"curl ",
	"wget ",
	"nc ",
	"ncat ",
	"netcat ",
}

// Config controls sandbox behaviour. Zero value is unsafe — always provide a
// SandboxDir before using file or shell tools.
type Config struct {
	// SandboxDir is the root directory that all file operations are confined to.
	// Must be an absolute path.
	SandboxDir string
	// ExtraDisallowed extends the built-in disallowed command list.
	ExtraDisallowed []string
	// ShellTimeout overrides the default 30 s timeout for shell_run.
	// Zero means use the default.
	ShellTimeout time.Duration
}

func (c Config) shellTimeout() time.Duration {
	if c.ShellTimeout > 0 {
		return c.ShellTimeout
	}
	return defaultShellTimeout
}

func (c Config) disallowed() []string {
	out := make([]string, len(defaultDisallowed)+len(c.ExtraDisallowed))
	copy(out, defaultDisallowed)
	copy(out[len(defaultDisallowed):], c.ExtraDisallowed)
	return out
}

// safePath resolves p relative to the sandbox and returns an error if the
// resolved path would escape the sandbox root.
func (c Config) safePath(p string) (string, error) {
	// Join with sandbox, then clean to eliminate any ".." components.
	abs := filepath.Join(c.SandboxDir, p)
	abs = filepath.Clean(abs)

	sandboxClean := filepath.Clean(c.SandboxDir)
	if !strings.HasPrefix(abs, sandboxClean+string(os.PathSeparator)) && abs != sandboxClean {
		return "", fmt.Errorf("path %q escapes sandbox", p)
	}
	return abs, nil
}

// ── file_read ─────────────────────────────────────────────────────────────────

type fileReadInput struct {
	Path string `json:"path"`
}

// ReadTool implements the file_read tool.
type ReadTool struct{ cfg Config }

// NewReadTool returns a file_read tool confined to cfg.SandboxDir.
func NewReadTool(cfg Config) *ReadTool { return &ReadTool{cfg: cfg} }

func (t *ReadTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "file_read",
		Description: "Read the contents of a file. Path is relative to the sandbox root.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the file to read.",
				},
			},
		},
	}
}

func (t *ReadTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in fileReadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("file_read: invalid input: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("file_read: path is required")
	}

	abs, err := t.cfg.safePath(in.Path)
	if err != nil {
		return "", fmt.Errorf("file_read: %w", err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("file_read: %w", err)
	}

	out, _ := json.Marshal(map[string]string{"path": in.Path, "content": string(data)})
	return string(out), nil
}

// ── file_write ────────────────────────────────────────────────────────────────

type fileWriteInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteTool implements the file_write tool.
type WriteTool struct{ cfg Config }

// NewWriteTool returns a file_write tool confined to cfg.SandboxDir.
func NewWriteTool(cfg Config) *WriteTool { return &WriteTool{cfg: cfg} }

func (t *WriteTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "file_write",
		Description: "Write or overwrite a file with the given content. Path is relative to the sandbox root. Parent directories are created automatically.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"path", "content"},
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the file to write.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full content to write to the file.",
				},
			},
		},
	}
}

func (t *WriteTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in fileWriteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("file_write: invalid input: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("file_write: path is required")
	}

	abs, err := t.cfg.safePath(in.Path)
	if err != nil {
		return "", fmt.Errorf("file_write: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("file_write: create parent dirs: %w", err)
	}

	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return "", fmt.Errorf("file_write: %w", err)
	}

	sessionID := approval.SessionIDFromContext(ctx)
	slog.InfoContext(ctx, "file_write",
		"path", in.Path,
		"bytes", len(in.Content),
		"session_id", sessionID,
	)

	out, _ := json.Marshal(map[string]any{
		"path":  in.Path,
		"bytes": len(in.Content),
	})
	return string(out), nil
}

// ── file_list ─────────────────────────────────────────────────────────────────

type fileListInput struct {
	Dir string `json:"dir"`
}

// ListTool implements the file_list tool.
type ListTool struct{ cfg Config }

// NewListTool returns a file_list tool confined to cfg.SandboxDir.
func NewListTool(cfg Config) *ListTool { return &ListTool{cfg: cfg} }

func (t *ListTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "file_list",
		Description: "List files and directories inside a directory. Path is relative to the sandbox root. Use \".\" or \"\" for the root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dir": map[string]any{
					"type":        "string",
					"description": "Relative path to the directory to list. Defaults to sandbox root.",
				},
			},
		},
	}
}

func (t *ListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in fileListInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("file_list: invalid input: %w", err)
		}
	}
	if in.Dir == "" {
		in.Dir = "."
	}

	abs, err := t.cfg.safePath(in.Dir)
	if err != nil {
		return "", fmt.Errorf("file_list: %w", err)
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("file_list: %w", err)
	}

	type entry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size,omitempty"`
	}
	result := make([]entry, 0, len(entries))
	for _, e := range entries {
		en := entry{Name: e.Name(), IsDir: e.IsDir()}
		if !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				en.Size = fi.Size()
			}
		}
		result = append(result, en)
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}

// ── shell_run ─────────────────────────────────────────────────────────────────

type shellRunInput struct {
	Command string `json:"command"`
}

// ShellTool implements the shell_run tool.
type ShellTool struct{ cfg Config }

// NewShellTool returns a shell_run tool with the given config.
func NewShellTool(cfg Config) *ShellTool { return &ShellTool{cfg: cfg} }

func (t *ShellTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "shell_run",
		Description: "Run a shell command (e.g. go build, go test, go vet) inside the sandbox directory. Captures stdout and stderr. Timeout is enforced. Destructive or network commands are blocked.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Shell command to execute, e.g. \"go test ./...\" or \"go vet ./...\".",
				},
			},
		},
	}
}

func (t *ShellTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in shellRunInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("shell_run: invalid input: %w", err)
	}
	if in.Command == "" {
		return "", fmt.Errorf("shell_run: command is required")
	}

	// Safety check: reject disallowed command prefixes.
	cmdLower := strings.TrimSpace(strings.ToLower(in.Command))
	for _, blocked := range t.cfg.disallowed() {
		if strings.HasPrefix(cmdLower, strings.ToLower(blocked)) {
			out, _ := json.Marshal(map[string]any{
				"allowed": false,
				"error":   fmt.Sprintf("command %q is not allowed", in.Command),
			})
			return string(out), nil
		}
	}

	timeout := t.cfg.shellTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", in.Command)
	cmd.Dir = t.cfg.SandboxDir

	output, err := cmd.CombinedOutput()

	result := map[string]any{
		"command": in.Command,
		"output":  string(output),
	}

	if ctx.Err() == context.DeadlineExceeded {
		result["error"] = fmt.Sprintf("command timed out after %s", timeout)
	} else if err != nil {
		result["exit_code"] = cmd.ProcessState.ExitCode()
		result["error"] = err.Error()
	} else {
		result["exit_code"] = 0
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
