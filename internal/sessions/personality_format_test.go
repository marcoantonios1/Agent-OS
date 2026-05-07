package sessions_test

import (
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

func TestFormatPersonalityContext_NilProfile(t *testing.T) {
	if got := sessions.FormatPersonalityContext(nil); got != "" {
		t.Errorf("expected empty string for nil profile, got %q", got)
	}
}

func TestFormatPersonalityContext_EmptySignals(t *testing.T) {
	p := &sessions.PersonalityProfile{UserID: "u1"}
	if got := sessions.FormatPersonalityContext(p); got != "" {
		t.Errorf("expected empty string for profile with no signals, got %q", got)
	}
}

func TestFormatPersonalityContext_AllBelowThreshold(t *testing.T) {
	p := &sessions.PersonalityProfile{
		UserID: "u1",
		Signals: []sessions.PersonalitySignal{
			{Key: sessions.SignalResponseLength, Value: "brief", Confidence: 0.3},
			{Key: sessions.SignalTechnicalDepth, Value: "high", Confidence: 0.59},
		},
	}
	if got := sessions.FormatPersonalityContext(p); got != "" {
		t.Errorf("expected empty string when all signals below threshold, got %q", got)
	}
}

func TestFormatPersonalityContext_MixedConfidence_OnlyHighIncluded(t *testing.T) {
	p := &sessions.PersonalityProfile{
		UserID: "u1",
		Signals: []sessions.PersonalitySignal{
			{Key: sessions.SignalResponseLength, Value: "brief", Confidence: 0.9},
			{Key: sessions.SignalTechnicalDepth, Value: "high", Confidence: 0.85},
			{Key: sessions.SignalHumorTolerance, Value: "light", Confidence: 0.4}, // below threshold
			{Key: sessions.SignalUrgencyPattern, Value: "low", Confidence: 0.2},   // below threshold
		},
	}
	got := sessions.FormatPersonalityContext(p)

	if !strings.Contains(got, "Response length preference") {
		t.Error("expected response_length signal in output")
	}
	if !strings.Contains(got, "Technical depth") {
		t.Error("expected technical_depth signal in output")
	}
	if strings.Contains(got, "Humor tolerance") {
		t.Error("low-confidence humor_tolerance signal must not appear in output")
	}
	if strings.Contains(got, "Urgency pattern") {
		t.Error("low-confidence urgency_pattern signal must not appear in output")
	}
}

func TestFormatPersonalityContext_ThresholdBoundary(t *testing.T) {
	p := &sessions.PersonalityProfile{
		UserID: "u1",
		Signals: []sessions.PersonalitySignal{
			{Key: sessions.SignalCommunicationStyle, Value: "direct", Confidence: 0.6},  // exactly at threshold — included
			{Key: sessions.SignalWorkingHours, Value: "morning", Confidence: 0.599},     // just below — excluded
		},
	}
	got := sessions.FormatPersonalityContext(p)

	if !strings.Contains(got, "Communication style") {
		t.Error("signal at confidence=0.6 must be included")
	}
	if strings.Contains(got, "Working hours") {
		t.Error("signal at confidence=0.599 must be excluded")
	}
}

func TestFormatPersonalityContext_HeaderPresent(t *testing.T) {
	p := &sessions.PersonalityProfile{
		UserID: "u1",
		Signals: []sessions.PersonalitySignal{
			{Key: sessions.SignalTechnicalDepth, Value: "high", Confidence: 0.8},
		},
	}
	got := sessions.FormatPersonalityContext(p)

	if !strings.Contains(got, "## User personality (inferred — treat as guidance, not rules)") {
		t.Error("expected personality block header")
	}
}

func TestFormatPersonalityContext_ConfidenceFormatted(t *testing.T) {
	p := &sessions.PersonalityProfile{
		UserID: "u1",
		Signals: []sessions.PersonalitySignal{
			{Key: sessions.SignalResponseLength, Value: "detailed", Confidence: 0.85},
		},
	}
	got := sessions.FormatPersonalityContext(p)

	if !strings.Contains(got, "0.85") {
		t.Errorf("expected confidence value in output, got: %q", got)
	}
}

func TestFormatPersonalityContext_UnknownKeyUsedAsLabel(t *testing.T) {
	p := &sessions.PersonalityProfile{
		UserID: "u1",
		Signals: []sessions.PersonalitySignal{
			{Key: "custom_signal", Value: "yes", Confidence: 0.9},
		},
	}
	got := sessions.FormatPersonalityContext(p)

	// Unknown keys should fall back to the raw key as the label.
	if !strings.Contains(got, "custom_signal") {
		t.Errorf("expected raw key as label for unknown signal, got: %q", got)
	}
}
