package email

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MultiProvider fans read operations out to all configured providers and merges
// the results. Write operations (Draft, Send) go to the primary provider only.
//
// Google is the primary when both Google and Microsoft are configured.
type MultiProvider struct {
	primary   EmailProvider
	providers []EmailProvider
}

// NewMultiProvider returns a MultiProvider. primary must not be nil and must
// also appear in providers so List/Search include its results.
func NewMultiProvider(primary EmailProvider, providers ...EmailProvider) *MultiProvider {
	return &MultiProvider{primary: primary, providers: providers}
}

// List fans out to all providers in parallel, merges by date descending, and
// caps the combined result at limit.
func (m *MultiProvider) List(ctx context.Context, limit int) ([]EmailSummary, error) {
	results, _ := fanOutList(ctx, m.providers, func(p EmailProvider) ([]EmailSummary, error) {
		return p.List(ctx, limit)
	})
	merged := mergeByDateDesc(results)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// Search fans out to all providers in parallel and merges results by date descending.
func (m *MultiProvider) Search(ctx context.Context, query string) ([]EmailSummary, error) {
	results, _ := fanOutList(ctx, m.providers, func(p EmailProvider) ([]EmailSummary, error) {
		return p.Search(ctx, query)
	})
	return mergeByDateDesc(results), nil
}

// Read tries each provider in order and returns the first hit.
func (m *MultiProvider) Read(ctx context.Context, id string) (*Email, error) {
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
	return nil, fmt.Errorf("email %q not found in any provider", id)
}

// Draft delegates to the primary provider only.
func (m *MultiProvider) Draft(ctx context.Context, to, subject, body string) (*Draft, error) {
	return m.primary.Draft(ctx, to, subject, body)
}

// Send delegates to the primary provider only.
func (m *MultiProvider) Send(ctx context.Context, to, subject, body string) error {
	return m.primary.Send(ctx, to, subject, body)
}

// ── helpers ───────────────────────────────────────────────────────────────────

type listResult struct {
	items []EmailSummary
	err   error
}

// fanOutList calls fetch on every provider concurrently and collects results.
// It always returns all successful slices; errors are returned as a slice so
// callers can decide whether to surface them.
func fanOutList(ctx context.Context, providers []EmailProvider, fetch func(EmailProvider) ([]EmailSummary, error)) ([][]EmailSummary, []error) {
	results := make([]listResult, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(idx int, prov EmailProvider) {
			defer wg.Done()
			items, err := fetch(prov)
			results[idx] = listResult{items: items, err: err}
		}(i, p)
	}
	wg.Wait()

	var (
		allItems [][]EmailSummary
		errs     []error
	)
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
		} else {
			allItems = append(allItems, r.items)
		}
	}
	return allItems, errs
}

// mergeByDateDesc merges multiple EmailSummary slices, deduplicates by ID, and
// sorts the result newest-first.
func mergeByDateDesc(slices [][]EmailSummary) []EmailSummary {
	seen := make(map[string]struct{})
	var merged []EmailSummary
	for _, slice := range slices {
		for _, e := range slice {
			if _, dup := seen[e.ID]; dup {
				continue
			}
			seen[e.ID] = struct{}{}
			merged = append(merged, e)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Date.After(merged[j].Date)
	})
	return merged
}
