package attachments

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// ── PDF builder helpers ───────────────────────────────────────────────────────

// buildPDF creates a minimal valid multi-page PDF where each page contains the
// given raw content stream. Each stream must be valid PDF content-stream syntax.
// pageTexts must not contain parentheses, backslashes, or non-ASCII characters
// (they are not escaped here — use simple alphanumeric strings in tests).
func buildPDF(t *testing.T, pageStreams []string) []byte {
	t.Helper()

	var buf bytes.Buffer
	write := func(s string) { buf.WriteString(s) }

	write("%PDF-1.4\n")

	nPages := len(pageStreams)
	// Object layout (1-indexed):
	//   1         Catalog
	//   2         Pages dictionary
	//   3..N+2    Page objects          (N = nPages)
	//   N+3..2N+2 Content streams
	//   2N+3      Font (Helvetica/Type1)
	fontObj := 2*nPages + 3
	contentStart := nPages + 3

	// Track byte offset of each object so we can build the xref table.
	// objOffset[i] is the byte offset of object (i+1).
	objOffset := make([]int, 0, 2+2*nPages+1)

	// Object 1: Catalog
	objOffset = append(objOffset, buf.Len())
	write("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")

	// Object 2: Pages
	kids := make([]string, nPages)
	for i := range pageStreams {
		kids[i] = fmt.Sprintf("%d 0 R", 3+i)
	}
	objOffset = append(objOffset, buf.Len())
	write(fmt.Sprintf("2 0 obj\n<</Type /Pages /Kids [%s] /Count %d>>\nendobj\n",
		strings.Join(kids, " "), nPages))

	// Objects 3..N+2: Page objects
	for i := range pageStreams {
		objOffset = append(objOffset, buf.Len())
		write(fmt.Sprintf(
			"%d 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"+
				" /Contents %d 0 R /Resources <</Font <</F1 %d 0 R>>>>>>\nendobj\n",
			3+i, contentStart+i, fontObj))
	}

	// Objects N+3..2N+2: Content streams
	for i, stream := range pageStreams {
		objOffset = append(objOffset, buf.Len())
		write(fmt.Sprintf("%d 0 obj\n<</Length %d>>\nstream\n%sendstream\nendobj\n",
			contentStart+i, len(stream), stream))
	}

	// Font object
	objOffset = append(objOffset, buf.Len())
	write(fmt.Sprintf(
		"%d 0 obj\n<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>\nendobj\n",
		fontObj))

	// xref table — each entry must be exactly 20 bytes.
	xrefPos := buf.Len()
	nObjs := len(objOffset) + 1 // includes the mandatory free object 0
	write(fmt.Sprintf("xref\n0 %d\n", nObjs))
	write("0000000000 65535 f \n") // object 0: free (always present)
	for _, off := range objOffset {
		write(fmt.Sprintf("%010d 00000 n \n", off))
	}

	write(fmt.Sprintf("trailer\n<</Size %d /Root 1 0 R>>\nstartxref\n%d\n", nObjs, xrefPos))
	write("%%EOF\n")

	return buf.Bytes()
}

// textStream returns a PDF content stream that draws text at a fixed position.
func textStream(text string) string {
	return fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", text)
}

// blankStream returns a PDF content stream with no text operators.
func blankStream() string { return "q Q\n" }

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestExtractPDFText_Valid(t *testing.T) {
	data := buildPDF(t, []string{textStream("Hello PDF")})

	got, err := ExtractPDFText(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Hello PDF") {
		t.Errorf("extracted text %q does not contain expected content", got)
	}
}

func TestExtractPDFText_MultiPage(t *testing.T) {
	data := buildPDF(t, []string{
		textStream("Page one"),
		textStream("Page two"),
		textStream("Page three"),
	})

	got, err := ExtractPDFText(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"Page one", "Page two", "Page three"} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted text missing %q; got: %q", want, got)
		}
	}
}

func TestExtractPDFText_PageLimit(t *testing.T) {
	// Build a PDF with MaxPDFPages+5 pages — extraction must stop at MaxPDFPages
	// and append the truncation note.
	streams := make([]string, MaxPDFPages+5)
	for i := range streams {
		streams[i] = textStream(fmt.Sprintf("page%d", i+1))
	}
	data := buildPDF(t, streams)

	got, err := ExtractPDFText(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(got, "[PDF truncated at 50 pages]") {
		t.Errorf("expected truncation note; got suffix %q", got[max(0, len(got)-40):])
	}
	// Pages beyond the limit must not appear in the output.
	overPage := fmt.Sprintf("page%d", MaxPDFPages+1)
	if strings.Contains(got, overPage) {
		t.Errorf("page beyond limit (%q) found in output", overPage)
	}
}

func TestExtractPDFText_Oversized(t *testing.T) {
	// Synthesise a byte slice just over the limit — no PDF parsing should occur.
	data := make([]byte, MaxPDFSize+1)
	_, err := ExtractPDFText(data)
	if err == nil {
		t.Fatal("expected error for oversized input, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size limit; got: %v", err)
	}
}

func TestExtractPDFText_Malformed(t *testing.T) {
	_, err := ExtractPDFText([]byte("this is not a PDF"))
	if err == nil {
		t.Fatal("expected error for malformed input, got nil")
	}
}

func TestExtractPDFText_Empty(t *testing.T) {
	_, err := ExtractPDFText([]byte{})
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

func TestExtractPDFText_NoText(t *testing.T) {
	// A valid PDF whose content stream contains no text operators (simulates
	// a scanned / image-only PDF where no text can be extracted).
	data := buildPDF(t, []string{blankStream()})

	_, err := ExtractPDFText(data)
	if err == nil {
		t.Fatal("expected error for image-only PDF, got nil")
	}
	if !strings.Contains(err.Error(), "no text extracted") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// max is provided for Go versions < 1.21 that don't have the builtin.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
