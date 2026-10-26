package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	calendarmonitor "github.com/marcoantonios1/Agent-OS/internal/monitors/calendar"
	inboxmonitor "github.com/marcoantonios1/Agent-OS/internal/monitors/inbox"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── p7EpisodicStore ───────────────────────────────────────────────────────────

// p7EpisodicStore is a thread-safe in-memory episodic.Store for Phase 7 tests.
// Search uses substring matching as a proxy for semantic similarity.
// No CGO, no sqlite-vec, no real embeddings required.
type p7EpisodicStore struct {
	mu       sync.RWMutex
	memories []episodic.Memory
}

func newP7EpisodicStore() *p7EpisodicStore { return &p7EpisodicStore{} }

func (s *p7EpisodicStore) Save(_ context.Context, mem episodic.Memory, _ []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memories = append(s.memories, mem)
	return nil
}

func (s *p7EpisodicStore) SaveText(_ context.Context, mem episodic.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mem.ID == "" {
		mem.ID = "mem-" + mem.Content[:min7(len(mem.Content), 8)]
	}
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = time.Now()
	}
	s.memories = append(s.memories, mem)
	return nil
}

// Search returns the k most recently saved memories. Integration tests use
// SearchByText exclusively; this method exists only to satisfy the interface.
func (s *p7EpisodicStore) Search(_ context.Context, _ string, _ []float32, k int) ([]episodic.Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if k > len(s.memories) {
		k = len(s.memories)
	}
	return s.memories[:k], nil
}

// SearchByText does word-overlap substring matching across all stored memories.
// Each word longer than 3 characters in the query is checked (case-insensitive);
// memories matching any such word are returned, ordered newest-first, up to k.
// This gives semantic-ish recall without real embeddings.
func (s *p7EpisodicStore) SearchByText(_ context.Context, userID, query string, k int) ([]episodic.Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	words := strings.Fields(strings.ToLower(query))
	var matches []episodic.Memory
	for i := len(s.memories) - 1; i >= 0; i-- {
		mem := s.memories[i]
		if mem.UserID != "" && userID != "" && mem.UserID != userID {
			continue
		}
		lower := strings.ToLower(mem.Content)
		for _, w := range words {
			if len(w) > 3 && strings.Contains(lower, w) {
				mem.Distance = 0.1 // close match
				matches = append(matches, mem)
				break
			}
		}
		if len(matches) >= k {
			break
		}
	}
	return matches, nil
}

func (s *p7EpisodicStore) Recent(_ context.Context, _ string, k int) ([]episodic.Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]episodic.Memory, 0, k)
	for i := len(s.memories) - 1; i >= 0 && len(result) < k; i-- {
		result = append(result, s.memories[i])
	}
	return result, nil
}

func (s *p7EpisodicStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.memories {
		if m.ID == id {
			s.memories = append(s.memories[:i], s.memories[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *p7EpisodicStore) Prune(_ context.Context, userID string, maxAge time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	var kept []episodic.Memory
	for _, m := range s.memories {
		if m.UserID == userID && m.AccessCount == 0 && m.CreatedAt.Before(cutoff) {
			continue
		}
		kept = append(kept, m)
	}
	s.memories = kept
	return nil
}

func (s *p7EpisodicStore) all() []episodic.Memory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]episodic.Memory, len(s.memories))
	copy(cp, s.memories)
	return cp
}

func (s *p7EpisodicStore) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.memories)
}

var _ episodic.Store = (*p7EpisodicStore)(nil)

func min7(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── p7Notifier ────────────────────────────────────────────────────────────────

// p7Notifier records notifications delivered by monitor workers.
type p7Notifier struct {
	mu   sync.Mutex
	msgs []*sessions.Reminder
}

func (n *p7Notifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, r)
	return nil
}
func (n *p7Notifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}
func (n *p7Notifier) last() *sessions.Reminder {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.msgs) == 0 {
		return nil
	}
	return n.msgs[len(n.msgs)-1]
}

// ── p7MockEmailProvider ───────────────────────────────────────────────────────

type p7MockEmailProvider struct {
	emails []email.EmailSummary
	err    error
}

func (m *p7MockEmailProvider) List(_ context.Context, _ int) ([]email.EmailSummary, error) {
	return m.emails, m.err
}
func (m *p7MockEmailProvider) Read(_ context.Context, _ string) (*email.Email, error) {
	return nil, nil
}
func (m *p7MockEmailProvider) Search(_ context.Context, _ string) ([]email.EmailSummary, error) {
	return nil, nil
}
func (m *p7MockEmailProvider) Draft(_ context.Context, _, _, _ string) (*email.Draft, error) {
	return nil, nil
}
func (m *p7MockEmailProvider) Send(_ context.Context, _, _, _ string) error { return nil }

// ── p7MockCalendarProvider ────────────────────────────────────────────────────

type p7MockCalendarProvider struct {
	events []calendar.Event
	err    error
}

