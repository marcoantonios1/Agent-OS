// calendartest is a manual test harness for the calendar tools.
// When MICROSOFT_CLIENT_ID and MICROSOFT_REFRESH_TOKEN are set it uses
// your real Outlook Calendar. When GOOGLE_CLIENT_ID is set it uses Google
// Calendar. Otherwise it falls back to a realistic stub.
//
// Usage:
//
//	go run ./cmd/calendartest/                   # stub provider
//	source .env && go run ./cmd/calendartest/    # live provider
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	googlecal "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/google"
	outlookcal "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/outlook"
)

// ── stub provider ─────────────────────────────────────────────────────────────

type stubProvider struct {
	events map[string]calendar.Event
}

func newStubProvider() *stubProvider {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	events := []calendar.Event{
		{
			ID:          "evt-stub-001",
			Title:       "Team standup",
			Description: "Daily sync with the team",
			Start:       today.Add(9 * time.Hour),
			End:         today.Add(9*time.Hour + 30*time.Minute),
			Attendees:   []string{"alice@example.com", "marco_antonios1@outlook.com"},
		},
		{
			ID:       "evt-stub-002",
			Title:    "Lunch with Bob",
			Location: "Café Roma, 42 High Street",
			Start:    today.Add(12 * time.Hour),
			End:      today.Add(13 * time.Hour),
		},
		{
			ID:    "evt-stub-003",
			Title: "Sprint planning",
			Start: today.Add(48 * time.Hour),
			End:   today.Add(48*time.Hour + 2*time.Hour),
		},
		{
			ID:     "evt-stub-004",
			Title:  "Company all-hands",
			AllDay: true,
			Start:  today.Add(72 * time.Hour),
			End:    today.Add(73 * time.Hour),
		},
	}

	m := make(map[string]calendar.Event, len(events))
	for _, e := range events {
		m[e.ID] = e
	}
	return &stubProvider{events: m}
}

func (s *stubProvider) List(_ context.Context, from, to time.Time) ([]calendar.Event, error) {
	var result []calendar.Event
	for _, e := range s.events {
		if !e.Start.Before(from) && e.Start.Before(to) {
			result = append(result, e)
		}
	}
	return result, nil
}

func (s *stubProvider) Read(_ context.Context, id string) (*calendar.Event, error) {
	e, ok := s.events[id]
	if !ok {
		return nil, fmt.Errorf("event %q not found", id)
	}
	return &e, nil
}

func (s *stubProvider) Create(_ context.Context, input calendar.CreateEventInput) (*calendar.Event, error) {
	e := calendar.Event{
		ID:          fmt.Sprintf("evt-stub-%d", time.Now().UnixNano()),
		Title:       input.Title,
		Description: input.Description,
		Location:    input.Location,
		Start:       input.Start,
		End:         input.End,
		Attendees:   input.Attendees,
		AllDay:      input.AllDay,
	}
	s.events[e.ID] = e
	return &e, nil
}

func (s *stubProvider) Update(_ context.Context, input calendar.UpdateEventInput) (*calendar.Event, error) {
	e, ok := s.events[input.EventID]
	if !ok {
		return nil, fmt.Errorf("event %q not found", input.EventID)
	}
	if input.Title != "" {
		e.Title = input.Title
	}
	if input.Description != "" {
		e.Description = input.Description
	}
	if input.Location != "" {
		e.Location = input.Location
	}
	if !input.Start.IsZero() {
		e.Start = input.Start
	}
	if !input.End.IsZero() {
		e.End = input.End
	}
	s.events[e.ID] = e
	return &e, nil
}

// ── test runner ───────────────────────────────────────────────────────────────

var pass, fail int

func run(name string, toolFn func() (string, error)) {
	output, err := toolFn()
	if err != nil {
		fmt.Printf("  ✗  %s\n     error: %v\n\n", name, err)
		fail++
		return
	}
	var pretty []byte
	var v any
	if jsonErr := json.Unmarshal([]byte(output), &v); jsonErr == nil {
		pretty, _ = json.MarshalIndent(v, "     ", "  ")
	} else {
		pretty = []byte(output)
	}
	fmt.Printf("  ✓  %s\n     %s\n\n", name, string(pretty))
	pass++
}

