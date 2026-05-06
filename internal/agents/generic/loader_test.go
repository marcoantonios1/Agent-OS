package generic

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/tools"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// writeAgentDir creates a temp dir containing agent.yaml and SYSTEM.md.
func writeAgentDir(t *testing.T, dir, yamlContent, sysContent string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if yamlContent != "" {
		if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(yamlContent), 0o640); err != nil {
			t.Fatalf("write agent.yaml: %v", err)
		}
	}
	if sysContent != "" {
		if err := os.WriteFile(filepath.Join(dir, "SYSTEM.md"), []byte(sysContent), 0o640); err != nil {
			t.Fatalf("write SYSTEM.md: %v", err)
		}
	}
}

const validYAML = `
id: support
model: gemma4:26b
max_tokens: 2048
intents:
  - support
skills:
  - web_search
sub_agents: []
`

const validSystem = "You are a support agent."

// ── Load ──────────────────────────────────────────────────────────────────────

func TestLoad_ValidDir(t *testing.T) {
	dir := t.TempDir()
	writeAgentDir(t, dir, validYAML, validSystem)

	ag, err := Load(dir, &fakeLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ag.cfg.ID != "support" {
		t.Errorf("ID = %q, want %q", ag.cfg.ID, "support")
	}
	if ag.cfg.Model != "gemma4:26b" {
		t.Errorf("Model = %q", ag.cfg.Model)
	}
	if ag.cfg.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", ag.cfg.MaxTokens)
	}
	if ag.cfg.SystemPrompt != validSystem {
		t.Errorf("SystemPrompt = %q", ag.cfg.SystemPrompt)
	}
	if len(ag.cfg.Intents) != 1 || ag.cfg.Intents[0] != "support" {
		t.Errorf("Intents = %v", ag.cfg.Intents)
	}
}

func TestLoad_MissingYAML(t *testing.T) {
	dir := t.TempDir()
	writeAgentDir(t, dir, "", validSystem)

	_, err := Load(dir, &fakeLLM{}, tools.NewRegistry())
	if err == nil {
		t.Fatal("expected error for missing agent.yaml")
	}
}

func TestLoad_MissingSystemMD(t *testing.T) {
	dir := t.TempDir()
	writeAgentDir(t, dir, validYAML, "")

	_, err := Load(dir, &fakeLLM{}, tools.NewRegistry())
	if err == nil {
		t.Fatal("expected error for missing SYSTEM.md")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	writeAgentDir(t, dir, "{{invalid yaml{{", validSystem)

	_, err := Load(dir, &fakeLLM{}, tools.NewRegistry())
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_EmptyID_FailsValidation(t *testing.T) {
	dir := t.TempDir()
	writeAgentDir(t, dir, "model: m\n", validSystem)

	_, err := Load(dir, &fakeLLM{}, tools.NewRegistry())
	if err == nil {
		t.Fatal("expected error for missing id in YAML")
	}
}

// ── LoadAll ───────────────────────────────────────────────────────────────────

func TestLoadAll_LoadsValidAgents(t *testing.T) {
	root := t.TempDir()
	writeAgentDir(t, filepath.Join(root, "support"), validYAML, validSystem)

	agents, err := LoadAll(root, &fakeLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := agents["support"]; !ok {
		t.Errorf("expected intent %q in loaded agents; got keys: %v", "support", agentKeys(agents))
	}
}

func TestLoadAll_SkipsBadDirs(t *testing.T) {
	root := t.TempDir()
	// bad dir: missing SYSTEM.md
	writeAgentDir(t, filepath.Join(root, "bad"), validYAML, "")
	// good dir
	writeAgentDir(t, filepath.Join(root, "support"), validYAML, validSystem)

	agents, err := LoadAll(root, &fakeLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := agents["support"]; !ok {
		t.Errorf("expected support agent to be loaded")
	}
	if len(agents) != 1 {
		t.Errorf("expected 1 agent, got %d: %v", len(agents), agentKeys(agents))
	}
}

func TestLoadAll_MultipleAgents_MultipleIntents(t *testing.T) {
	root := t.TempDir()
	yaml2 := `
id: billing
model: gemma4:26b
intents:
  - billing
  - payments
`
	writeAgentDir(t, filepath.Join(root, "support"), validYAML, validSystem)
	writeAgentDir(t, filepath.Join(root, "billing"), yaml2, "You are billing.")

	agents, err := LoadAll(root, &fakeLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(agents) != 3 { // support + billing + payments
		t.Errorf("expected 3 intent entries, got %d: %v", len(agents), agentKeys(agents))
	}
}

func TestLoadAll_EmptyDir_ReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	agents, err := LoadAll(root, &fakeLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty map, got %v", agentKeys(agents))
	}
}

func TestLoadAll_NonexistentDir_ReturnsError(t *testing.T) {
	_, err := LoadAll("/does/not/exist", &fakeLLM{}, tools.NewRegistry())
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestLoadAll_IgnoresFiles(t *testing.T) {
	root := t.TempDir()
	// A file at the root level (not a dir) should be silently skipped.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi"), 0o640); err != nil {
		t.Fatal(err)
	}
	writeAgentDir(t, filepath.Join(root, "support"), validYAML, validSystem)

	agents, err := LoadAll(root, &fakeLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(agents))
	}
}

func TestLoadAll_SkipsDirWithNoAgentYAML(t *testing.T) {
	root := t.TempDir()
	// A subdir with no agent.yaml must be silently skipped — it is not an agent dir.
	noYAMLDir := filepath.Join(root, "not-an-agent")
	if err := os.MkdirAll(noYAMLDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Only a SYSTEM.md, no agent.yaml
	if err := os.WriteFile(filepath.Join(noYAMLDir, "SYSTEM.md"), []byte("sys"), 0o640); err != nil {
		t.Fatal(err)
	}
	writeAgentDir(t, filepath.Join(root, "support"), validYAML, validSystem)

	agents, err := LoadAll(root, &fakeLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	// Only the valid agent should appear; the dir without agent.yaml is skipped.
	if len(agents) != 1 {
		t.Errorf("expected 1 agent, got %d: %v", len(agents), agentKeys(agents))
	}
	if _, ok := agents["support"]; !ok {
		t.Errorf("expected support agent; got %v", agentKeys(agents))
	}
}

// ── helper ────────────────────────────────────────────────────────────────────

func agentKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
