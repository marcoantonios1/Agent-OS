// Package brave implements SearchProvider backed by the Brave Search API.
package brave

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
)

const apiEndpoint = "https://api.search.brave.com/res/v1/web/search"

// Provider calls the Brave Search API.
type Provider struct {
	apiKey string
	client *http.Client
}

// New creates a Brave Provider with the given API key.
func New(apiKey string) *Provider {
	return &Provider{
		apiKey: apiKey,
		client: &http.Client{},
	}
}

// Search performs a Brave web search and returns up to limit results.
func (p *Provider) Search(ctx context.Context, query string, limit int) ([]websearch.SearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}

	q := url.Values{}
	q.Set("q", query)
	q.Set("count", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return nil, fmt.Errorf("brave: API error %s: %s", resp.Status, e.Error.Message)
	}

	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("brave: decode response: %w", err)
	}

	results := make([]websearch.SearchResult, 0, len(body.Web.Results))
	for _, r := range body.Web.Results {
		results = append(results, websearch.SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
		})
	}
	return results, nil
}

// Compile-time check: *Provider satisfies websearch.SearchProvider.
var _ websearch.SearchProvider = (*Provider)(nil)
