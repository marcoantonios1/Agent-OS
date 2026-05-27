package episodic

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

// mockStore records SaveText calls. mu guards saved for race-safe access.
type mockStore struct {
	mu    sync.Mutex
	saved []Memory
	err   error
}

func (m *mockStore) Save(_ context.Context, mem Memory, _ []float32) error { return m.err }
func (m *mockStore) SaveText(_ context.Context, mem Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.saved = append(m.saved, mem)
	return nil
}

func (m *mockStore) savedSnapshot() []Memory {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Memory, len(m.saved))
	copy(out, m.saved)
	return out
}
func (m *mockStore) Search(_ context.Context, _ string, _ []float32, _ int) ([]Memory, error) {
	return nil, nil
}
func (m *mockStore) SearchByText(_ context.Context, _, _ string, _ int) ([]Memory, error) {
	return nil, nil
}
func (m *mockStore) Recent(_ context.Context, _ string, _ int) ([]Memory, error) {
	return nil, nil
}
func (m *mockStore) Delete(_ context.Context, _ string) error { return nil }
func (m *mockStore) Prune(_ context.Context, _ string, _ time.Duration) error { return nil }

// mockLLMExtract returns a scripted response.
type mockLLMExtract struct {
	content string
	err     error
}

func (m *mockLLMExtract) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	return costguard.CompletionResponse{Content: m.content}, m.err
}
func (m *mockLLMExtract) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	return nil, nil
}
func (m *mockLLMExtract) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

func TestExtract_JSONArray_ReturnsTwoMemories(t *testing.T) {
	llm := &mockLLMExtract{content: `["User's colleague Alice is difficult to work with", "User prefers morning meetings"]`}
	store := &mockStore{}
	ext := NewExtractor(llm, store, "test-model")

	memories, err := ext.Extract(context.Background(), "I had a tough meeting with Alice", "That sounds hard.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("got %d memories, want 2", len(memories))
	}
	if memories[0] != "User's colleague Alice is difficult to work with" {
		t.Errorf("memories[0] = %q", memories[0])
	}
	if memories[1] != "User prefers morning meetings" {
		t.Errorf("memories[1] = %q", memories[1])
	}

	// Verify ObserveAsync saves them.
	store2 := &mockStore{}
	ext2 := NewExtractor(llm, store2, "test-model")
	ext2.ObserveAsync("user-1", "sess-1", "whatsapp", "I had a tough meeting", "That sounds hard.")
	time.Sleep(100 * time.Millisecond)
	saved2 := store2.savedSnapshot()
	if len(saved2) != 2 {
		t.Errorf("store has %d saved memories, want 2", len(saved2))
	}
	for _, m := range saved2 {
		if m.UserID != "user-1" {
			t.Errorf("UserID = %q, want user-1", m.UserID)
		}
		if m.Source != "conversation" {
			t.Errorf("Source = %q, want conversation", m.Source)
		}
	}
}

func TestExtract_MalformedJSON_ReturnsError(t *testing.T) {
	llm := &mockLLMExtract{content: "not json at all"}
	store := &mockStore{}
	ext := NewExtractor(llm, store, "test-model")

	_, err := ext.Extract(context.Background(), "hi", "hello")
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}

	// ObserveAsync must not panic.
	ext2 := NewExtractor(llm, store, "test-model")
	ext2.ObserveAsync("user-1", "sess-1", "whatsapp", "hi", "hello")
	time.Sleep(100 * time.Millisecond)
	// No assertion on store — error path skips save.
}

func TestExtract_EmptyArray_NoMemoriesSaved(t *testing.T) {
	llm := &mockLLMExtract{content: "[]"}
	store := &mockStore{}
	ext := NewExtractor(llm, store, "test-model")

	memories, err := ext.Extract(context.Background(), "hi", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(memories) != 0 {
		t.Errorf("got %d memories, want 0", len(memories))
	}

	ext2 := NewExtractor(llm, store, "test-model")
	ext2.ObserveAsync("user-1", "sess-1", "whatsapp", "hi", "hello")
	time.Sleep(100 * time.Millisecond)
	if len(store.savedSnapshot()) != 0 {
		t.Errorf("store has %d saved memories, want 0", len(store.savedSnapshot()))
	}
}
