package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanFixturePDFs(t *testing.T) {
	dir := filepath.Join("testdata", "scans")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("run scripts/gen_scan_test_pdfs.py to create scan fixtures")
	}
	names := []string{"scan_colour.pdf", "scan_grey.pdf", "scan_1bit.pdf", "scan_three_pages.pdf"}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s", name)
		}
		res := ocrPDFPages(path, "eng", "ocr_extract")
		if !res.Success {
			t.Fatalf("%s: %s", name, res.Error)
		}
		body, warns := splitExtractWarnings(res.Output)
		body, more := CleanExtractionBody(body)
		warns = append(warns, more...)
		if strings.TrimSpace(body) == "" {
			t.Fatalf("%s: empty OCR body", name)
		}
		if name == "scan_three_pages.pdf" {
			pages := splitPageTexts(body)
			if len(pages) != 3 {
				t.Fatalf("three-page scan: got %d pages", len(pages))
			}
			for _, w := range warns {
				if strings.Contains(w, "couldn't be read") && strings.Contains(body, w) {
					t.Fatalf("page warning leaked into text: %q", w)
				}
			}
		}
	}
}
