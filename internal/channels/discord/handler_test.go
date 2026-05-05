package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
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

// ── passesFilter ──────────────────────────────────────────────────────────────

func TestPassesFilter_DM_AlwaysTrue(t *testing.T) {
	if !passesFilter("", "bot1", "!ai", true) {
		t.Error("DM should always pass the filter")
	}
}

func TestPassesFilter_NoPrefix_AlwaysTrue(t *testing.T) {
	if !passesFilter("hello", "bot1", "", false) {
		t.Error("guild channel with no prefix should always pass")
	}
}

func TestPassesFilter_Guild_WithPrefix_Matches(t *testing.T) {
	if !passesFilter("!ai describe this", "bot1", "!ai", false) {
		t.Error("message starting with prefix should pass")
	}
}

func TestPassesFilter_Guild_WithMention_Passes(t *testing.T) {
	if !passesFilter("<@bot1> describe this", "bot1", "!ai", false) {
		t.Error("message with @mention should pass even without prefix")
	}
}

func TestPassesFilter_Guild_NoMatchFails(t *testing.T) {
	if passesFilter("just chatting", "bot1", "!ai", false) {
		t.Error("message without prefix or mention should not pass")
	}
}

func TestPassesFilter_Guild_EmptyText_NoPrefix_Passes(t *testing.T) {
	// Attachment-only message in a channel with no prefix configured.
	if !passesFilter("", "bot1", "", false) {
		t.Error("empty text with no prefix should pass (attachment-only message)")
	}
}

// ── fetchAttachments helpers ──────────────────────────────────────────────────

// newAttachmentHandler returns a Handler wired to a custom HTTP client,
// suitable for unit-testing fetchAttachments without a real Discord session.
func newAttachmentHandler(client *http.Client) *Handler {
	return &Handler{log: slog.Default(), httpClient: client}
}

// buildDiscordPDF creates a minimal valid single-page text PDF for tests.
func buildDiscordPDF(pageText string) []byte {
	var buf bytes.Buffer
	write := func(s string) { buf.WriteString(s) }
	write("%PDF-1.4\n")

	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", pageText)
	offsets := make([]int, 0, 5)

	offsets = append(offsets, buf.Len())
	write("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")

	offsets = append(offsets, buf.Len())
	write("2 0 obj\n<</Type /Pages /Kids [3 0 R] /Count 1>>\nendobj\n")

	offsets = append(offsets, buf.Len())
	write(fmt.Sprintf("3 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"+
		" /Contents 4 0 R /Resources <</Font <</F1 5 0 R>>>>>>\nendobj\n"))

	offsets = append(offsets, buf.Len())
	write(fmt.Sprintf("4 0 obj\n<</Length %d>>\nstream\n%sendstream\nendobj\n",
		len(stream), stream))

	offsets = append(offsets, buf.Len())
	write("5 0 obj\n<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>\nendobj\n")

	xrefPos := buf.Len()
	nObjs := len(offsets) + 1
	write(fmt.Sprintf("xref\n0 %d\n", nObjs))
	write("0000000000 65535 f \n")
	for _, off := range offsets {
		write(fmt.Sprintf("%010d 00000 n \n", off))
	}
	write(fmt.Sprintf("trailer\n<</Size %d /Root 1 0 R>>\nstartxref\n%d\n", nObjs, xrefPos))
	write("%%EOF\n")
	return buf.Bytes()
}

// ── fetchAttachments tests ────────────────────────────────────────────────────

func TestFetchAttachments_Image(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a} // PNG-ish header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgBytes) //nolint:errcheck
	}))
	defer srv.Close()

	h := newAttachmentHandler(srv.Client())
	atts := []*discordgo.MessageAttachment{
		{URL: srv.URL + "/img.png", Filename: "img.png", ContentType: "image/png"},
	}

	parts := h.fetchAttachments(context.Background(), atts)

	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	p := parts[0]
	if p.Type != "image" {
		t.Errorf("type = %q, want image", p.Type)
	}
	if p.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", p.MimeType)
	}
	if p.Filename != "img.png" {
		t.Errorf("filename = %q, want img.png", p.Filename)
	}
	if want := base64.StdEncoding.EncodeToString(imgBytes); p.ImageData != want {
		t.Error("ImageData does not match base64-encoded bytes")
	}
}

