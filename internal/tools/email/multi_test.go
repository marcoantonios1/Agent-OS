package email_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
)

// ── mock ──────────────────────────────────────────────────────────────────────

type mockEmailProvider struct {
	summaries []email.EmailSummary
	email     *email.Email
	draft     *email.Draft
	readErr   error
	listErr   error
	searchErr error
	sendErr   error
	// capture write calls
	sentTo      string
	sentSubject string
	sentBody    string
	drafted     bool
}

func (m *mockEmailProvider) List(_ context.Context, _ int) ([]email.EmailSummary, error) {
	return m.summaries, m.listErr
}
func (m *mockEmailProvider) Search(_ context.Context, _ string) ([]email.EmailSummary, error) {
	return m.summaries, m.searchErr
}
func (m *mockEmailProvider) Read(_ context.Context, _ string) (*email.Email, error) {
	return m.email, m.readErr
}
func (m *mockEmailProvider) Draft(_ context.Context, to, subject, body string) (*email.Draft, error) {
	m.drafted = true
	m.sentTo, m.sentSubject, m.sentBody = to, subject, body
	return m.draft, nil
}
func (m *mockEmailProvider) Send(_ context.Context, to, subject, body string) error {
	m.sentTo, m.sentSubject, m.sentBody = to, subject, body
	return m.sendErr
}

// ── helpers ───────────────────────────────────────────────────────────────────

func t0(offset time.Duration) time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(offset)
}

func summary(id string, age time.Duration) email.EmailSummary {
	return email.EmailSummary{ID: id, Subject: id, Date: t0(-age)}
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestMultiEmail_List_MergesSortsByDateDesc(t *testing.T) {
	a := &mockEmailProvider{summaries: []email.EmailSummary{
		summary("a1", 1*time.Hour),
		summary("a2", 3*time.Hour),
	}}
	b := &mockEmailProvider{summaries: []email.EmailSummary{
		summary("b1", 2*time.Hour),
		summary("b2", 4*time.Hour),
	}}
	mp := email.NewMultiProvider(a, a, b)

	got, err := mp.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"a1", "b1", "a2", "b2"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("[%d] ID = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestMultiEmail_List_CapsAtLimit(t *testing.T) {
	a := &mockEmailProvider{summaries: []email.EmailSummary{
		summary("a1", 1*time.Hour), summary("a2", 2*time.Hour),
	}}
	b := &mockEmailProvider{summaries: []email.EmailSummary{
		summary("b1", 3*time.Hour), summary("b2", 4*time.Hour),
	}}
	mp := email.NewMultiProvider(a, a, b)

	got, _ := mp.List(context.Background(), 3)
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestMultiEmail_List_DeduplicatesByID(t *testing.T) {
	shared := summary("dup", 1*time.Hour)
	a := &mockEmailProvider{summaries: []email.EmailSummary{shared}}
	b := &mockEmailProvider{summaries: []email.EmailSummary{shared, summary("b1", 2*time.Hour)}}
	mp := email.NewMultiProvider(a, a, b)

	got, _ := mp.List(context.Background(), 10)
	seen := map[string]int{}
	for _, e := range got {
		seen[e.ID]++
	}
	if seen["dup"] != 1 {
		t.Errorf("duplicate ID appeared %d times, want 1", seen["dup"])
	}
}

func TestMultiEmail_List_PartialFailureReturnsAvailable(t *testing.T) {
	a := &mockEmailProvider{summaries: []email.EmailSummary{summary("a1", 1*time.Hour)}}
	b := &mockEmailProvider{listErr: errors.New("network error")}
	mp := email.NewMultiProvider(a, a, b)

	got, _ := mp.List(context.Background(), 10)
	if len(got) != 1 || got[0].ID != "a1" {
		t.Errorf("expected results from healthy provider, got %v", got)
	}
}

// ── Search ────────────────────────────────────────────────────────────────────

func TestMultiEmail_Search_MergesAndSorts(t *testing.T) {
	a := &mockEmailProvider{summaries: []email.EmailSummary{summary("a1", 1*time.Hour)}}
	b := &mockEmailProvider{summaries: []email.EmailSummary{summary("b1", 30*time.Minute)}}
	mp := email.NewMultiProvider(a, a, b)

	got, err := mp.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// b1 is newer (30m ago) so comes first
	if got[0].ID != "b1" {
		t.Errorf("first result = %q, want %q", got[0].ID, "b1")
	}
}

// ── Read ──────────────────────────────────────────────────────────────────────

func TestMultiEmail_Read_FirstHitWins(t *testing.T) {
	target := &email.Email{ID: "e1", Subject: "found"}
	a := &mockEmailProvider{readErr: errors.New("not found")}
	b := &mockEmailProvider{email: target}
	mp := email.NewMultiProvider(a, a, b)

	got, err := mp.Read(context.Background(), "e1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.ID != "e1" {
		t.Errorf("ID = %q, want %q", got.ID, "e1")
	}
}

func TestMultiEmail_Read_AllFailReturnsError(t *testing.T) {
	a := &mockEmailProvider{readErr: errors.New("not found")}
	b := &mockEmailProvider{readErr: errors.New("not found")}
	mp := email.NewMultiProvider(a, a, b)

	_, err := mp.Read(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error when all providers fail, got nil")
	}
}

// ── Draft / Send (primary only) ───────────────────────────────────────────────

func TestMultiEmail_Draft_UsesPrimaryOnly(t *testing.T) {
	primary := &mockEmailProvider{draft: &email.Draft{To: "x@y.com"}}
	secondary := &mockEmailProvider{}
	mp := email.NewMultiProvider(primary, primary, secondary)

	_, err := mp.Draft(context.Background(), "x@y.com", "hi", "body")
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if !primary.drafted {
		t.Error("primary Draft not called")
	}
	if secondary.drafted {
		t.Error("secondary Draft must not be called")
	}
}

func TestMultiEmail_Send_UsesPrimaryOnly(t *testing.T) {
	primary := &mockEmailProvider{}
	secondary := &mockEmailProvider{}
	mp := email.NewMultiProvider(primary, primary, secondary)

	if err := mp.Send(context.Background(), "x@y.com", "hi", "body"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if primary.sentTo != "x@y.com" {
		t.Errorf("primary.sentTo = %q, want %q", primary.sentTo, "x@y.com")
	}
	if secondary.sentTo != "" {
		t.Error("secondary Send must not be called")
	}
}

func TestMultiEmail_Send_PropagatesPrimaryError(t *testing.T) {
	primary := &mockEmailProvider{sendErr: errors.New("smtp error")}
	mp := email.NewMultiProvider(primary, primary)

	if err := mp.Send(context.Background(), "x@y.com", "hi", "body"); err == nil {
		t.Fatal("expected error from primary Send, got nil")
	}
}
