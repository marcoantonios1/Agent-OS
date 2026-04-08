// Package outlook implements the EmailProvider interface using the Microsoft
// Graph API for Outlook / Hotmail / Microsoft 365 accounts.
package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
)

const graphBase = "https://graph.microsoft.com/v1.0/me"

// microsoftEndpoint uses /consumers/ which is required for personal Microsoft
// accounts (Outlook.com, Hotmail, Live). /common/ is for work/school accounts.
var microsoftEndpoint = oauth2.Endpoint{
	AuthURL:  "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
	TokenURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
}

var scopes = []string{
	"offline_access",
	"Mail.Read",
	"Mail.ReadWrite",
}

// Provider implements email.EmailProvider backed by the Microsoft Graph API.
type Provider struct {
	client *http.Client
}

// NewFromEnv creates a Provider reading credentials from environment variables:
//
//	OUTLOOK_CLIENT_ID     — Azure app client ID
//	OUTLOOK_REFRESH_TOKEN — long-lived refresh token (obtained via outlookauth)
//	OUTLOOK_CLIENT_SECRET — optional; not required for device code flow apps
func NewFromEnv(ctx context.Context) (*Provider, error) {
	clientID := os.Getenv("OUTLOOK_CLIENT_ID")
	refreshToken := os.Getenv("OUTLOOK_REFRESH_TOKEN")

	if clientID == "" || refreshToken == "" {
		return nil, fmt.Errorf("outlook: OUTLOOK_CLIENT_ID and OUTLOOK_REFRESH_TOKEN must be set")
	}
	return New(ctx, clientID, os.Getenv("OUTLOOK_CLIENT_SECRET"), refreshToken)
}

// New creates a Provider from explicit credentials.
// clientSecret may be empty for public client (device code flow) apps.
func New(ctx context.Context, clientID, clientSecret, refreshToken string) (*Provider, error) {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
		Endpoint:     microsoftEndpoint,
	}
	token := &oauth2.Token{
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		// Zero expiry forces the token source to refresh immediately.
	}
	return &Provider{client: cfg.Client(ctx, token)}, nil
}

// List returns up to limit recent emails from the inbox.
func (p *Provider) List(ctx context.Context, limit int) ([]email.EmailSummary, error) {
	endpoint := fmt.Sprintf(
		"%s/messages?$top=%d&$select=id,subject,from,receivedDateTime,bodyPreview&$orderby=receivedDateTime desc",
		graphBase, limit,
	)
	var resp struct {
		Value []graphMessage `json:"value"`
	}
	if err := p.get(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("outlook list: %w", err)
	}
	summaries := make([]email.EmailSummary, len(resp.Value))
	for i, m := range resp.Value {
		summaries[i] = m.toSummary()
	}
	return summaries, nil
}

// Read returns the full email for the given message ID.
func (p *Provider) Read(ctx context.Context, id string) (*email.Email, error) {
	endpoint := fmt.Sprintf(
		"%s/messages/%s?$select=id,subject,from,toRecipients,receivedDateTime,body",
		graphBase, id,
	)
	var msg graphMessage
	if err := p.get(ctx, endpoint, &msg); err != nil {
		return nil, fmt.Errorf("outlook read %s: %w", id, err)
	}
	return msg.toEmail(), nil
}

// Search returns emails matching the query. Supports OData $search syntax
// as well as plain keywords (e.g. "from:alice subject:budget").
func (p *Provider) Search(ctx context.Context, query string) ([]email.EmailSummary, error) {
	endpoint := fmt.Sprintf(
		`%s/messages?$search=%s&$select=id,subject,from,receivedDateTime,bodyPreview`,
		graphBase, url.QueryEscape(`"`+query+`"`),
	)
	var resp struct {
		Value []graphMessage `json:"value"`
	}
	if err := p.get(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("outlook search %q: %w", query, err)
	}
	summaries := make([]email.EmailSummary, len(resp.Value))
	for i, m := range resp.Value {
		summaries[i] = m.toSummary()
	}
	return summaries, nil
}

// Send delivers an email immediately via the Graph API sendMail endpoint.
func (p *Provider) Send(ctx context.Context, to, subject, body string) error {
	payload := map[string]any{
		"message": map[string]any{
			"subject": subject,
			"body": map[string]string{
				"contentType": "Text",
				"content":     body,
			},
			"toRecipients": []map[string]any{
				{"emailAddress": map[string]string{"address": to}},
			},
		},
		"saveToSentItems": true,
	}
	if err := p.post(ctx, graphBase+"/sendMail", payload); err != nil {
		return fmt.Errorf("outlook send: %w", err)
	}
	return nil
}

// Draft saves a draft in Outlook. It does not send the email.
func (p *Provider) Draft(ctx context.Context, to, subject, body string) (*email.Draft, error) {
	payload := map[string]any{
		"subject": subject,
		"isDraft": true,
		"body": map[string]string{
			"contentType": "Text",
			"content":     body,
		},
		"toRecipients": []map[string]any{
			{"emailAddress": map[string]string{"address": to}},
		},
	}
	endpoint := fmt.Sprintf("%s/messages", graphBase)
	if err := p.post(ctx, endpoint, payload); err != nil {
		return nil, fmt.Errorf("outlook draft: %w", err)
	}
	return &email.Draft{To: to, Subject: subject, Body: body}, nil
}

// ── Graph API types ───────────────────────────────────────────────────────────

type graphMessage struct {
	ID                 string          `json:"id"`
	Subject            string          `json:"subject"`
	BodyPreview        string          `json:"bodyPreview"`
	ReceivedDateTime   string          `json:"receivedDateTime"`
	From               graphRecipient  `json:"from"`
	ToRecipients       []graphRecipient `json:"toRecipients"`
	Body               graphBody       `json:"body"`
}

type graphRecipient struct {
	EmailAddress struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	} `json:"emailAddress"`
}

type graphBody struct {
	Content string `json:"content"`
}

func (m *graphMessage) toSummary() email.EmailSummary {
	return email.EmailSummary{
		ID:      m.ID,
		From:    m.From.EmailAddress.Address,
		Subject: m.Subject,
		Date:    parseTime(m.ReceivedDateTime),
		Snippet: m.BodyPreview,
	}
}

func (m *graphMessage) toEmail() *email.Email {
	to := make([]string, len(m.ToRecipients))
	for i, r := range m.ToRecipients {
		to[i] = r.EmailAddress.Address
	}
	return &email.Email{
		ID:      m.ID,
		From:    m.From.EmailAddress.Address,
		To:      to,
		Subject: m.Subject,
		Date:    parseTime(m.ReceivedDateTime),
		Body:    m.Body.Content,
	}
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (p *Provider) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return fmt.Errorf("graph API %s: %s", resp.Status, e.Error.Message)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *Provider) post(ctx context.Context, endpoint string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return fmt.Errorf("graph API %s: %s", resp.Status, e.Error.Message)
	}
	return nil
}

// Compile-time check: *Provider satisfies email.EmailProvider.
var _ email.EmailProvider = (*Provider)(nil)
