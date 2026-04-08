// Package gmail implements the EmailProvider interface using the Gmail API.
package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	googlemail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
)

const gmailUser = "me"

// Provider implements email.EmailProvider backed by the Gmail API.
type Provider struct {
	svc *googlemail.Service
}

// NewFromEnv creates a Provider reading credentials from environment variables:
//
//	GMAIL_CLIENT_ID      — OAuth2 client ID
//	GMAIL_CLIENT_SECRET  — OAuth2 client secret
//	GMAIL_REFRESH_TOKEN  — long-lived refresh token (obtained via gmailauth)
func NewFromEnv(ctx context.Context) (*Provider, error) {
	clientID := os.Getenv("GMAIL_CLIENT_ID")
	clientSecret := os.Getenv("GMAIL_CLIENT_SECRET")
	refreshToken := os.Getenv("GMAIL_REFRESH_TOKEN")

	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return nil, fmt.Errorf("gmail: GMAIL_CLIENT_ID, GMAIL_CLIENT_SECRET, and GMAIL_REFRESH_TOKEN must all be set")
	}
	return New(ctx, clientID, clientSecret, refreshToken)
}

// New creates a Provider from explicit credentials.
func New(ctx context.Context, clientID, clientSecret, refreshToken string) (*Provider, error) {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			googlemail.GmailReadonlyScope,
			googlemail.GmailComposeScope,
		},
		Endpoint: google.Endpoint,
	}

	token := &oauth2.Token{
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
	}

	httpClient := cfg.Client(ctx, token)
	svc, err := googlemail.NewService(ctx,
		option.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("gmail: create service: %w", err)
	}
	return &Provider{svc: svc}, nil
}

// NewWithClient creates a Provider using a pre-configured HTTP client.
// Useful for testing with a custom transport.
func NewWithClient(ctx context.Context, client *http.Client) (*Provider, error) {
	svc, err := googlemail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("gmail: create service: %w", err)
	}
	return &Provider{svc: svc}, nil
}

// List returns up to limit recent email summaries from the inbox.
func (p *Provider) List(ctx context.Context, limit int) ([]email.EmailSummary, error) {
	resp, err := p.svc.Users.Messages.List(gmailUser).
		MaxResults(int64(limit)).
		LabelIds("INBOX").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("gmail list: %w", err)
	}

	summaries := make([]email.EmailSummary, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		msg, err := p.svc.Users.Messages.Get(gmailUser, m.Id).
			Format("METADATA").
			MetadataHeaders("Subject", "From", "Date").
			Context(ctx).
			Do()
		if err != nil {
			continue // skip unreadable messages
		}
		summaries = append(summaries, toSummary(msg))
	}
	return summaries, nil
}

// Read returns the full email for the given message ID.
func (p *Provider) Read(ctx context.Context, id string) (*email.Email, error) {
	msg, err := p.svc.Users.Messages.Get(gmailUser, id).
		Format("FULL").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("gmail read %s: %w", id, err)
	}
	return toEmail(msg), nil
}

// Search returns email summaries matching the Gmail query string.
// Supports standard Gmail search operators (from:, subject:, after:, etc.).
func (p *Provider) Search(ctx context.Context, query string) ([]email.EmailSummary, error) {
	resp, err := p.svc.Users.Messages.List(gmailUser).
		Q(query).
		MaxResults(20).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("gmail search %q: %w", query, err)
	}

	summaries := make([]email.EmailSummary, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		msg, err := p.svc.Users.Messages.Get(gmailUser, m.Id).
			Format("METADATA").
			MetadataHeaders("Subject", "From", "Date").
			Context(ctx).
			Do()
		if err != nil {
			continue
		}
		summaries = append(summaries, toSummary(msg))
	}
	return summaries, nil
}

// Send delivers an email immediately via the Gmail API.
func (p *Provider) Send(ctx context.Context, to, subject, body string) error {
	raw := buildRawMessage("me", to, subject, body)
	encoded := base64.URLEncoding.EncodeToString([]byte(raw))
	_, err := p.svc.Users.Messages.Send(gmailUser, &googlemail.Message{Raw: encoded}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("gmail send: %w", err)
	}
	return nil
}

// Draft saves a draft in Gmail. It does not send the email.
func (p *Provider) Draft(ctx context.Context, to, subject, body string) (*email.Draft, error) {
	raw := buildRawMessage("me", to, subject, body)
	encoded := base64.URLEncoding.EncodeToString([]byte(raw))

	_, err := p.svc.Users.Drafts.Create(gmailUser, &googlemail.Draft{
		Message: &googlemail.Message{Raw: encoded},
	}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gmail draft: %w", err)
	}
	return &email.Draft{To: to, Subject: subject, Body: body}, nil
}

// ── conversion helpers ────────────────────────────────────────────────────────

func toSummary(msg *googlemail.Message) email.EmailSummary {
	return email.EmailSummary{
		ID:      msg.Id,
		From:    header(msg, "From"),
		Subject: header(msg, "Subject"),
		Date:    parseDate(header(msg, "Date")),
		Snippet: msg.Snippet,
	}
}

func toEmail(msg *googlemail.Message) *email.Email {
	return &email.Email{
		ID:      msg.Id,
		From:    header(msg, "From"),
		To:      strings.Split(header(msg, "To"), ", "),
		Subject: header(msg, "Subject"),
		Date:    parseDate(header(msg, "Date")),
		Body:    extractBody(msg.Payload),
	}
}

func header(msg *googlemail.Message, name string) string {
	if msg.Payload == nil {
		return ""
	}
	for _, h := range msg.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// extractBody walks the MIME tree and returns the first text/plain part.
func extractBody(part *googlemail.MessagePart) string {
	if part == nil {
		return ""
	}
	if strings.HasPrefix(part.MimeType, "text/plain") && part.Body != nil && part.Body.Data != "" {
		decoded, err := base64.URLEncoding.DecodeString(part.Body.Data)
		if err != nil {
			return ""
		}
		return string(decoded)
	}
	for _, sub := range part.Parts {
		if body := extractBody(sub); body != "" {
			return body
		}
	}
	return ""
}

func parseDate(s string) time.Time {
	formats := []string{
		time.RFC1123Z,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func buildRawMessage(from, to, subject, body string) string {
	return fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		from, to, subject, body,
	)
}

// Compile-time check: *Provider satisfies email.EmailProvider.
var _ email.EmailProvider = (*Provider)(nil)
