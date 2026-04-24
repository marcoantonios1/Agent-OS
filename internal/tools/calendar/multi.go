package calendar

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MultiProvider fans read operations out to all configured providers and merges
// the results. Write operations (Create, Update) go to the primary provider only.
//
// Google is the primary when both Google and Microsoft are configured.
type MultiProvider struct {
	primary   CalendarProvider
	providers []CalendarProvider
}

// NewMultiProvider returns a MultiProvider. primary must not be nil and must
// also appear in providers so List includes its results.
func NewMultiProvider(primary CalendarProvider, providers ...CalendarProvider) *MultiProvider {
	return &MultiProvider{primary: primary, providers: providers}
}

// List fans out to all providers in parallel, merges by start time ascending,
// and deduplicates by ID.
func (m *MultiProvider) List(ctx context.Context, from, to time.Time) ([]Event, error) {
	type result struct {
		events []Event
		err    error
	}

	results := make([]result, len(m.providers))
	var wg sync.WaitGroup
	for i, p := range m.providers {
		wg.Add(1)
		go func(idx int, prov CalendarProvider) {
			defer wg.Done()
			evs, err := prov.List(ctx, from, to)
			results[idx] = result{events: evs, err: err}
		}(i, p)
	}
	wg.Wait()

	seen := make(map[string]struct{})
	var merged []Event
	for _, r := range results {
		if r.err != nil {
			continue // partial failure: include successful providers' results
		}
		for _, e := range r.events {
			if _, dup := seen[e.ID]; dup {
				continue
			}
			seen[e.ID] = struct{}{}
			merged = append(merged, e)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Start.Before(merged[j].Start)
	})
	return merged, nil
}

// Read tries each provider in order and returns the first hit.
func (m *MultiProvider) Read(ctx context.Context, id string) (*Event, error) {
	var lastErr error
	for _, p := range m.providers {
		e, err := p.Read(ctx, id)
		if err == nil {
			return e, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("event %q not found in any provider", id)
}

// Create delegates to the primary provider only.
func (m *MultiProvider) Create(ctx context.Context, event CreateEventInput) (*Event, error) {
	return m.primary.Create(ctx, event)
}

// Update delegates to the primary provider only.
func (m *MultiProvider) Update(ctx context.Context, input UpdateEventInput) (*Event, error) {
	return m.primary.Update(ctx, input)
}