func (m *p7MockCalendarProvider) List(_ context.Context, _, _ time.Time) ([]calendar.Event, error) {
	return m.events, m.err
}
func (m *p7MockCalendarProvider) Read(_ context.Context, _ string) (*calendar.Event, error) {
	return nil, nil
}
func (m *p7MockCalendarProvider) Create(_ context.Context, _ calendar.CreateEventInput) (*calendar.Event, error) {
	return nil, nil
}
func (m *p7MockCalendarProvider) Update(_ context.Context, _ calendar.UpdateEventInput) (*calendar.Event, error) {
	return nil, nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestPhase7_EpisodicMemory_SaveAndSearch saves 3 memories and verifies that
// a semantically relevant query returns the correct memory.
func TestPhase7_EpisodicMemory_SaveAndSearch(t *testing.T) {
	ctx := context.Background()
	store := newP7EpisodicStore()

	memories := []episodic.Memory{
		{ID: "m1", UserID: "u1", Content: "Alice manages the Q3 budget approval process", Source: "conversation", CreatedAt: time.Now()},
		{ID: "m2", UserID: "u1", Content: "User prefers morning meetings over afternoon ones", Source: "conversation", CreatedAt: time.Now()},
		{ID: "m3", UserID: "u1", Content: "The new API deployment went live on Tuesday", Source: "conversation", CreatedAt: time.Now()},
	}
	for _, m := range memories {
		if err := store.SaveText(ctx, m); err != nil {
			t.Fatalf("SaveText: %v", err)
		}
	}

	// Query about budget should surface m1, not m2 or m3.
	results, err := store.SearchByText(ctx, "u1", "budget approval Alice", 3)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result, got 0")
	}
	if !strings.Contains(results[0].Content, "Alice") {
		t.Errorf("top result should be about Alice/budget, got: %q", results[0].Content)
	}
}

// TestPhase7_EpisodicMemory_SemanticMatch_DifferentWording saves a memory about
// a difficult meeting with Alice and verifies that a differently-worded query
// still finds it via word-overlap matching.
func TestPhase7_EpisodicMemory_SemanticMatch_DifferentWording(t *testing.T) {
	ctx := context.Background()
	store := newP7EpisodicStore()

	_ = store.SaveText(ctx, episodic.Memory{
		ID:        "m-alice-budget",
		UserID:    "u1",
		Content:   "Alice was difficult in the budget meeting and rejected the proposal",
		Source:    "conversation",
		CreatedAt: time.Now(),
	})
	_ = store.SaveText(ctx, episodic.Memory{
		ID:        "m-pizza",
		UserID:    "u1",
		Content:   "User ordered pizza for the team lunch on Friday",
		Source:    "conversation",
		CreatedAt: time.Now(),
	})

	results, err := store.SearchByText(ctx, "u1", "tense conversation with Alice about budget", 5)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}

	var found bool
	for _, r := range results {
		if strings.Contains(r.Content, "Alice") && strings.Contains(r.Content, "budget") {
			found = true
			break
		}
	}
	if !found {
		contents := make([]string, len(results))
		for i, r := range results {
			contents[i] = r.Content
		}
		t.Errorf("Alice/budget memory not found via different-wording query; got results: %v", contents)
	}
}

// TestPhase7_MemoryExtraction_RunsAsync verifies that the episodic extractor
// saves memories asynchronously without blocking the main response path.
func TestPhase7_MemoryExtraction_RunsAsync(t *testing.T) {
	store := newP7EpisodicStore()

	llm := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			{Content: `["Alice is difficult on budget topics", "Meeting ran 20 minutes over"]`},
		},
		onCall: func(_ costguard.CompletionRequest) {},
	}

	extractor := episodic.NewExtractor(llm, store, "gemma4:27b")

	start := time.Now()
	extractor.ObserveAsync(
		"u1", "sess-p7", "whatsapp",
		"Alice was really difficult in the budget meeting today",
		"I understand the meeting was challenging with Alice on budget topics.",
	)

	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("ObserveAsync blocked for %v — should return immediately", elapsed)
	}

	// Wait for the background goroutine to complete.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if store.count() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if store.count() < 2 {
		t.Errorf("expected at least 2 memories saved, got %d", store.count())
	}

	var found bool
	for _, m := range store.all() {
		if strings.Contains(m.Content, "Alice") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected memory about Alice to be saved by extractor")
	}
}

