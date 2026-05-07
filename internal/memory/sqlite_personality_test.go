package memory_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestPersonalityStore(t *testing.T) *memory.SQLitePersonalityStore {
	t.Helper()
	db, err := memory.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return memory.NewSQLitePersonalityStore(db)
}

// ── SQLitePersonalityStore ────────────────────────────────────────────────────

func TestSQLitePersonality_NewUser_ReturnsEmptyProfile(t *testing.T) {
	store := newTestPersonalityStore(t)

	profile, err := store.GetPersonality("nobody")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if profile == nil {
		t.Fatal("expected non-nil profile for new user")
	}
	if profile.UserID != "nobody" {
		t.Errorf("UserID = %q, want %q", profile.UserID, "nobody")
	}
	if len(profile.Signals) != 0 {
		t.Errorf("expected 0 signals for new user, got %d", len(profile.Signals))
	}
}

func TestSQLitePersonality_UpsertSignal_InsertsNewSignal(t *testing.T) {
	store := newTestPersonalityStore(t)

	err := store.UpsertSignal("u1", sessions.PersonalitySignal{
		Key:   sessions.SignalResponseLength,
		Value: "brief",
	})
	if err != nil {
		t.Fatalf("UpsertSignal: %v", err)
	}

	profile, err := store.GetPersonality("u1")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if len(profile.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(profile.Signals))
	}
	sig := profile.Signals[0]
	if sig.Key != sessions.SignalResponseLength {
		t.Errorf("Key = %q, want %q", sig.Key, sessions.SignalResponseLength)
	}
	if sig.Value != "brief" {
		t.Errorf("Value = %q, want %q", sig.Value, "brief")
	}
	if sig.Count != 1 {
		t.Errorf("Count = %d, want 1", sig.Count)
	}
}

func TestSQLitePersonality_UpsertSignal_IncrementsCountAndConfidence(t *testing.T) {
	store := newTestPersonalityStore(t)

	sig := sessions.PersonalitySignal{Key: sessions.SignalTechnicalDepth, Value: "high"}
	for i := 0; i < 5; i++ {
		if err := store.UpsertSignal("u1", sig); err != nil {
			t.Fatalf("UpsertSignal iteration %d: %v", i, err)
		}
	}

	profile, err := store.GetPersonality("u1")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if len(profile.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(profile.Signals))
	}
	got := profile.Signals[0]
	if got.Count != 5 {
		t.Errorf("Count = %d, want 5", got.Count)
	}
	// confidence = min(5/10, 1.0) = 0.5
	if got.Confidence < 0.49 || got.Confidence > 0.51 {
		t.Errorf("Confidence = %.3f, want ~0.5", got.Confidence)
	}
}

func TestSQLitePersonality_UpsertSignal_ConfidenceCapsAt1(t *testing.T) {
	store := newTestPersonalityStore(t)

	sig := sessions.PersonalitySignal{Key: sessions.SignalUrgencyPattern, Value: "high"}
	for i := 0; i < 15; i++ {
		if err := store.UpsertSignal("u1", sig); err != nil {
			t.Fatalf("UpsertSignal: %v", err)
		}
	}

	profile, _ := store.GetPersonality("u1")
	got := profile.Signals[0]
	if got.Count != 15 {
		t.Errorf("Count = %d, want 15", got.Count)
	}
	if got.Confidence != 1.0 {
		t.Errorf("Confidence = %.3f, want 1.0 (capped)", got.Confidence)
	}
}

func TestSQLitePersonality_UpsertSignal_UpdatesValue(t *testing.T) {
	store := newTestPersonalityStore(t)

	store.UpsertSignal("u1", sessions.PersonalitySignal{Key: sessions.SignalWorkingHours, Value: "morning"}) //nolint:errcheck
	store.UpsertSignal("u1", sessions.PersonalitySignal{Key: sessions.SignalWorkingHours, Value: "evening"}) //nolint:errcheck

	profile, _ := store.GetPersonality("u1")
	if profile.Signals[0].Value != "evening" {
		t.Errorf("Value after update = %q, want %q", profile.Signals[0].Value, "evening")
	}
}

func TestSQLitePersonality_UpsertSignal_UpdatesLastSeen(t *testing.T) {
	store := newTestPersonalityStore(t)

	before := time.Now().Add(-time.Second)
	store.UpsertSignal("u1", sessions.PersonalitySignal{Key: sessions.SignalHumorTolerance, Value: "light"}) //nolint:errcheck

	profile, _ := store.GetPersonality("u1")
	if !profile.Signals[0].LastSeen.After(before) {
		t.Error("LastSeen should be updated to now on upsert")
	}
}

