// Package calendar implements the calendar tools used by the Comms Agent.
package calendar

import (
	"context"
	"time"
)

// Event represents a calendar event.
type Event struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Location    string    `json:"location,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Attendees   []string  `json:"attendees,omitempty"`
	AllDay      bool      `json:"all_day,omitempty"`
}

// CreateEventInput holds the fields required to create a calendar event.
type CreateEventInput struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Location    string    `json:"location,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Attendees   []string  `json:"attendees,omitempty"`
	AllDay      bool      `json:"all_day,omitempty"`
}

// CalendarProvider is the adapter interface for calendar backends.
// Swap the concrete implementation (Google Calendar, Outlook Calendar, …)
// without touching the tools.
type CalendarProvider interface {
	// List returns all events in the [from, to) range.
	List(ctx context.Context, from, to time.Time) ([]Event, error)
	// Read returns the full event for the given ID.
	Read(ctx context.Context, id string) (*Event, error)
	// Create creates a new event and returns it. The tool layer enforces the
	// approval gate before this is ever called.
	Create(ctx context.Context, event CreateEventInput) (*Event, error)
}
