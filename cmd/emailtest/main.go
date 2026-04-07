// emailtest is a manual test harness for the email tools.
// When GMAIL_CLIENT_ID, GMAIL_CLIENT_SECRET, and GMAIL_REFRESH_TOKEN are set
// it runs against your real Gmail account. Otherwise it falls back to a
// realistic stub so you can exercise the tools without OAuth credentials.
//
// Usage:
//
//	go run ./cmd/emailtest/                        # stub provider
//	source .env && go run ./cmd/emailtest/         # real Gmail
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email/gmail"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email/outlook"
)

// ── stub provider ─────────────────────────────────────────────────────────────

type stubProvider struct{}

var inbox = []email.EmailSummary{
	{
		ID:      "msg-001",
		From:    "alice@acme.com",
		Subject: "Q2 budget review",
		Date:    time.Now().Add(-2 * time.Hour),
		Snippet: "Hi Marco, can you take a look at the attached spreadsheet before Friday?",
	},
	{
		ID:      "msg-002",
		From:    "github-noreply@github.com",
		Subject: "[Agent-OS] PR #22 merged",
		Date:    time.Now().Add(-5 * time.Hour),
		Snippet: "marcoantonios1 merged pull request #22 — Costguard client implementation.",
	},
	{
		ID:      "msg-003",
		From:    "bob@startup.io",
		Subject: "Coffee catch-up?",
		Date:    time.Now().Add(-24 * time.Hour),
		Snippet: "Hey! Are you free next week for a quick catch-up? I'd love to hear what you're building.",
	},
	{
		ID:      "msg-004",
		From:    "billing@digitalocean.com",
		Subject: "Your invoice for April 2026",
		Date:    time.Now().Add(-48 * time.Hour),
		Snippet: "Invoice #INV-98712 — Amount due: $48.00. Due date: 15 Apr 2026.",
	},
	{
		ID:      "msg-005",
		From:    "team@notion.so",
		Subject: "Your workspace is almost full",
		Date:    time.Now().Add(-72 * time.Hour),
		Snippet: "Your Notion workspace has used 90% of its storage limit. Upgrade to continue.",
	},
}

var fullEmails = map[string]*email.Email{
	"msg-001": {
		ID:      "msg-001",
		From:    "alice@acme.com",
		To:      []string{"marco_antonios1@outlook.com"},
		Subject: "Q2 budget review",
		Date:    time.Now().Add(-2 * time.Hour),
		Body: `Hi Marco,

Can you take a look at the attached spreadsheet before our Friday call?
I've highlighted the areas where we're over budget. Let me know if you
have any questions.

Best,
Alice`,
	},
	"msg-003": {
		ID:      "msg-003",
		From:    "bob@startup.io",
		To:      []string{"marco_antonios1@outlook.com"},
		Subject: "Coffee catch-up?",
		Date:    time.Now().Add(-24 * time.Hour),
		Body: `Hey Marco!

Hope you're doing well. I'd love to grab a virtual coffee next week and
hear what you've been building. I'm free Tuesday or Thursday afternoon.

Cheers,
Bob`,
	},
}

func (s *stubProvider) List(_ context.Context, limit int) ([]email.EmailSummary, error) {
	if limit > len(inbox) {
		limit = len(inbox)
	}
	return inbox[:limit], nil
}

func (s *stubProvider) Read(_ context.Context, id string) (*email.Email, error) {
	e, ok := fullEmails[id]
	if !ok {
		return nil, fmt.Errorf("email %q not found", id)
	}
	return e, nil
}

func (s *stubProvider) Search(_ context.Context, query string) ([]email.EmailSummary, error) {
	// Simple substring match on From + Subject for the stub.
	var results []email.EmailSummary
	for _, s := range inbox {
		if contains(s.From, query) || contains(s.Subject, query) || contains(s.Snippet, query) {
			results = append(results, s)
		}
	}
	return results, nil
}

