package sessions

import "time"

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
