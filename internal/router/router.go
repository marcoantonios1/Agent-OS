package router

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const unknownIntentReply = "I'm not sure how to help with that — could you rephrase or give me a bit more detail?"

// Agent is the interface every sub-agent must implement.
// Agents must not be constructed directly by callers; they are registered with
// the Router and invoked only through this interface.
type Agent interface {
	Handle(ctx context.Context, req types.AgentRequest) (types.AgentResponse, error)
}

// Router is the single entry point for all inbound messages. It owns the
// classify → dispatch → respond loop and keeps session history consistent.
type Router struct {
	Classifier IntentClassifier
	Agents     map[Intent]Agent
	Sessions   sessions.SessionStore
	log        *slog.Logger
}

// New returns a Router with the given classifier, agents, and session store.
func New(classifier IntentClassifier, agents map[Intent]Agent, store sessions.SessionStore) *Router {
	return &Router{
		Classifier: classifier,
		Agents:     agents,
		Sessions:   store,
		log:        slog.Default(),
	}
}

// Route is the core dispatch loop:
//  1. Load or create the session.
//  2. Build the full history (existing turns + current user message).
//  3. Classify intent.
//  4. Dispatch to the registered Agent (or return a helpful fallback for unknown).
//  5. Persist both the user turn and the assistant reply.
//  6. Return an OutboundMessage to the caller.
func (r *Router) Route(ctx context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	// 1. Load or create session.
	sess, err := r.loadOrCreate(msg)
	if err != nil {
		return types.OutboundMessage{}, fmt.Errorf("router: session: %w", err)
	}

	// 2. Build full history including the current user turn so the classifier
	//    and the agent both see it, without persisting it yet.
	history := append(sess.History, types.ConversationTurn{ //nolint:gocritic — intentional copy-append
		Role:    "user",
		Content: msg.Text,
	})

	// 3. Classify.
	intent, classifyErr := r.Classifier.Classify(ctx, msg.SessionID, msg.Text, history)
	if classifyErr != nil {
		// Classifier errors are non-fatal; intent will be IntentUnknown.
		r.log.WarnContext(ctx, "classifier error, defaulting to unknown",
			"session_id", msg.SessionID, "error", classifyErr)
	}

	r.log.InfoContext(ctx, "routing message",
		"session_id", msg.SessionID,
		"user_id", msg.UserID,
		"intent", intent,
	)

	// 4. Dispatch.
	replyText, dispatchErr := r.dispatch(ctx, msg, intent, history)

	// 5. Persist turns regardless of dispatch outcome so history stays accurate.
	if persistErr := r.persistTurns(msg.SessionID, msg.Text, replyText); persistErr != nil {
		r.log.WarnContext(ctx, "failed to persist turns", "session_id", msg.SessionID, "error", persistErr)
	}

	if dispatchErr != nil {
		return types.OutboundMessage{}, fmt.Errorf("router: dispatch: %w", dispatchErr)
	}

	return types.OutboundMessage{
		SessionID: msg.SessionID,
		ChannelID: msg.ChannelID,
		UserID:    msg.UserID,
		Text:      replyText,
	}, nil
}

// loadOrCreate fetches the session by ID or creates a fresh one.
func (r *Router) loadOrCreate(msg types.InboundMessage) (*sessions.Session, error) {
	sess, err := r.Sessions.Get(msg.SessionID)
	if err == nil {
		return sess, nil
	}

	// Any error from Get is treated as "not found" — create a new session.
	now := time.Now()
	sess = &sessions.Session{
		ID:        msg.SessionID,
		UserID:    msg.UserID,
		ChannelID: msg.ChannelID,
		Metadata:  msg.Metadata,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := r.Sessions.Save(sess); err != nil {
		return nil, fmt.Errorf("create session %s: %w", msg.SessionID, err)
	}
	r.log.InfoContext(context.Background(), "new session created",
		"session_id", msg.SessionID, "user_id", msg.UserID)
	return sess, nil
}

// dispatch resolves the intent to an Agent and calls it. For IntentUnknown or
// an unregistered intent it returns a helpful fallback string without an error.
func (r *Router) dispatch(
	ctx context.Context,
	msg types.InboundMessage,
	intent Intent,
	history []types.ConversationTurn,
) (string, error) {
	if intent == IntentUnknown {
		return unknownIntentReply, nil
	}

	agent, ok := r.Agents[intent]
	if !ok {
		r.log.WarnContext(ctx, "no agent registered for intent",
			"session_id", msg.SessionID, "intent", intent)
		return unknownIntentReply, nil
	}

	resp, err := agent.Handle(ctx, types.AgentRequest{
		SessionID: msg.SessionID,
		UserID:    msg.UserID,
		Intent:    string(intent),
		History:   history,
		Input:     msg.Text,
		Metadata:  msg.Metadata,
	})
	if err != nil {
		return "", fmt.Errorf("agent %s: %w", intent, err)
	}
	return resp.Output, nil
}

// persistTurns appends both the user and assistant turns to the session store.
func (r *Router) persistTurns(sessionID, userText, assistantText string) error {
	if err := r.Sessions.AppendTurn(sessionID, "user", userText); err != nil {
		return fmt.Errorf("append user turn: %w", err)
	}
	if err := r.Sessions.AppendTurn(sessionID, "assistant", assistantText); err != nil {
		return fmt.Errorf("append assistant turn: %w", err)
	}
	return nil
}
