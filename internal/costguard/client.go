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

	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const (
	maxAttempts    = 3
	initialBackoff = 200 * time.Millisecond
)

// Client implements LLMClient by routing requests through the Costguard gateway
// using the OpenAI-compatible /v1/chat/completions endpoint.
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

// ── OpenAI wire types ─────────────────────────────────────────────────────────

type oaiRequest struct {
	Model     string       `json:"model"`
	Messages  []oaiMessage `json:"messages"`
	Tools     []oaiTool    `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens,omitempty"`
	Stream    bool         `json:"stream,omitempty"`
}

// oaiMessage uses a pointer for Content so assistant turns with only tool_calls
// can set content to null (omitempty would hide a legitimate empty string).
type oaiMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type oaiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function oaiFuncCall `json:"function"`
}

type oaiFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiResponse struct {
	Choices []struct {
		Message oaiMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// oaiStreamDelta is one JSON frame in an OpenAI SSE stream.
type oaiStreamDelta struct {
	Choices []struct {
		Delta struct {
			Content   string        `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// ── Complete ──────────────────────────────────────────────────────────────────

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

		resp, lastErr = c.doPost(ctx, "/v1/chat/completions", body)
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

	var apiResp oaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("costguard: decode response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("costguard: response has no choices")
	}

	msg := apiResp.Choices[0].Message
	content := ""
	if msg.Content != nil {
		content = *msg.Content
	}

	toolCalls := make([]types.ToolCall, len(msg.ToolCalls))
	for i, tc := range msg.ToolCalls {
		toolCalls[i] = types.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}
	}

	c.log.InfoContext(ctx, "completion done",
		"model", req.Model,
		"prompt_tokens", apiResp.Usage.PromptTokens,
		"completion_tokens", apiResp.Usage.CompletionTokens,
	)

	return CompletionResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage: Usage{
			PromptTokens:     apiResp.Usage.PromptTokens,
			CompletionTokens: apiResp.Usage.CompletionTokens,
			TotalTokens:      apiResp.Usage.TotalTokens,
		},
	}, nil
}

// ── Stream ────────────────────────────────────────────────────────────────────

// Stream performs a streaming completion and returns a channel of chunks.
// The caller must drain the channel until it is closed.
func (c *Client) Stream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error) {
	body := c.buildRequest(req, true)

	resp, err := c.doPost(ctx, "/v1/chat/completions", body)
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
			var delta oaiStreamDelta
			if err := json.Unmarshal([]byte(payload), &delta); err != nil {
				ch <- StreamChunk{Error: fmt.Errorf("costguard: decode chunk: %w", err)}
				return
			}
			if len(delta.Choices) == 0 {
				continue
			}
			content := delta.Choices[0].Delta.Content
			done := delta.Choices[0].FinishReason == "stop"
			select {
			case ch <- StreamChunk{Content: content, Done: done}:
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err()}
				return
			}
			if done {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("costguard: read stream: %w", err)}
		}
	}()

	return ch, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (c *Client) buildRequest(req CompletionRequest, stream bool) oaiRequest {
	msgs := make([]oaiMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, toOAIMessage(m))
	}

	var tools []oaiTool
	for _, td := range req.Tools {
		tools = append(tools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
		})
	}

	return oaiRequest{
		Model:     req.Model,
		Messages:  msgs,
		Tools:     tools,
		MaxTokens: req.MaxTokens,
		Stream:    stream,
	}
}

func toOAIMessage(t types.ConversationTurn) oaiMessage {
	msg := oaiMessage{Role: t.Role}

	switch t.Role {
	case "assistant":
		if len(t.ToolCalls) > 0 {
			// Assistant turn with tool calls: content is null, tool_calls is set.
			msg.ToolCalls = make([]oaiToolCall, len(t.ToolCalls))
			for i, tc := range t.ToolCalls {
				msg.ToolCalls[i] = oaiToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: oaiFuncCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				}
			}
		} else {
			msg.Content = &t.Content
		}
	case "tool":
		msg.ToolCallID = t.ToolCallID
		msg.Content = &t.Content
	default:
		// system, user
		msg.Content = &t.Content
	}

	return msg
}

func (c *Client) doPost(ctx context.Context, path string, body oaiRequest) (*http.Response, error) {
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
