// Package email implements the email tools used by the Comms Agent.
// No tool in this package sends email autonomously — sending is gated behind
// an explicit approval step (see Issue #11).
package email

import (
	"context"
	"time"
)

// EmailSummary is a lightweight representation of an email, suitable for lists.
type EmailSummary struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
	Snippet string    `json:"snippet"`
}

// Email is the full representation of a single email message.
type Email struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	To      []string  `json:"to"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
	Body    string    `json:"body"`
}

// Draft holds a composed email that has not been sent.
type Draft struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// EmailProvider is the adapter interface for email backends.
// Swap the concrete implementation (Gmail, IMAP, …) without touching the tools.
type EmailProvider interface {
	// List returns up to limit recent email summaries.
	List(ctx context.Context, limit int) ([]EmailSummary, error)
	// Read returns the full email for the given ID.
	Read(ctx context.Context, id string) (*Email, error)
	// Search returns email summaries matching the query string.
	Search(ctx context.Context, query string) ([]EmailSummary, error)
	// Draft composes an email and returns it as a Draft. It must not send.
	Draft(ctx context.Context, to, subject, body string) (*Draft, error)
}
