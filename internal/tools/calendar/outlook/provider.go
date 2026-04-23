// Package outlook implements the CalendarProvider interface using the Microsoft
// Graph API for Outlook / Hotmail / Microsoft 365 calendar.
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

	agentOAuth "github.com/marcoantonios1/Agent-OS/internal/oauth"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
)

const graphBase = "https://graph.microsoft.com/v1.0/me"

var microsoftEndpoint = oauth2.Endpoint{
	AuthURL:       "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
	TokenURL:      "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
	DeviceAuthURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode",
}

var calendarScopes = []string{
	"offline_access",
	"Calendars.Read",
	"Calendars.ReadWrite",
}

// Provider implements calendar.CalendarProvider using the Microsoft Graph API.
type Provider struct {
	client *http.Client
}

// NewFromEnv creates a Provider from environment variables:
//
//	MICROSOFT_CLIENT_ID     — Azure app client ID
//	MICROSOFT_REFRESH_TOKEN — long-lived refresh token (from microsoftauth)
func NewFromEnv(ctx context.Context) (*Provider, error) {
	clientID := os.Getenv("MICROSOFT_CLIENT_ID")
	refreshToken := os.Getenv("MICROSOFT_REFRESH_TOKEN")

	if clientID == "" || refreshToken == "" {
		return nil, fmt.Errorf("outlook calendar: MICROSOFT_CLIENT_ID and MICROSOFT_REFRESH_TOKEN must be set")
	}
	return New(ctx, clientID, refreshToken, nil)
}

// New creates a Provider from explicit credentials.
// persist, if non-nil, is called whenever the OAuth server issues a new refresh token.
func New(ctx context.Context, clientID, refreshToken string, persist func(string)) (*Provider, error) {
	cfg := &oauth2.Config{
		ClientID: clientID,
		Scopes:   calendarScopes,
		Endpoint: microsoftEndpoint,
	}
	token := &oauth2.Token{RefreshToken: refreshToken, TokenType: "Bearer"}
	ts := agentOAuth.NewPersistingTokenSource(cfg.TokenSource(ctx, token), refreshToken, persist)
	return &Provider{client: oauth2.NewClient(ctx, ts)}, nil
}

// List returns all events in the [from, to) range.
func (p *Provider) List(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	filter := fmt.Sprintf("start/dateTime ge '%s' and end/dateTime le '%s'",
		from.UTC().Format(time.RFC3339),
		to.UTC().Format(time.RFC3339),
	)
	endpoint := fmt.Sprintf(
		"%s/events?$filter=%s&$select=id,subject,bodyPreview,location,start,end,attendees,isAllDay&$orderby=start/dateTime",
		graphBase, url.QueryEscape(filter),
	)

	var resp struct {
		Value []graphEvent `json:"value"`
	}
	if err := p.get(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("outlook calendar list: %w", err)
	}

	events := make([]calendar.Event, len(resp.Value))
	for i, e := range resp.Value {
		events[i] = e.toEvent()
	}
	return events, nil
}

// Read returns the full event for the given ID.
func (p *Provider) Read(ctx context.Context, id string) (*calendar.Event, error) {
	endpoint := fmt.Sprintf(
		"%s/events/%s?$select=id,subject,bodyPreview,body,location,start,end,attendees,isAllDay",
		graphBase, id,
	)
	var e graphEvent
	if err := p.get(ctx, endpoint, &e); err != nil {
		return nil, fmt.Errorf("outlook calendar read %s: %w", id, err)
	}
	ev := e.toEvent()
	return &ev, nil
}