func section(title string) {
	fmt.Printf("── %s ──────────────────────────────────\n", title)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func main() {
	ctx := context.Background()

	var p calendar.CalendarProvider = newStubProvider()
	mode := "stub"

	switch {
	case os.Getenv("MICROSOFT_CLIENT_ID") != "" && os.Getenv("MICROSOFT_REFRESH_TOKEN") != "":
		op, err := outlookcal.NewFromEnv(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Outlook Calendar setup failed: %v\nFalling back to stub provider.\n\n", err)
		} else {
			p = op
			mode = "Outlook Calendar (live)"
		}
	case os.Getenv("GOOGLE_CLIENT_ID") != "":
		gp, err := googlecal.NewFromEnv(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Google Calendar setup failed: %v\nFalling back to stub provider.\n\n", err)
		} else {
			p = gp
			mode = "Google Calendar (live)"
		}
	}

	approvalStore := approval.NewMemoryStore()
	const testSession = "calendartest-session"
	approvalCtx := approval.WithSessionID(ctx, testSession)

	listTool := calendar.NewListTool(p)
	readTool := calendar.NewReadTool(p)
	createTool := calendar.NewCreateTool(p, approvalStore)

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	nextWeek := today.Add(7 * 24 * time.Hour)
	twoWeeks := today.Add(14 * 24 * time.Hour)
	isStub := mode == "stub"

	fmt.Println("Agent OS — Calendar Tools Manual Test")
	fmt.Printf("Provider: %s\n\n", mode)

	// Fetch a real event ID for read tests when using a live provider.
	// Look up to 30 days ahead so there's a reasonable chance of finding one.
	var liveEventID string
	if !isStub {
		events, err := p.List(ctx, today.Add(-30*24*time.Hour), today.Add(30*24*time.Hour))
		if err == nil && len(events) > 0 {
			liveEventID = events[0].ID
		}
	}

	// ── calendar_list ──────────────────────────────────────────────────────────
	section("calendar_list")

	run("list events this week", func() (string, error) {
		return listTool.Execute(ctx, mustJSON(map[string]string{
			"from": today.Format(time.RFC3339),
			"to":   nextWeek.Format(time.RFC3339),
		}))
	})

	run("list events next two weeks", func() (string, error) {
		return listTool.Execute(ctx, mustJSON(map[string]string{
			"from": today.Format(time.RFC3339),
			"to":   twoWeeks.Format(time.RFC3339),
		}))
	})

	run("list with 'today' shorthand", func() (string, error) {
		return listTool.Execute(ctx, mustJSON(map[string]string{
			"from": "today",
			"to":   "today",
		}))
	})

	run("list with empty range → expect zero events", func() (string, error) {
		past := today.Add(-48 * time.Hour)
		yesterday := today.Add(-24 * time.Hour)
		return listTool.Execute(ctx, mustJSON(map[string]string{
			"from": past.Format(time.RFC3339),
			"to":   yesterday.Format(time.RFC3339),
		}))
	})

	run("list with missing 'from' → expect error", func() (string, error) {
		out, err := listTool.Execute(ctx, mustJSON(map[string]string{
			"to": nextWeek.Format(time.RFC3339),
		}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	run("list with invalid JSON → expect error", func() (string, error) {
		out, err := listTool.Execute(ctx, []byte(`{bad`))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	// ── calendar_read ──────────────────────────────────────────────────────────
	section("calendar_read")

	run("read first event (stub: standup / live: first found)", func() (string, error) {
		id := "evt-stub-001"
		if !isStub {
			if liveEventID == "" {
				return "[skipped — no events found in ±30 days to read]", nil
			}
			id = liveEventID
		}
		return readTool.Execute(ctx, mustJSON(map[string]string{"id": id}))
	})

	run("read second event (stub: lunch / live: same first event again)", func() (string, error) {
		id := "evt-stub-002"
		if !isStub {
			if liveEventID == "" {
				return "[skipped — no events found in ±30 days to read]", nil
			}
			id = liveEventID
		}
		return readTool.Execute(ctx, mustJSON(map[string]string{"id": id}))
	})

	run("read non-existent ID → expect error", func() (string, error) {
		out, err := readTool.Execute(ctx, mustJSON(map[string]string{"id": "does-not-exist"}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	run("read with missing id field → expect error", func() (string, error) {
		out, err := readTool.Execute(ctx, mustJSON(map[string]string{}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	// ── calendar_create ────────────────────────────────────────────────────────
	section("calendar_create")

	run("create without prior approval → returns pending_approval", func() (string, error) {
		result, err := createTool.Execute(approvalCtx, mustJSON(map[string]any{
			"title": "Sneaky meeting",
			"start": nextWeek.Add(10 * time.Hour).Format(time.RFC3339),
			"end":   nextWeek.Add(11 * time.Hour).Format(time.RFC3339),
		}))
		if err != nil {
			return "", err
		}
		var resp map[string]any
		json.Unmarshal([]byte(result), &resp)
		if resp["status"] != "pending_approval" {
			return result, fmt.Errorf("expected status=pending_approval, got %v", resp["status"])
		}
		return fmt.Sprintf("[pending correctly returned — action_id: %v]", resp["action_id"]), nil
	})

	run("create with end before start → expect error", func() (string, error) {
		out, err := createTool.Execute(approvalCtx, mustJSON(map[string]any{
			"title": "Bad times event",
			"start": nextWeek.Add(11 * time.Hour).Format(time.RFC3339),
			"end":   nextWeek.Add(10 * time.Hour).Format(time.RFC3339),
		}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	run("create with missing title → expect error", func() (string, error) {
		out, err := createTool.Execute(approvalCtx, mustJSON(map[string]any{
			"start": nextWeek.Add(10 * time.Hour).Format(time.RFC3339),
			"end":   nextWeek.Add(11 * time.Hour).Format(time.RFC3339),
		}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	run("create proceeds after approval is granted (stub only)", func() (string, error) {
		if mode != "stub" {
			return "[skipped — live provider: no test events created]", nil
		}
		input := mustJSON(map[string]any{
			"title":       "Test event from calendartest",
			"description": "Created by the calendartest harness",
			"location":    "Virtual",
			"start":       nextWeek.Add(14 * time.Hour).Format(time.RFC3339),
			"end":         nextWeek.Add(15 * time.Hour).Format(time.RFC3339),
			"attendees":   []string{"alice@example.com"},
		})
		// First call registers the pending action.
		result, err := createTool.Execute(approvalCtx, input)
		if err != nil {
			return "", err
		}
		var pending map[string]any
		json.Unmarshal([]byte(result), &pending)
		actionID, _ := pending["action_id"].(string)
		// Simulate user confirmation: grant the action.
		approvalStore.Grant(testSession, actionID)
		// Second call with identical params should now create the event.
		return createTool.Execute(approvalCtx, input)
	})

	// ── summary ────────────────────────────────────────────────────────────────
	total := pass + fail
	fmt.Printf("────────────────────────────────────────\n")
	fmt.Printf("Results: %d passed / %d failed / %d total\n", pass, fail, total)
	if fail > 0 {
		os.Exit(1)
	}
}
