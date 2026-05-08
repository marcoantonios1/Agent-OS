package generic

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
)

// Load reads agent.yaml and SYSTEM.md from dir and returns a configured Agent.
func Load(dir string, llm costguard.LLMClient, globalRegistry *tools.ToolRegistry) (*Agent, error) {
	cfg, err := loadConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("generic.Load %q: %w", dir, err)
	}
	return New(cfg, globalRegistry, llm)
}

// LoadAll scans dir for sub-directories, each containing an agent.yaml and
// SYSTEM.md. Successfully loaded agents are keyed by each of their declared
// intents. Directories that fail to load are logged and skipped — the caller
// receives only the agents that parsed and validated successfully.
func LoadAll(dir string, llm costguard.LLMClient, globalRegistry *tools.ToolRegistry) (map[router.Intent]router.Agent, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("generic.LoadAll: cannot read %q: %w", dir, err)
	}

	out := make(map[router.Intent]router.Agent)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		agentDir := filepath.Join(dir, e.Name())
		ag, err := Load(agentDir, llm, globalRegistry)
		if err != nil {
			slog.Warn("generic.LoadAll: skipping agent", "dir", agentDir, "error", err)
			continue
		}
		for _, intent := range ag.cfg.Intents {
			out[router.Intent(intent)] = ag
		}
		slog.Info("generic.LoadAll: loaded agent", "id", ag.cfg.ID, "intents", ag.cfg.Intents)
	}
	return out, nil
}

func loadConfig(dir string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		return Config{}, fmt.Errorf("read agent.yaml: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse agent.yaml: %w", err)
	}

	sysData, err := os.ReadFile(filepath.Join(dir, "SYSTEM.md"))
	if err != nil {
		return Config{}, fmt.Errorf("read SYSTEM.md: %w", err)
	}
	cfg.SystemPrompt = string(sysData)

	soulData, err := os.ReadFile(filepath.Join(dir, "SOUL.md"))
	if err == nil {
		cfg.SystemPrompt += "\n\n" + string(soulData)
	}

	return cfg, nil
}