func TestSQLitePersonality_MultipleSignals(t *testing.T) {
	store := newTestPersonalityStore(t)

	signals := []sessions.PersonalitySignal{
		{Key: sessions.SignalResponseLength, Value: "brief"},
		{Key: sessions.SignalTechnicalDepth, Value: "medium"},
		{Key: sessions.SignalCommunicationStyle, Value: "casual"},
	}
	for _, sig := range signals {
		if err := store.UpsertSignal("u1", sig); err != nil {
			t.Fatalf("UpsertSignal %s: %v", sig.Key, err)
		}
	}

	profile, err := store.GetPersonality("u1")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if len(profile.Signals) != 3 {
		t.Errorf("expected 3 signals, got %d", len(profile.Signals))
	}
}

func TestSQLitePersonality_SavePersonality_RoundTrip(t *testing.T) {
	store := newTestPersonalityStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	profile := &sessions.PersonalityProfile{
		UserID: "u1",
		Signals: []sessions.PersonalitySignal{
			{Key: sessions.SignalResponseLength, Value: "detailed", Confidence: 0.8, Count: 8, LastSeen: now},
		},
	}
	if err := store.SavePersonality(profile); err != nil {
		t.Fatalf("SavePersonality: %v", err)
	}

	got, err := store.GetPersonality("u1")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if len(got.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(got.Signals))
	}
	s := got.Signals[0]
	if s.Count != 8 || s.Confidence != 0.8 || s.Value != "detailed" {
		t.Errorf("signal mismatch: %+v", s)
	}
}

func TestSQLitePersonality_IsolatedByUser(t *testing.T) {
	store := newTestPersonalityStore(t)

	store.UpsertSignal("alice", sessions.PersonalitySignal{Key: sessions.SignalResponseLength, Value: "brief"})     //nolint:errcheck
	store.UpsertSignal("bob", sessions.PersonalitySignal{Key: sessions.SignalResponseLength, Value: "verbose"})     //nolint:errcheck

	alice, _ := store.GetPersonality("alice")
	bob, _ := store.GetPersonality("bob")

	if alice.Signals[0].Value != "brief" {
		t.Errorf("alice value = %q, want brief", alice.Signals[0].Value)
	}
	if bob.Signals[0].Value != "verbose" {
		t.Errorf("bob value = %q, want verbose", bob.Signals[0].Value)
	}
}

// ── MemoryPersonalityStore ────────────────────────────────────────────────────

func TestMemoryPersonality_NewUser_ReturnsEmptyProfile(t *testing.T) {
	store := memory.NewPersonalityStore()

	profile, err := store.GetPersonality("nobody")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if profile == nil {
		t.Fatal("expected non-nil profile")
	}
	if len(profile.Signals) != 0 {
		t.Errorf("expected 0 signals, got %d", len(profile.Signals))
	}
}

func TestMemoryPersonality_UpsertSignal_IncrementsCount(t *testing.T) {
	store := memory.NewPersonalityStore()

	sig := sessions.PersonalitySignal{Key: sessions.SignalTechnicalDepth, Value: "high"}
	for i := 0; i < 3; i++ {
		store.UpsertSignal("u1", sig) //nolint:errcheck
	}

	profile, _ := store.GetPersonality("u1")
	if len(profile.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(profile.Signals))
	}
	if profile.Signals[0].Count != 3 {
		t.Errorf("Count = %d, want 3", profile.Signals[0].Count)
	}
	// confidence = min(3/10, 1.0) = 0.3
	if profile.Signals[0].Confidence < 0.29 || profile.Signals[0].Confidence > 0.31 {
		t.Errorf("Confidence = %.3f, want ~0.3", profile.Signals[0].Confidence)
	}
}

func TestMemoryPersonality_UpsertSignal_ConfidenceCapsAt1(t *testing.T) {
	store := memory.NewPersonalityStore()

	sig := sessions.PersonalitySignal{Key: sessions.SignalUrgencyPattern, Value: "low"}
	for i := 0; i < 20; i++ {
		store.UpsertSignal("u1", sig) //nolint:errcheck
	}

	profile, _ := store.GetPersonality("u1")
	if profile.Signals[0].Confidence != 1.0 {
		t.Errorf("Confidence = %.3f, want 1.0", profile.Signals[0].Confidence)
	}
}
