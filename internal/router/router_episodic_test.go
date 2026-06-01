package router

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
)

type mockEpisodicSearcher struct {
	memories []episodic.Memory
	err      error
}

func (m *mockEpisodicSearcher) SearchByText(_ context.Context, _, _ string, _ int) ([]episodic.Memory, error) {
	return m.memories, m.err
}

func makeRouterWithEpisodic(store EpisodicMemorySearcher) *Router {
	r := New(nil, nil, nil, nil)
	r.EpisodicStore = store
	return r
}

func TestInjectEpisodicMemories_MemoriesInjected(t *testing.T) {
	now := time.Now()
	store := &mockEpisodicSearcher{memories: []episodic.Memory{
		{ID: "1", Content: "Alice is difficult on budget topics", Distance: 0.2, CreatedAt: now.Add(-3 * 24 * time.Hour)},
		{ID: "2", Content: "User committed to send report by Friday", Distance: 0.3, CreatedAt: now.Add(-7 * 24 * time.Hour)},
		{ID: "3", Content: "User prefers morning meetings", Distance: 0.4, CreatedAt: now.Add(-14 * 24 * time.Hour)},
	}}
	r := makeRouterWithEpisodic(store)

	meta := make(map[string]string)
	r.injectEpisodicMemories(context.Background(), meta, "user1", "discuss budget with Alice")

	block, ok := meta["user.episodic_memories"]
	if !ok {
		t.Fatal("expected user.episodic_memories in metadata")
	}
	if !strings.Contains(block, "Alice is difficult") {
		t.Error("block should contain Alice memory")
	}
	if !strings.Contains(block, "## Relevant memories") {
		t.Error("block should contain section header")
	}
	if !strings.Contains(block, "days ago") {
		t.Error("block should contain relative age")
	}
}

func TestInjectEpisodicMemories_StoreError_NoMetadataMutated(t *testing.T) {
	store := &mockEpisodicSearcher{err: fmt.Errorf("db error")}
	r := makeRouterWithEpisodic(store)

	meta := make(map[string]string)
	r.injectEpisodicMemories(context.Background(), meta, "user1", "what did Alice say about the budget")

	if _, ok := meta["user.episodic_memories"]; ok {
		t.Error("metadata must not be mutated on store error")
	}
}

func TestInjectEpisodicMemories_ShortMessage_NoSearch(t *testing.T) {
	store := &mockEpisodicSearcher{
		memories: []episodic.Memory{
			{ID: "1", Content: "should not appear", Distance: 0.1, CreatedAt: time.Now()},
		},
	}
	r := makeRouterWithEpisodic(store)

	meta := make(map[string]string)
	r.injectEpisodicMemories(context.Background(), meta, "user1", "hi there")

	if _, ok := meta["user.episodic_memories"]; ok {
		t.Error("short message must not trigger episodic search")
	}
}

func TestInjectEpisodicMemories_DistanceFilterApplied(t *testing.T) {
	now := time.Now()
	store := &mockEpisodicSearcher{memories: []episodic.Memory{
		{ID: "1", Content: "close memory", Distance: 0.3, CreatedAt: now.Add(-24 * time.Hour)},
		{ID: "2", Content: "distant memory", Distance: 0.9, CreatedAt: now.Add(-24 * time.Hour)},
	}}
	r := makeRouterWithEpisodic(store)

	meta := make(map[string]string)
	r.injectEpisodicMemories(context.Background(), meta, "user1", "what is the meeting about")

	block := meta["user.episodic_memories"]
	if !strings.Contains(block, "close memory") {
		t.Error("close memory should be injected")
	}
	if strings.Contains(block, "distant memory") {
		t.Error("distant memory must be filtered by distance threshold")
	}
}
