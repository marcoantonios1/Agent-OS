package sessions

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// confidenceThreshold is the minimum confidence required for a signal to be
// included in the personality context block injected into agent system prompts.
const confidenceThreshold = 0.6

// signalLabels maps signal keys to human-readable labels for system prompts.
var signalLabels = map[string]string{
	SignalResponseLength:     "Response length preference",
	SignalTechnicalDepth:     "Technical depth",
	SignalCommunicationStyle: "Communication style",
	SignalHumorTolerance:     "Humor tolerance",
	SignalQuestionStyle:      "Question style",
	SignalWorkingHours:       "Working hours",
	SignalUrgencyPattern:     "Urgency pattern",
	SignalTopicInterests:     "Topic interests",
}

// FormatPersonalityContext formats high-confidence personality signals into a
// system-prompt block. Only signals with confidence >= confidenceThreshold are
// included. Returns an empty string when profile is nil, has no signals, or has
// no signals that clear the threshold — so callers can safely WriteString it.
func FormatPersonalityContext(profile *PersonalityProfile) string {
	if profile == nil || len(profile.Signals) == 0 {
		return ""
	}

	// Collect qualifying signals in a deterministic order.
	qualified := make([]PersonalitySignal, 0, len(profile.Signals))
	for _, sig := range profile.Signals {
		if sig.Confidence >= confidenceThreshold {
			qualified = append(qualified, sig)
		}
	}
	if len(qualified) == 0 {
		return ""
	}
	sort.Slice(qualified, func(i, j int) bool { return qualified[i].Key < qualified[j].Key })

	var sb strings.Builder
	sb.WriteString("\n\n## User personality (inferred — treat as guidance, not rules)")
	for _, sig := range qualified {
		label, ok := signalLabels[sig.Key]
		if !ok {
			label = sig.Key
		}
		sb.WriteString("\n- ")
		sb.WriteString(label)
		sb.WriteString(": ")
		sb.WriteString(sig.Value)
		sb.WriteString(fmt.Sprintf(" (confidence: %.2g)", sig.Confidence))
	}
	return sb.String()
}

// PersonalitySignal is a single observed behavioural trait for a user.
// Signals are derived automatically from conversation patterns — not set explicitly.
type PersonalitySignal struct {
	Key        string    // e.g. "response_length", "technical_depth"
	Value      string    // e.g. "brief", "high"
	Confidence float64   // 0.0–1.0, increases with repeated observation
	LastSeen   time.Time
	Count      int // how many times this signal has been observed
}

// PersonalityProfile is the full set of observed signals for one user.
type PersonalityProfile struct {
	UserID    string
	Signals   []PersonalitySignal
	UpdatedAt time.Time
}

// PersonalityStore persists personality signals independently of session lifetime.
// Implementations must be safe for concurrent use.
type PersonalityStore interface {
	// GetPersonality returns the full profile for userID.
	// Returns a zero-value profile (not an error) when the user has no signals yet.
	GetPersonality(userID string) (*PersonalityProfile, error)

	// SavePersonality replaces the full profile for profile.UserID.
	SavePersonality(profile *PersonalityProfile) error

	// UpsertSignal increments the count for an existing signal or inserts a new one.
	// Confidence is recalculated as min(count/10, 1.0) after the increment.
	// LastSeen is always updated to now.
	UpsertSignal(userID string, signal PersonalitySignal) error
}

// Signal keys — use these constants to avoid typos when reading/writing signals.
const (
	SignalResponseLength     = "response_length"     // "brief" | "detailed" | "verbose"
	SignalTechnicalDepth     = "technical_depth"      // "low" | "medium" | "high"
	SignalCommunicationStyle = "communication_style"  // "formal" | "casual" | "direct"
	SignalHumorTolerance     = "humor_tolerance"      // "none" | "light" | "high"
	SignalQuestionStyle      = "question_style"       // "asks_followup" | "assumes" | "guesses"
	SignalWorkingHours       = "working_hours"        // "morning" | "evening" | "night" | "mixed"
	SignalUrgencyPattern     = "urgency_pattern"      // "high" | "medium" | "low"
	SignalTopicInterests     = "topic_interests"      // comma-separated topics
)
