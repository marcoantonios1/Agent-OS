package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const unknownIntentReply = "I'm not sure how to help with that — could you rephrase or give me a bit more detail?"

// responseSeparator is placed between merged agent responses in compound replies.
const responseSeparator = "\n\n---\n\n"

// approvalKeywords are the exact lowercase strings accepted as a confirmation.
var approvalKeywords = map[string]bool{
	"yes": true, "y": true, "confirm": true, "approve": true,
	"ok": true, "sure": true, "proceed": true, "go ahead": true,
}

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
	Approvals  approval.Store
	// Users is optional. When set, the user's profile is loaded on every
	// dispatch and injected into AgentRequest.Metadata under "user.profile"
	// so agents can personalise their system prompts.
	Users sessions.UserStore
	log   *slog.Logger
}

// New returns a Router with the given classifier, agents, session store, and
// approval store.
func New(classifier IntentClassifier, agents map[Intent]Agent, store sessions.SessionStore, approvals approval.Store) *Router {
	return &Router{
		Classifier: classifier,
		Agents:     agents,
		Sessions:   store,
		Approvals:  approvals,
		log:        slog.Default(),
	}
}

// Route is the core dispatch loop:
//  1. Load or create the session.
//  2. If the message is a confirmation keyword, grant all pending approvals.
//  3. Build the full history (existing turns + current user message).
//  4. Classify intent(s).
//  5. For compound requests dispatch each agent in order; single requests
//     dispatch to one agent. The session ID is attached to ctx so
//     approval-gated tools can check the store.
//  6. Persist both the user turn and the assistant reply.
//  7. Return an OutboundMessage to the caller.
func (r *Router) Route(ctx context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	// 1. Load or create session.
	sess, err := r.loadOrCreate(msg)
	if err != nil {
		return types.OutboundMessage{}, fmt.Errorf("router: session: %w", err)
	}

	// 2. Grant pending approvals when the user sends a confirmation keyword.
	if r.Approvals != nil && isApprovalMessage(msg.Text) {
		pending := r.Approvals.ListPending(msg.SessionID)
		for _, rec := range pending {
			r.Approvals.Grant(msg.SessionID, rec.ActionID)
			r.log.InfoContext(ctx, "approval granted",
				"session_id", msg.SessionID, "action_id", rec.ActionID)
		}
	}

	// 3. Build full history including the current user turn.
	history := append(sess.History, types.ConversationTurn{ //nolint:gocritic — intentional copy-append
		Role:    "user",
		Content: msg.Text,
	})

	// 4. Classify — returns an ordered []Intent (one or more).
	intents, classifyErr := r.Classifier.Classify(ctx, msg.SessionID, msg.Text, history)
	if classifyErr != nil {
		r.log.WarnContext(ctx, "classifier error, defaulting to unknown",
			"session_id", msg.SessionID, "error", classifyErr)
	}

	r.log.InfoContext(ctx, "routing message",
		"session_id", msg.SessionID,
		"user_id", msg.UserID,
		"intents", intents,
	)

	// 5. Dispatch — inject session and user IDs into ctx so tools can read them.
	ctx = approval.WithSessionID(ctx, msg.SessionID)
	ctx = sessions.WithUserID(ctx, msg.UserID)
	agentResp, dispatchErr := r.dispatchAll(ctx, msg, intents, history, sess.Metadata)

	// Save any metadata the agent(s) produced back into the session.
	for k, v := range agentResp.Metadata {
		if err := r.Sessions.SetMetadata(msg.SessionID, k, v); err != nil {
			r.log.WarnContext(ctx, "failed to save agent metadata",
				"session_id", msg.SessionID, "key", k, "error", err)
		}
	}

	// 6. Persist turns regardless of dispatch outcome so history stays accurate.
	if persistErr := r.persistTurns(msg.SessionID, msg.Text, agentResp.Output); persistErr != nil {
		r.log.WarnContext(ctx, "failed to persist turns", "session_id", msg.SessionID, "error", persistErr)
	}

	if dispatchErr != nil {
		return types.OutboundMessage{}, fmt.Errorf("router: dispatch: %w", dispatchErr)
	}

	return types.OutboundMessage{
		SessionID: msg.SessionID,
		ChannelID: msg.ChannelID,
		UserID:    msg.UserID,
		Text:      agentResp.Output,
	}, nil
}

// isApprovalMessage returns true when the trimmed, lowercased message exactly
// matches one of the recognised confirmation keywords.
func isApprovalMessage(text string) bool {
	return approvalKeywords[strings.ToLower(strings.TrimSpace(text))]
}

