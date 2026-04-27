// Package types defines the shared data structures used across all agents,
// channels, the router, and tools within Agent OS.
package types

import (
	"context"
	"time"
)

// SubAgentCaller lets an agent dispatch a task to another named agent without
// going through the full router cycle. The sub-call bypasses classification,
// runs within the parent session context, and does NOT append its turns to the
// main session history.
type SubAgentCaller interface {
	Call(ctx context.Context, agentID string, prompt string) (string, error)
}

// ChannelID identifies the communication channel a message originates from
// (e.g. "web", "discord", "whatsapp", "telegram").
type ChannelID string

// AgentID identifies a specific agent within the system
// (e.g. "comms", "builder", "research").
type AgentID string

// InboundMessage represents a message received from a user via any channel.
type InboundMessage struct {
	// ID is a unique identifier for this message.
	ID string
	// ChannelID identifies the channel the message arrived on.
	ChannelID ChannelID
	// UserID identifies the user who sent the message.
	UserID string
	// SessionID groups messages belonging to the same conversation session.
	SessionID string
	// Text is the raw message content.
	Text string
	// Timestamp is when the message was received.
	Timestamp time.Time
	// Metadata holds arbitrary channel-specific key/value pairs.
	Metadata map[string]string
}

// OutboundMessage represents a message to be delivered to a user via a channel.
type OutboundMessage struct {
	// SessionID ties the response back to the originating session.
	SessionID string
	// ChannelID identifies the channel to deliver the message on.
	ChannelID ChannelID
	// UserID identifies the recipient.
	UserID string
	// Text is the response content.
	Text string
	// Metadata holds arbitrary channel-specific key/value pairs.
	Metadata map[string]string
}

// ToolCall represents a single tool invocation requested by the model.
type ToolCall struct {
	// ID is the opaque identifier for this call, used to correlate results.
	ID string
	// Name is the tool being invoked.
	Name string
	// Arguments is the raw JSON-encoded argument string from the model.
	Arguments string
}

// ConversationTurn represents a single exchange in a conversation history.
// Role may be "system", "user", "assistant", or "tool".
type ConversationTurn struct {
	// Role is "system", "user", "assistant", or "tool".
	Role string
	// Content is the text of the turn. Empty for assistant turns that only
	// contain tool calls.
	Content string
	// ToolCalls is populated for assistant turns that request tool calls.
	ToolCalls []ToolCall
	// ToolCallID ties a tool-role turn back to the ToolCall it is responding to.
	ToolCallID string
}

// AgentRequest is the input payload passed to an agent for processing.
type AgentRequest struct {
	// SessionID identifies the ongoing conversation session.
	SessionID string
	// UserID identifies the user making the request.
	UserID string
	// Intent is the classified intent of the user's input (e.g. "code", "search").
	Intent string
	// History contains prior turns in the conversation for context.
	History []ConversationTurn
	// Input is the current user message to process.
	Input string
	// Metadata holds arbitrary request-scoped key/value pairs.
	Metadata map[string]string
	// SubCaller is set by the router so agents can dispatch sub-tasks to other
	// agents. Nil when not wired (e.g., in unit tests that don't need it).
	SubCaller SubAgentCaller
}

// AgentResponse is the output returned by an agent after processing a request.
type AgentResponse struct {
	// AgentID identifies which agent produced this response.
	AgentID AgentID
	// Output is the agent's generated reply.
	Output string
	// Error holds any error that occurred during processing; nil on success.
	Error error
	// Metadata holds arbitrary response-scoped key/value pairs.
	Metadata map[string]string
}
