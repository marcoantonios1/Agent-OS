// Package tools defines the Tool interface and ToolRegistry used by all agents
// to declare and execute tools via Costguard's tool-calling API.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

// Tool is the interface every tool must implement.
// Agents declare tools via Definition and the agentic loop calls Execute when
// the LLM requests a tool invocation.
type Tool interface {
	// Definition returns the metadata Costguard uses to describe the tool to
	// the LLM (name, description, JSON-schema parameters).
	Definition() costguard.ToolDefinition
	// Execute runs the tool with the given JSON-encoded input and returns the
	// result as a plain string the LLM can read.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// ToolRegistry holds a named set of tools and routes LLM tool-call requests to
// the correct implementation.
type ToolRegistry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty ToolRegistry.
func NewRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry. If a tool with the same name is already
// registered it is replaced.
func (r *ToolRegistry) Register(t Tool) {
	r.tools[t.Definition().Name] = t
}

// Definitions returns the ToolDefinition slice to pass to a CompletionRequest
// so the LLM knows which tools are available.
func (r *ToolRegistry) Definitions() []costguard.ToolDefinition {
	defs := make([]costguard.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

// Execute dispatches a tool call by name. Returns a descriptive error if the
// tool is not registered.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("tool %q is not registered", name)
	}
	return t.Execute(ctx, input)
}