func TestFetchAttachments_PDF(t *testing.T) {
	pdfBytes := buildDiscordPDF("Invoice total 250")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(pdfBytes) //nolint:errcheck
	}))
	defer srv.Close()

	h := newAttachmentHandler(srv.Client())
	atts := []*discordgo.MessageAttachment{
		{URL: srv.URL + "/inv.pdf", Filename: "inv.pdf", ContentType: "application/pdf"},
	}

	parts := h.fetchAttachments(context.Background(), atts)

	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	p := parts[0]
	if p.Type != "text" {
		t.Errorf("type = %q, want text", p.Type)
	}
	if !strings.Contains(p.Text, "Invoice total 250") {
		t.Errorf("extracted text %q missing expected content", p.Text)
	}
	if p.Filename != "inv.pdf" {
		t.Errorf("filename = %q, want inv.pdf", p.Filename)
	}
}

func TestFetchAttachments_UnsupportedType_Ignored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("zip data")) //nolint:errcheck
	}))
	defer srv.Close()

	h := newAttachmentHandler(srv.Client())
	atts := []*discordgo.MessageAttachment{
		{URL: srv.URL + "/archive.zip", Filename: "archive.zip", ContentType: "application/zip"},
	}

	parts := h.fetchAttachments(context.Background(), atts)
	if len(parts) != 0 {
		t.Errorf("got %d parts, want 0 (unsupported type should be silently ignored)", len(parts))
	}
}

func TestFetchAttachments_ContentTypeWithCharset(t *testing.T) {
	imgBytes := []byte{0xFF, 0xD8} // JPEG magic bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(imgBytes) //nolint:errcheck
	}))
	defer srv.Close()

	h := newAttachmentHandler(srv.Client())
	// Discord occasionally sends charset parameters alongside the MIME type.
	atts := []*discordgo.MessageAttachment{
		{URL: srv.URL + "/photo.jpg", Filename: "photo.jpg", ContentType: "image/jpeg; charset=binary"},
	}

	parts := h.fetchAttachments(context.Background(), atts)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1 (charset param should be stripped)", len(parts))
	}
	if parts[0].MimeType != "image/jpeg" {
		t.Errorf("mime = %q, want image/jpeg", parts[0].MimeType)
	}
}

func TestFetchAttachments_FallbackMimeFromExtension(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(imgBytes) //nolint:errcheck
	}))
	defer srv.Close()

	h := newAttachmentHandler(srv.Client())
	// ContentType is empty — handler should infer from ".png" extension.
	atts := []*discordgo.MessageAttachment{
		{URL: srv.URL + "/screenshot.png", Filename: "screenshot.png", ContentType: ""},
	}

	parts := h.fetchAttachments(context.Background(), atts)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1 (MIME inferred from extension)", len(parts))
	}
	if parts[0].MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", parts[0].MimeType)
	}
}

func TestFetchAttachments_MaxFiveProcessed(t *testing.T) {
	imgBytes := []byte{0xFF, 0xD8}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(imgBytes) //nolint:errcheck
	}))
	defer srv.Close()

	h := newAttachmentHandler(srv.Client())
	atts := make([]*discordgo.MessageAttachment, 7)
	for i := range atts {
		atts[i] = &discordgo.MessageAttachment{
			URL:         srv.URL + fmt.Sprintf("/img%d.jpg", i),
			Filename:    fmt.Sprintf("img%d.jpg", i),
			ContentType: "image/jpeg",
		}
	}

	parts := h.fetchAttachments(context.Background(), atts)
	if len(parts) != maxDiscordAttachments {
		t.Errorf("got %d parts, want %d (max cap)", len(parts), maxDiscordAttachments)
	}
}

func TestFetchAttachments_HTTPError_Skipped(t *testing.T) {
	// Server immediately closes — triggers a connection error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	h := newAttachmentHandler(srv.Client())
	atts := []*discordgo.MessageAttachment{
		{URL: srv.URL + "/img.png", Filename: "img.png", ContentType: "image/png"},
	}

	// Should not panic; the attachment is simply skipped.
	parts := h.fetchAttachments(context.Background(), atts)
	if len(parts) != 0 {
		t.Errorf("got %d parts, want 0 after HTTP error", len(parts))
	}
}

func TestFetchAttachments_Empty(t *testing.T) {
	h := newAttachmentHandler(http.DefaultClient)
	if parts := h.fetchAttachments(context.Background(), nil); parts != nil {
		t.Errorf("nil attachments should return nil, got %v", parts)
	}
}
