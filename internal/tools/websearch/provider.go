// Package websearch defines the SearchProvider interface and SearchResult type
// used by the web_search and web_fetch tools.
package websearch

import "context"

// SearchResult is a single result returned by a web search.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchProvider is the interface every search backend must satisfy.
type SearchProvider interface {
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}