// loadOrCreate fetches the session by ID or creates a fresh one.
func (r *Router) loadOrCreate(msg types.InboundMessage) (*sessions.Session, error) {
	sess, err := r.Sessions.Get(msg.SessionID)
	if err == nil {
		return sess, nil
	}

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

// dispatchAll resolves intents to agents and runs them. For a single intent
// the error from the agent is returned to the caller (existing behaviour). For
// compound requests each agent is run in order; per-agent errors are folded
// into the output text so one failure never suppresses the others.
func (r *Router) dispatchAll(
	ctx context.Context,
	msg types.InboundMessage,
	intents []Intent,
	history []types.ConversationTurn,
	sessionMeta map[string]string,
) (types.AgentResponse, error) {
	// Filter to only valid, non-unknown intents.
	valid := make([]Intent, 0, len(intents))
	for _, i := range intents {
		if i != IntentUnknown {
			valid = append(valid, i)
		}
	}

	if len(valid) == 0 {
		return types.AgentResponse{Output: unknownIntentReply}, nil
	}

	// Single intent: preserve existing behaviour, including error propagation.
	if len(valid) == 1 {
		return r.dispatch(ctx, msg, valid[0], history, sessionMeta)
	}

	// Compound intent: run each agent sequentially, merge outputs.
	r.log.InfoContext(ctx, "compound intent dispatch",
		"session_id", msg.SessionID, "count", len(valid), "intents", valid)

	parts := make([]string, 0, len(valid))
	mergedMeta := make(map[string]string)

	for _, intent := range valid {
		resp, err := r.dispatch(ctx, msg, intent, history, sessionMeta)
		if err != nil {
			r.log.WarnContext(ctx, "agent error in compound dispatch",
				"session_id", msg.SessionID, "intent", intent, "error", err)
			parts = append(parts, fmt.Sprintf("**[%s — error]** %v", intent, err))
			continue
		}
		// Label each section when there are multiple agents.
		parts = append(parts, fmt.Sprintf("**[%s]**\n%s", intent, resp.Output))
		// Merge metadata — later agents overwrite earlier ones on key collision.
		for k, v := range resp.Metadata {
			mergedMeta[k] = v
		}
	}

	return types.AgentResponse{
		Output:   strings.Join(parts, responseSeparator),
		Metadata: mergedMeta,
	}, nil
}

// dispatch resolves a single intent to an Agent and calls it. For IntentUnknown
// or an unregistered intent it returns a helpful fallback without an error.
func (r *Router) dispatch(
	ctx context.Context,
	msg types.InboundMessage,
	intent Intent,
	history []types.ConversationTurn,
	sessionMeta map[string]string,
) (types.AgentResponse, error) {
	fallback := types.AgentResponse{Output: unknownIntentReply}

	if intent == IntentUnknown {
		return fallback, nil
	}

	agent, ok := r.Agents[intent]
	if !ok {
		r.log.WarnContext(ctx, "no agent registered for intent",
			"session_id", msg.SessionID, "intent", intent)
		return fallback, nil
	}

	r.log.InfoContext(ctx, "agent_dispatch",
		"session_id", msg.SessionID,
		"agent_id", string(intent),
	)

	// Build per-dispatch metadata: session metadata + freshly loaded user
	// profile. We copy to avoid mutating the session's metadata map.
	agentMeta := make(map[string]string, len(sessionMeta)+1)
	for k, v := range sessionMeta {
		agentMeta[k] = v
	}
	if r.Users != nil {
		if profile, err := r.Users.GetUser(msg.UserID); err == nil {
			if b, err := json.Marshal(profile); err == nil {
				agentMeta["user.profile"] = string(b)
			}
		}
	}

	start := time.Now()
	resp, err := agent.Handle(ctx, types.AgentRequest{
		SessionID: msg.SessionID,
		UserID:    msg.UserID,
		Intent:    string(intent),
		History:   history,
		Input:     msg.Text,
		Metadata:  agentMeta,
	})
	latency := time.Since(start).Milliseconds()
	if err != nil {
		r.log.WarnContext(ctx, "agent_error",
			"session_id", msg.SessionID,
			"agent_id", string(intent),
			"latency_ms", latency,
			"error", err,
		)
		return types.AgentResponse{}, fmt.Errorf("agent %s: %w", intent, err)
	}
	r.log.InfoContext(ctx, "agent_complete",
		"session_id", msg.SessionID,
		"agent_id", string(intent),
		"latency_ms", latency,
	)
	return resp, nil
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
