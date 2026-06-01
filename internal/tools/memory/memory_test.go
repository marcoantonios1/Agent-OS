package memory_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	memorytool "github.com/marcoantonios1/Agent-OS/internal/tools/memory"
)

// mockStore is a test double for episodic.Store.
type mockStore struct {
	saved     []episodic.Memory
	searched  []string
	deleted   []string
	results   []episodic.Memory
	saveErr   error
	searchErr error
	deleteErr error
}

func (m *mockStore) Save(_ context.Context, mem episodic.Memory, _ []float32) error {
	return m.saveErr
}

func (m *mockStore) SaveText(_ context.Context, mem episodic.Memory) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, mem)
	return nil
}

func (m *mockStore) Search(_ context.Context, _ string, _ []float32, _ int) ([]episodic.Memory, error) {
	return m.results, m.searchErr
}

func (m *mockStore) SearchByText(_ context.Context, _, query string, _ int) ([]episodic.Memory, error) {
	m.searched = append(m.searched, query)
	return m.results, m.searchErr
}

func (m *mockStore) Recent(_ context.Context, _ string, _ int) ([]episodic.Memory, error) {
	return nil, nil
}

func (m *mockStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deleted = append(m.deleted, id)
	return nil
}

func (m *mockStore) Prune(_ context.Context, _ string, _ time.Duration) error { return nil }

// ── SaveTool ──────────────────────────────────────────────────────────────────

func TestSaveTool_Execute_PersistsMemory(t *testing.T) {
	store := &mockStore{}
	tool := memorytool.NewSaveTool(store)

	ctx := sessions.WithUserID(context.Background(), "user1")
	raw, _ := json.Marshal(map[string]string{"fact": "Alice's birthday is March 15"})

	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 saved memory, got %d", len(store.saved))
	}
	if store.saved[0].Content != "Alice's birthday is March 15" {
		t.Errorf("content = %q", store.saved[0].Content)
	}
	if store.saved[0].Source != "explicit" {
		t.Errorf("source = %q, want explicit", store.saved[0].Source)
	}
	if !strings.Contains(out, "Alice's birthday") {
		t.Errorf("output = %q", out)
	}
}

func TestSaveTool_Execute_EmptyFact_ReturnsError(t *testing.T) {
	store := &mockStore{}
	tool := memorytool.NewSaveTool(store)

	ctx := sessions.WithUserID(context.Background(), "user1")
	raw, _ := json.Marshal(map[string]string{"fact": ""})

	_, err := tool.Execute(ctx, raw)
	if err == nil {
		t.Fatal("expected error for empty fact")
	}
}

// ── SearchTool ────────────────────────────────────────────────────────────────

func TestSearchTool_Execute_ReturnsFormattedResults(t *testing.T) {
	store := &mockStore{results: []episodic.Memory{
		{ID: "1", Content: "Alice's birthday is March 15", Distance: 0.2, CreatedAt: time.Now().Add(-3 * 24 * time.Hour)},
		{ID: "2", Content: "Alice prefers email over Slack", Distance: 0.4, CreatedAt: time.Now().Add(-7 * 24 * time.Hour)},
	}}
	tool := memorytool.NewSearchTool(store)

	ctx := sessions.WithUserID(context.Background(), "user1")
	raw, _ := json.Marshal(map[string]string{"query": "Alice"})

	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(store.searched) != 1 || store.searched[0] != "Alice" {
		t.Errorf("searched = %v", store.searched)
	}
	if !strings.Contains(out, "Alice's birthday") {
		t.Errorf("output = %q", out)
	}
	if !strings.Contains(out, "days ago") {
		t.Errorf("output should contain relative age, got: %q", out)
	}
}

func TestSearchTool_Execute_NoResults(t *testing.T) {
	store := &mockStore{results: nil}
	tool := memorytool.NewSearchTool(store)

	ctx := sessions.WithUserID(context.Background(), "user1")
	raw, _ := json.Marshal(map[string]string{"query": "xyzzy"})

	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "No relevant memories found") {
		t.Errorf("expected no-results message, got: %q", out)
	}
}

// ── ForgetTool ────────────────────────────────────────────────────────────────

func TestForgetTool_Execute_DeletesClosestMatch(t *testing.T) {
	store := &mockStore{results: []episodic.Memory{
		{ID: "mem-42", Content: "Alice's birthday is March 15", Distance: 0.3, CreatedAt: time.Now()},
	}}
	tool := memorytool.NewForgetTool(store)

	ctx := sessions.WithUserID(context.Background(), "user1")
	raw, _ := json.Marshal(map[string]string{"fact": "Alice birthday"})

	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "mem-42" {
		t.Errorf("deleted = %v", store.deleted)
	}
	if !strings.Contains(out, "Forgotten") {
		t.Errorf("output = %q", out)
	}
}

func TestForgetTool_Execute_LowConfidenceMatch_DoesNotDelete(t *testing.T) {
	store := &mockStore{results: []episodic.Memory{
		{ID: "mem-99", Content: "Some unrelated memory", Distance: 0.9, CreatedAt: time.Now()},
	}}
	tool := memorytool.NewForgetTool(store)

	ctx := sessions.WithUserID(context.Background(), "user1")
	raw, _ := json.Marshal(map[string]string{"fact": "Alice birthday"})

	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected no deletions for low-confidence match, got %v", store.deleted)
	}
	if !strings.Contains(out, "No sufficiently close memory") {
		t.Errorf("output = %q", out)
	}
}

func TestForgetTool_Execute_NoMatch(t *testing.T) {
	store := &mockStore{results: nil}
	tool := memorytool.NewForgetTool(store)

	ctx := sessions.WithUserID(context.Background(), "user1")
	raw, _ := json.Marshal(map[string]string{"fact": "something"})

	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "No matching memory found") {
		t.Errorf("output = %q", out)
	}
}