// Create creates a new event on the default Outlook calendar.
func (p *Provider) Create(ctx context.Context, input calendar.CreateEventInput) (*calendar.Event, error) {
	attendees := make([]map[string]any, len(input.Attendees))
	for i, a := range input.Attendees {
		attendees[i] = map[string]any{
			"emailAddress": map[string]string{"address": a},
			"type":         "required",
		}
	}

	body := map[string]any{
		"subject": input.Title,
		"body": map[string]string{
			"contentType": "Text",
			"content":     input.Description,
		},
		"location":  map[string]string{"displayName": input.Location},
		"attendees": attendees,
		"isAllDay":  input.AllDay,
		"start": map[string]string{
			"dateTime": input.Start.UTC().Format(time.RFC3339),
			"timeZone": "UTC",
		},
		"end": map[string]string{
			"dateTime": input.End.UTC().Format(time.RFC3339),
			"timeZone": "UTC",
		},
	}

	var created graphEvent
	if err := p.post(ctx, graphBase+"/events", body, &created); err != nil {
		return nil, fmt.Errorf("outlook calendar create: %w", err)
	}
	e := created.toEvent()
	return &e, nil
}

// ── Graph API types ───────────────────────────────────────────────────────────

type graphEvent struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	BodyPreview string `json:"bodyPreview"`
	Location    struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	Start struct {
		DateTime string `json:"dateTime"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
	} `json:"end"`
	Attendees []struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"attendees"`
	IsAllDay bool `json:"isAllDay"`
}

func (e *graphEvent) toEvent() calendar.Event {
	ev := calendar.Event{
		ID:          e.ID,
		Title:       e.Subject,
		Description: e.BodyPreview,
		Location:    e.Location.DisplayName,
		AllDay:      e.IsAllDay,
	}
	if t, err := time.Parse(time.RFC3339, e.Start.DateTime); err == nil {
		ev.Start = t
	} else if t, err := time.Parse("2006-01-02T15:04:05.0000000", e.Start.DateTime); err == nil {
		ev.Start = t
	}
	if t, err := time.Parse(time.RFC3339, e.End.DateTime); err == nil {
		ev.End = t
	} else if t, err := time.Parse("2006-01-02T15:04:05.0000000", e.End.DateTime); err == nil {
		ev.End = t
	}
	for _, a := range e.Attendees {
		ev.Attendees = append(ev.Attendees, a.EmailAddress.Address)
	}
	return ev
}

// Update applies a partial update to an existing event via PATCH.
func (p *Provider) Update(ctx context.Context, input calendar.UpdateEventInput) (*calendar.Event, error) {
	patch := map[string]any{}
	if input.Title != "" {
		patch["subject"] = input.Title
	}
	if input.Description != "" {
		patch["body"] = map[string]string{"contentType": "Text", "content": input.Description}
	}
	if input.Location != "" {
		patch["location"] = map[string]string{"displayName": input.Location}
	}
	if !input.Start.IsZero() {
		patch["start"] = map[string]string{
			"dateTime": input.Start.UTC().Format(time.RFC3339),
			"timeZone": "UTC",
		}
	}
	if !input.End.IsZero() {
		patch["end"] = map[string]string{
			"dateTime": input.End.UTC().Format(time.RFC3339),
			"timeZone": "UTC",
		}
	}
	endpoint := fmt.Sprintf("%s/events/%s", graphBase, input.EventID)
	var updated graphEvent
	if err := p.patch(ctx, endpoint, patch, &updated); err != nil {
		return nil, fmt.Errorf("outlook calendar update %s: %w", input.EventID, err)
	}
	e := updated.toEvent()
	return &e, nil
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
			Error struct{ Message string `json:"message"` } `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return fmt.Errorf("graph API %s: %s", resp.Status, e.Error.Message)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *Provider) post(ctx context.Context, endpoint string, body any, out any) error {
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
			Error struct{ Message string `json:"message"` } `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return fmt.Errorf("graph API %s: %s", resp.Status, e.Error.Message)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *Provider) patch(ctx context.Context, endpoint string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, strings.NewReader(string(b)))
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

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Message string `json:"message"` } `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return fmt.Errorf("graph API %s: %s", resp.Status, e.Error.Message)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Compile-time check: *Provider satisfies calendar.CalendarProvider.
var _ calendar.CalendarProvider = (*Provider)(nil)
