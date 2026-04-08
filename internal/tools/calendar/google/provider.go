// Package google implements the CalendarProvider interface using the Google
// Calendar API.
package google

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	googlecal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"

	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
)

const calendarID = "primary"

var calendarScopes = []string{
	googlecal.CalendarReadonlyScope,
	googlecal.CalendarEventsScope,
}

// Provider implements calendar.CalendarProvider using the Google Calendar API.
type Provider struct {
	svc *googlecal.Service
}

// NewFromEnv creates a Provider from environment variables:
//
//	GOOGLE_CAL_CLIENT_ID      — OAuth2 client ID
//	GOOGLE_CAL_CLIENT_SECRET  — OAuth2 client secret
//	GOOGLE_CAL_REFRESH_TOKEN  — long-lived refresh token (from googlecalauth)
func NewFromEnv(ctx context.Context) (*Provider, error) {
	clientID := os.Getenv("GOOGLE_CAL_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CAL_CLIENT_SECRET")
	refreshToken := os.Getenv("GOOGLE_CAL_REFRESH_TOKEN")

	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return nil, fmt.Errorf("google calendar: GOOGLE_CAL_CLIENT_ID, GOOGLE_CAL_CLIENT_SECRET, and GOOGLE_CAL_REFRESH_TOKEN must all be set")
	}
	return New(ctx, clientID, clientSecret, refreshToken)
}

// New creates a Provider from explicit credentials.
func New(ctx context.Context, clientID, clientSecret, refreshToken string) (*Provider, error) {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       calendarScopes,
		Endpoint:     googleoauth.Endpoint,
	}
	token := &oauth2.Token{RefreshToken: refreshToken, TokenType: "Bearer"}
	svc, err := googlecal.NewService(ctx, option.WithHTTPClient(cfg.Client(ctx, token)))
	if err != nil {
		return nil, fmt.Errorf("google calendar: create service: %w", err)
	}
	return &Provider{svc: svc}, nil
}

// NewWithClient creates a Provider using a pre-configured HTTP client (useful for testing).
func NewWithClient(ctx context.Context, client *http.Client) (*Provider, error) {
	svc, err := googlecal.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("google calendar: create service: %w", err)
	}
	return &Provider{svc: svc}, nil
}

// List returns all events in the [from, to) range from the primary calendar.
func (p *Provider) List(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	resp, err := p.svc.Events.List(calendarID).
		TimeMin(from.Format(time.RFC3339)).
		TimeMax(to.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar list: %w", err)
	}

	events := make([]calendar.Event, 0, len(resp.Items))
	for _, item := range resp.Items {
		events = append(events, toEvent(item))
	}
	return events, nil
}

// Read returns the full event for the given ID from the primary calendar.
func (p *Provider) Read(ctx context.Context, id string) (*calendar.Event, error) {
	item, err := p.svc.Events.Get(calendarID, id).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar read %s: %w", id, err)
	}
	e := toEvent(item)
	return &e, nil
}

// Create creates a new event on the primary calendar.
func (p *Provider) Create(ctx context.Context, input calendar.CreateEventInput) (*calendar.Event, error) {
	gEvent := &googlecal.Event{
		Summary:     input.Title,
		Description: input.Description,
		Location:    input.Location,
	}

	if input.AllDay {
		gEvent.Start = &googlecal.EventDateTime{Date: input.Start.Format("2006-01-02")}
		gEvent.End = &googlecal.EventDateTime{Date: input.End.Format("2006-01-02")}
	} else {
		gEvent.Start = &googlecal.EventDateTime{DateTime: input.Start.Format(time.RFC3339)}
		gEvent.End = &googlecal.EventDateTime{DateTime: input.End.Format(time.RFC3339)}
	}

	for _, a := range input.Attendees {
		gEvent.Attendees = append(gEvent.Attendees, &googlecal.EventAttendee{Email: a})
	}

	created, err := p.svc.Events.Insert(calendarID, gEvent).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar create: %w", err)
	}
	e := toEvent(created)
	return &e, nil
}

// ── conversion helpers ────────────────────────────────────────────────────────

func toEvent(item *googlecal.Event) calendar.Event {
	e := calendar.Event{
		ID:          item.Id,
		Title:       item.Summary,
		Description: item.Description,
		Location:    item.Location,
	}
	for _, a := range item.Attendees {
		e.Attendees = append(e.Attendees, a.Email)
	}
	if item.Start != nil {
		if item.Start.DateTime != "" {
			e.Start, _ = time.Parse(time.RFC3339, item.Start.DateTime)
		} else if item.Start.Date != "" {
			e.Start, _ = time.Parse("2006-01-02", item.Start.Date)
			e.AllDay = true
		}
	}
	if item.End != nil {
		if item.End.DateTime != "" {
			e.End, _ = time.Parse(time.RFC3339, item.End.DateTime)
		} else if item.End.Date != "" {
			e.End, _ = time.Parse("2006-01-02", item.End.Date)
		}
	}
	return e
}

// Compile-time check: *Provider satisfies calendar.CalendarProvider.
var _ calendar.CalendarProvider = (*Provider)(nil)