// TestPhase7_MemoryInjection_ReachesAgent verifies that when the router has an
// EpisodicStore, relevant memories are injected into the agent's system prompt.
func TestPhase7_MemoryInjection_ReachesAgent(t *testing.T) {
	store := newP7EpisodicStore()

	_ = store.SaveText(context.Background(), episodic.Memory{
		ID:        "m-alice",
		UserID:    "user-p7",
		Content:   "Alice tends to push back on large budget line items",
		Source:    "conversation",
		CreatedAt: time.Now().Add(-3 * 24 * time.Hour),
	})

	var capturedSystemPrompts []string
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("I'll help you prepare for the meeting with Alice."),
		},
		onCall: func(req costguard.CompletionRequest) {
			for _, msg := range req.Messages {
				if msg.Role == "system" {
					capturedSystemPrompts = append(capturedSystemPrompts, msg.Content)
				}
			}
		},
	}

	stack := newStack(stackConfig{
		customLLM: capLLM,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	stack.router.EpisodicStore = store

	_, _, err := stack.post(chatRequest{
		SessionID: "p7-inject-1",
		UserID:    "user-p7",
		Text:      "I have a meeting with Alice about the budget tomorrow",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// Find the agent system prompt — it contains both "Comms Agent" and the
	// memory section (or just the memory section on the second call).
	var agentPrompt string
	for _, p := range capturedSystemPrompts {
		if strings.Contains(p, "## Relevant memories") {
			agentPrompt = p
			break
		}
	}

	if !strings.Contains(agentPrompt, "Alice") {
		t.Errorf("agent system prompt should contain Alice memory; captured %d system prompts\nPrompt excerpt: %.500s",
			len(capturedSystemPrompts), agentPrompt)
	}
	if !strings.Contains(agentPrompt, "## Relevant memories") {
		t.Errorf("agent system prompt should contain memory section header; got:\n%.500s", agentPrompt)
	}
}

// TestPhase7_InboxMonitor_DeduplicatesNotifications verifies that when an email
// is already recorded in the seen store, the inbox monitor does not re-notify.
func TestPhase7_InboxMonitor_DeduplicatesNotifications(t *testing.T) {
	ctx := context.Background()
	store := newP7EpisodicStore()

	emails := []email.EmailSummary{
		{ID: "e-new", From: "alice@example.com", Subject: "Q3 budget needs your sign-off", Date: time.Now()},
		{ID: "e-seen", From: "bob@example.com", Subject: "Weekly digest", Date: time.Now()},
	}

	// Mark e-seen as already triaged — simulates a prior check.
	seen := inboxmonitor.NewMemorySeenStore()
	_ = seen.MarkSeen(ctx, "user-p7-inbox", "e-seen")

	// e-seen is filtered by dedup; only e-new reaches the LLM, aliased as "e1".
	llm := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			{Content: `["e1"]`},
		},
		onCall: func(_ costguard.CompletionRequest) {},
	}

	notifier := &p7Notifier{}
	mon := inboxmonitor.New(
		inboxmonitor.Config{
			Interval:  time.Hour,
			Channel:   types.ChannelID("discord"),
			UserID:    "user-p7-inbox",
			MaxEmails: 20,
			Model:     "test-model",
		},
		&p7MockEmailProvider{emails: emails},
		llm,
		store,
		seen,
		notifier,
	)

	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected exactly 1 notification (for e-new), got %d", notifier.count())
	}
	msg := notifier.last().Message
	if !strings.Contains(msg, "Q3 budget") {
		t.Errorf("notification should be about Q3 budget, got: %s", msg)
	}
	if strings.Contains(msg, "Weekly digest") {
		t.Errorf("already-seen email should not appear in notification")
	}
}

// TestPhase7_CalendarMonitor_EnrichesWithMemory verifies that when an event has
// Alice as an attendee and episodic memory contains context about Alice, the
// calendar brief includes that context.
func TestPhase7_CalendarMonitor_EnrichesWithMemory(t *testing.T) {
	ctx := context.Background()
	store := newP7EpisodicStore()

	_ = store.SaveText(ctx, episodic.Memory{
		ID:        "mem-alice-cal",
		UserID:    "user-p7-cal",
		Content:   "Alice tends to push back on budget line items over $50k",
		Source:    "conversation",
		CreatedAt: time.Now().Add(-7 * 24 * time.Hour),
	})

	now := time.Now()
	events := []calendar.Event{
		{
			ID:        "evt-budget",
			Title:     "Q3 Budget Review",
			Start:     now.Add(20 * time.Minute),
			End:       now.Add(80 * time.Minute),
			Attendees: []string{"alice@example.com"},
		},
	}

	notifier := &p7Notifier{}
	llm := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{},
		onCall:    func(_ costguard.CompletionRequest) {},
	}

	mon := calendarmonitor.New(
		calendarmonitor.Config{
			Interval:  5 * time.Minute,
			Lookahead: 48 * time.Hour,
			Channel:   types.ChannelID("discord"),
			UserID:    "user-p7-cal",
			Model:     "test-model",
		},
		&p7MockCalendarProvider{events: events},
		llm,
		store,
		nil,
		notifier,
	)

	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected 1 calendar brief, got %d", notifier.count())
	}
	msg := notifier.last().Message
	if !strings.Contains(msg, "Q3 Budget Review") {
		t.Errorf("brief should mention event title, got: %s", msg)
	}
	if !strings.Contains(msg, "alice") {
		t.Errorf("brief should mention attendee Alice, got: %s", msg)
	}
	if !strings.Contains(msg, "budget") {
		t.Errorf("brief should contain episodic context about Alice and budget, got: %s", msg)
	}
}