func (s *stubProvider) Draft(_ context.Context, to, subject, body string) (*email.Draft, error) {
	return &email.Draft{To: to, Subject: subject, Body: body}, nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

// ── test runner ───────────────────────────────────────────────────────────────

type result struct {
	name   string
	passed bool
	output string
	err    error
}

var pass, fail int

func run(name string, toolFn func() (string, error)) {
	output, err := toolFn()
	if err != nil {
		fmt.Printf("  ✗  %s\n     error: %v\n\n", name, err)
		fail++
		return
	}
	// Pretty-print JSON output.
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

func main() {
	ctx := context.Background()

	var p email.EmailProvider = &stubProvider{}
	mode := "stub"

	switch {
	case os.Getenv("OUTLOOK_CLIENT_ID") != "" && os.Getenv("OUTLOOK_REFRESH_TOKEN") != "":
		op, err := outlook.NewFromEnv(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Outlook setup failed: %v\nFalling back to stub provider.\n\n", err)
		} else {
			p = op
			mode = "Outlook (live) — marco_antonios1@outlook.com"
		}
	case os.Getenv("GMAIL_CLIENT_ID") != "":
		gp, err := gmail.NewFromEnv(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Gmail setup failed: %v\nFalling back to stub provider.\n\n", err)
		} else {
			p = gp
			mode = "Gmail (live) — antoniosm384@gmail.com"
		}
	}

	listTool := email.NewListTool(p)
	readTool := email.NewReadTool(p)
	searchTool := email.NewSearchTool(p)
	draftTool := email.NewDraftTool(p)

	fmt.Println("Agent OS — Email Tools Manual Test")
	fmt.Printf("Provider: %s\n\n", mode)

	// ── email_list ─────────────────────────────────────────────────────────────
	section("email_list")

	run("list 3 most recent emails", func() (string, error) {
		return listTool.Execute(ctx, mustJSON(map[string]int{"limit": 3}))
	})

	run("list with default limit (no input)", func() (string, error) {
		return listTool.Execute(ctx, mustJSON(map[string]any{}))
	})

	run("list capped at max (limit=999)", func() (string, error) {
		return listTool.Execute(ctx, mustJSON(map[string]int{"limit": 999}))
	})

	run("list with invalid JSON → expect error", func() (string, error) {
		out, err := listTool.Execute(ctx, []byte(`{bad`))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	// ── email_read ─────────────────────────────────────────────────────────────
	section("email_read")

	run("read msg-001 (budget review)", func() (string, error) {
		return readTool.Execute(ctx, mustJSON(map[string]string{"id": "msg-001"}))
	})

	run("read msg-003 (coffee catch-up)", func() (string, error) {
		return readTool.Execute(ctx, mustJSON(map[string]string{"id": "msg-003"}))
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

	// ── email_search ───────────────────────────────────────────────────────────
	section("email_search")

	run("search 'budget'", func() (string, error) {
		return searchTool.Execute(ctx, mustJSON(map[string]string{"query": "budget"}))
	})

	run("search 'github'", func() (string, error) {
		return searchTool.Execute(ctx, mustJSON(map[string]string{"query": "github"}))
	})

	run("search 'acme.com'", func() (string, error) {
		return searchTool.Execute(ctx, mustJSON(map[string]string{"query": "acme.com"}))
	})

	run("search with missing query → expect error", func() (string, error) {
		out, err := searchTool.Execute(ctx, mustJSON(map[string]string{}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	// ── email_draft ────────────────────────────────────────────────────────────
	section("email_draft")

	run("draft reply to alice (budget review)", func() (string, error) {
		return draftTool.Execute(ctx, mustJSON(map[string]string{
			"to":      "alice@acme.com",
			"subject": "Re: Q2 budget review",
			"body":    "Hi Alice,\n\nThanks for sharing. I've reviewed the spreadsheet and left comments on the highlighted rows. Happy to discuss on the Friday call.\n\nBest,\nMarco",
		}))
	})

	run("draft reply to bob (coffee catch-up)", func() (string, error) {
		return draftTool.Execute(ctx, mustJSON(map[string]string{
			"to":      "bob@startup.io",
			"subject": "Re: Coffee catch-up?",
			"body":    "Hey Bob!\n\nTuesday afternoon works great for me. How does 3 PM sound?\n\nCheers,\nMarco",
		}))
	})

	run("draft with missing 'to' field → expect error", func() (string, error) {
		out, err := draftTool.Execute(ctx, mustJSON(map[string]string{
			"subject": "Hello",
			"body":    "Body text",
		}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	run("draft with missing 'body' field → expect error", func() (string, error) {
		out, err := draftTool.Execute(ctx, mustJSON(map[string]string{
			"to":      "someone@example.com",
			"subject": "Hello",
		}))
		if err != nil {
			return fmt.Sprintf("[error correctly returned: %v]", err), nil
		}
		return out, fmt.Errorf("expected error but got none")
	})

	// ── summary ────────────────────────────────────────────────────────────────
	total := pass + fail
	fmt.Printf("────────────────────────────────────────\n")
	fmt.Printf("Results: %d passed / %d failed / %d total\n", pass, fail, total)
	if fail > 0 {
		os.Exit(1)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
