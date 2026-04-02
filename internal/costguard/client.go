package costguard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	maxAttempts    = 3
	initialBackoff = 200 * time.Millisecond
)

// Client implements LLMClient by routing requests through the Costguard gateway.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	log        *slog.Logger
}

// NewFromEnv creates a Client reading COSTGUARD_URL and COSTGUARD_API_KEY from
// the environment. It panics if COSTGUARD_URL is not set.
func NewFromEnv() *Client {
	url := os.Getenv("COSTGUARD_URL")
	if url == "" {
		panic("COSTGUARD_URL environment variable is not set")
	}
	return New(url, os.Getenv("COSTGUARD_API_KEY"))
}

// New creates a Client with the given base URL and API key.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		log:        slog.Default(),
	}
}

// apiRequest is the JSON body sent to the Costguard gateway.
type apiRequest struct {
	Model     string         `json:"model"`
	Messages  []apiMessage   `json:"messages"`
	Tools     []ToolDefinition `json:"tools,omitempty"`
	MaxTokens int            `json:"max_tokens,omitempty"`
	Stream    bool           `json:"stream,omitempty"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// apiResponse is the JSON body returned for non-streaming requests.
type apiResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// sseChunk is one JSON frame sent over an SSE stream.
type sseChunk struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
}

// Complete performs a single-shot completion with up to maxAttempts retries.
func (c *Client) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	body := c.buildRequest(req, false)

	var (
		resp    *http.Response
		lastErr error
	)
	for attempt := range maxAttempts {
		if attempt > 0 {
			backoff := initialBackoff * (1 << (attempt - 1))
			c.log.InfoContext(ctx, "retrying request", "attempt", attempt+1, "backoff", backoff)
			select {
			case <-ctx.Done():
				return CompletionResponse{}, ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, lastErr = c.doPost(ctx, "/v1/complete", body)
		if lastErr == nil && !isRetryable(resp.StatusCode) {
			break
		}
		if resp != nil {
			resp.Body.Close()
			c.log.WarnContext(ctx, "retryable error", "status", resp.StatusCode, "attempt", attempt+1)
		} else {
			c.log.WarnContext(ctx, "request error", "error", lastErr, "attempt", attempt+1)
		}
	}
	if lastErr != nil {
		return CompletionResponse{}, fmt.Errorf("costguard: all %d attempts failed: %w", maxAttempts, lastErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, fmt.Errorf("costguard: unexpected status %d", resp.StatusCode)
	}

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("costguard: decode response: %w", err)
	}

	c.log.InfoContext(ctx, "completion done",
		"model", req.Model,
		"prompt_tokens", apiResp.Usage.PromptTokens,
		"completion_tokens", apiResp.Usage.CompletionTokens,
	)

	return CompletionResponse{
		Content:   apiResp.Content,
		ToolCalls: apiResp.ToolCalls,
		Usage: Usage{
			PromptTokens:     apiResp.Usage.PromptTokens,
			CompletionTokens: apiResp.Usage.CompletionTokens,
			TotalTokens:      apiResp.Usage.TotalTokens,
		},
	}, nil
}

// Stream performs a streaming completion and returns a channel of chunks.
// The caller must drain the channel until it is closed.
func (c *Client) Stream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error) {
	body := c.buildRequest(req, true)

	resp, err := c.doPost(ctx, "/v1/stream", body)
	if err != nil {
		return nil, fmt.Errorf("costguard: stream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("costguard: stream unexpected status %d", resp.StatusCode)
	}

	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				ch <- StreamChunk{Done: true}
				return
			}
			var chunk sseChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				ch <- StreamChunk{Error: fmt.Errorf("costguard: decode chunk: %w", err)}
				return
			}
			select {
			case ch <- StreamChunk{Content: chunk.Content, Done: chunk.Done}:
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err()}
				return
			}
			if chunk.Done {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("costguard: read stream: %w", err)}
		}
	}()

	return ch, nil
}

func (c *Client) buildRequest(req CompletionRequest, stream bool) apiRequest {
	msgs := make([]apiMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = apiMessage{Role: m.Role, Content: m.Content}
	}
	return apiRequest{
		Model:     req.Model,
		Messages:  msgs,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		Stream:    stream,
	}
}

func (c *Client) doPost(ctx context.Context, path string, body apiRequest) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	c.log.InfoContext(ctx, "costguard request", "path", path, "model", body.Model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	return c.httpClient.Do(httpReq)
}

// isRetryable returns true for status codes that warrant a retry.
func isRetryable(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}
