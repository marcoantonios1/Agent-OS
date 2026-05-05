// Package attachments provides shared utilities for handling file attachments
// across all channel handlers (web, Discord, WhatsApp).
package attachments

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// MaxPDFSize is the maximum PDF size accepted — 20 MB.
const MaxPDFSize = 20 * 1024 * 1024

// MaxPDFPages is the maximum number of pages processed — 50.
// Pages beyond this limit are silently dropped; a note is appended to the
// returned text so the LLM knows the document was truncated.
const MaxPDFPages = 50

// ExtractPDFText extracts plain text from a PDF byte slice.
// Returns extracted text, or an error if the PDF is encrypted or malformed.
// For scanned PDFs (image-only, no embedded text), an error is returned —
// OCR is out of scope.
func ExtractPDFText(data []byte) (string, error) {
	if len(data) > MaxPDFSize {
		return "", fmt.Errorf("PDF size %d MB exceeds the %d MB limit",
			len(data)/(1024*1024), MaxPDFSize/(1024*1024))
	}

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if errors.Is(err, pdf.ErrInvalidPassword) {
			return "", fmt.Errorf("PDF is encrypted: a password is required to read it")
		}
		return "", fmt.Errorf("open PDF: %w", err)
	}

	numPages := r.NumPage()
	truncated := numPages > MaxPDFPages
	if truncated {
		numPages = MaxPDFPages
	}

	var sb strings.Builder
	for i := 1; i <= numPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		sb.WriteString(text)
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		return "", fmt.Errorf("no text extracted: PDF may be scanned or contain only images")
	}

	if truncated {
		result += "\n[PDF truncated at 50 pages]"
	}

	return result, nil
}
