package discord

import (
	"strings"
	"testing"
)

// ── sessionKey ────────────────────────────────────────────────────────────────

func TestSessionKey_GuildChannel(t *testing.T) {
	key := sessionKey("guild123", "chan456", "user789")
	want := "discord:guild123:chan456:user789"
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

func TestSessionKey_DirectMessage(t *testing.T) {
	key := sessionKey("", "chan456", "user789")
	want := "discord:dm:chan456:user789"
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

func TestSessionKey_TwoUsersInSameChannel_AreIsolated(t *testing.T) {
	a := sessionKey("g1", "ch1", "user-alice")
	b := sessionKey("g1", "ch1", "user-bob")
	if a == b {
		t.Error("different users in the same channel must have different session keys")
	}
}

func TestSessionKey_SameUserInTwoChannels_AreIsolated(t *testing.T) {
	a := sessionKey("g1", "ch-general", "user1")
	b := sessionKey("g1", "ch-random", "user1")
	if a == b {
		t.Error("same user in different channels must have different session keys")
	}
}

func TestSessionKey_SameUserAcrossGuilds_AreIsolated(t *testing.T) {
	a := sessionKey("guild-A", "ch1", "user1")
	b := sessionKey("guild-B", "ch1", "user1")
	if a == b {
		t.Error("same user in different guilds must have different session keys")
	}
}

// ── stripMention ──────────────────────────────────────────────────────────────

func TestStripMention_UserMention(t *testing.T) {
	got := stripMention("<@12345> hello world", "12345")
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestStripMention_NicknameMention(t *testing.T) {
	got := stripMention("<@!12345> hello world", "12345")
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestStripMention_NoMention(t *testing.T) {
	got := stripMention("hello world", "12345")
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestStripMention_EmptyBotID(t *testing.T) {
	got := stripMention("<@12345> hello", "")
	if got != "<@12345> hello" {
		t.Errorf("got %q, want unchanged text", got)
	}
}

func TestStripMention_LeadingWhitespace(t *testing.T) {
	got := stripMention("  <@12345> hello", "12345")
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// ── preprocessText ────────────────────────────────────────────────────────────

func TestPreprocessText_DM_NoPrefix_Routes(t *testing.T) {
	text, ok := preprocessText("what is the weather?", "bot1", "", true)
	if !ok {
		t.Error("DM should always be routed")
	}
	if text != "what is the weather?" {
		t.Errorf("got %q, want unchanged text", text)
	}
}

func TestPreprocessText_DM_WithMention_Stripped(t *testing.T) {
	text, ok := preprocessText("<@bot1> what is the weather?", "bot1", "", true)
	if !ok {
		t.Error("DM with mention should be routed")
	}
	if text != "what is the weather?" {
		t.Errorf("got %q, want %q", text, "what is the weather?")
	}
}

func TestPreprocessText_DM_EmptyAfterStrip_NotRouted(t *testing.T) {
	_, ok := preprocessText("<@bot1>", "bot1", "", true)
	if ok {
		t.Error("empty text after stripping mention should not be routed")
	}
}

func TestPreprocessText_Guild_NoPrefix_RoutesAll(t *testing.T) {
	text, ok := preprocessText("hello bot", "bot1", "", false)
	if !ok {
		t.Error("guild message with no prefix config should be routed")
	}
	if text != "hello bot" {
		t.Errorf("got %q", text)
	}
}

func TestPreprocessText_Guild_NoPrefix_MentionStripped(t *testing.T) {
	text, ok := preprocessText("<@bot1> help me", "bot1", "", false)
	if !ok {
		t.Error("should be routed")
	}
	if text != "help me" {
		t.Errorf("got %q, want %q", text, "help me")
	}
}

func TestPreprocessText_Guild_WithPrefix_MatchesPrefix(t *testing.T) {
	text, ok := preprocessText("!ai what is the capital of France?", "bot1", "!ai", false)
	if !ok {
		t.Error("message with matching prefix should be routed")
	}
	if text != "what is the capital of France?" {
		t.Errorf("got %q, want %q", text, "what is the capital of France?")
	}
}

func TestPreprocessText_Guild_WithPrefix_NoMatch_Ignored(t *testing.T) {
	_, ok := preprocessText("just chatting, not a command", "bot1", "!ai", false)
	if ok {
		t.Error("message without required prefix should not be routed")
	}
}

func TestPreprocessText_Guild_WithPrefix_MentionAccepted(t *testing.T) {
	text, ok := preprocessText("<@bot1> help me", "bot1", "!ai", false)
	if !ok {
		t.Error("bot mention should be accepted even when custom prefix is configured")
	}
	if text != "help me" {
		t.Errorf("got %q, want %q", text, "help me")
	}
}

func TestPreprocessText_Guild_WithPrefix_NicknameMentionAccepted(t *testing.T) {
	text, ok := preprocessText("<@!bot1> do something", "bot1", "!ai", false)
	if !ok {
		t.Error("nickname mention should be accepted")
	}
	if text != "do something" {
		t.Errorf("got %q, want %q", text, "do something")
	}
}

func TestPreprocessText_EmptyText_NotRouted(t *testing.T) {
	_, ok := preprocessText("   ", "bot1", "", false)
	if ok {
		t.Error("whitespace-only text should not be routed")
	}
}

// ── splitMessage ──────────────────────────────────────────────────────────────

func TestSplitMessage_ShortText(t *testing.T) {
	chunks := splitMessage("hello", 2000)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("got %v", chunks)
	}
}

func TestSplitMessage_ExactLimit(t *testing.T) {
	text := string(make([]byte, 2000))
	chunks := splitMessage(text, 2000)
	if len(chunks) != 1 {
		t.Errorf("text exactly at limit should not be split, got %d chunks", len(chunks))
	}
}

// ── truncateForEdit ───────────────────────────────────────────────────────────

func TestTruncateForEdit_ShortText_Unchanged(t *testing.T) {
	text := "short message"
	got := truncateForEdit(text)
	if got != text {
		t.Errorf("got %q, want unchanged", got)
	}
}

func TestTruncateForEdit_ExactLimit_Unchanged(t *testing.T) {
	text := strings.Repeat("a", maxMessageLen)
	got := truncateForEdit(text)
	if got != text {
		t.Errorf("text at exact limit should not be truncated")
	}
}

func TestTruncateForEdit_LongText_FitsWithinLimit(t *testing.T) {
	text := strings.Repeat("a", maxMessageLen+500)
	got := truncateForEdit(text)
	if len(got) > maxMessageLen {
		t.Errorf("truncated text length %d exceeds maxMessageLen %d", len(got), maxMessageLen)
	}
}

func TestTruncateForEdit_LongText_EndsWithEllipsis(t *testing.T) {
	text := strings.Repeat("a", maxMessageLen+500)
	got := truncateForEdit(text)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated text should end with ellipsis, got %q", got[len(got)-5:])
	}
}

func TestTruncateForEdit_BreaksAtWordBoundary(t *testing.T) {
	// Put a space at position 1900 (past the 3/4 mark of 1999 ≈ 1499).
	prefix := strings.Repeat("a", 1900)
	suffix := strings.Repeat("b", 200)
	text := prefix + " " + suffix // total > 2000
	got := truncateForEdit(text)
	// Should break at the space, so no "b" characters in the truncated result.
	if strings.Contains(got, "b") {
		t.Errorf("expected word-boundary split before 'b' run, got: %q", got[len(got)-20:])
	}
}

func TestSplitMessage_PreservesNewlines(t *testing.T) {
	// Build a 2001-char string with a newline at position 1600 (past the 3/4 mark
	// of 2000 = 1500), so splitMessage prefers to break there.
	part1 := strings.Repeat("a", 1600)
	part2 := strings.Repeat("b", 400)
	text := part1 + "\n" + part2 // 2001 chars total — forces a split
	chunks := splitMessage(text, 2000)
	if len(chunks) < 2 {
		t.Errorf("expected 2+ chunks, got %d", len(chunks))
	}
}
