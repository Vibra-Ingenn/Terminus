package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures: scripts/gen_locked_and_form_test_pdfs.py

func lockedFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", "locked", name)
	if _, err := os.Stat(path); err != nil {
		t.Skip("run scripts/gen_locked_and_form_test_pdfs.py to create the locked fixtures")
	}
	return path
}

func TestTextInsideFormIsRead(t *testing.T) {
	res := toolPDFExtract(map[string]any{"path": lockedFixture(t, "form_text.pdf")})
	if !res.Success {
		t.Fatalf("form_text.pdf: %s", res.Error)
	}
	for _, want := range []string{"Text inside a form", "Second line of the form"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("missing %q in %q", want, res.Output)
		}
	}
	if strings.Contains(res.Output, "made of pictures") {
		t.Errorf("text PDF called pictures: %q", res.Output)
	}
}

func TestLockedPDFsThatOpenWithoutPassword(t *testing.T) {
	for _, name := range []string{"locked_aes128.pdf", "locked_aes256.pdf"} {
		res := toolPDFExtract(map[string]any{"path": lockedFixture(t, name)})
		if !res.Success || !strings.Contains(res.Output, "Account balance 1234.56") {
			t.Errorf("%s: success=%v err=%q out=%q", name, res.Success, res.Error, res.Output)
		}
	}
}

func TestLockedJPEGScanIsOCRd(t *testing.T) {
	path := lockedFixture(t, "locked_rc4_40_scan.pdf")
	if _, err := findTesseractExecutable(); err != nil {
		t.Skip("Tesseract not installed")
	}
	res := toolPDFExtract(map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("locked scan: %s", res.Error)
	}
	if !strings.Contains(strings.ToUpper(res.Output), "LOCKED SCAN TEST") {
		t.Errorf("OCR text missing: %q", res.Output)
	}
}

func TestPasswordLockedPDFSaysSo(t *testing.T) {
	res := toolPDFExtract(map[string]any{"path": lockedFixture(t, "locked_password.pdf")})
	if res.Success {
		t.Fatalf("password PDF should not open: %q", res.Output)
	}
	if !strings.Contains(res.Error, "locked with a password") || strings.Contains(strings.ToLower(res.Error), "damaged") {
		t.Errorf("message = %q", res.Error)
	}
}

func TestPageCountsAreNotesNotWarnings(t *testing.T) {
	warns, notes := splitNotes([]string{"3 pages read from the PDF text layer", "1 page read by OCR", "this PDF was damaged; the text was recovered"}, 1000)
	if len(notes) != 2 || len(warns) != 1 {
		t.Fatalf("warns=%q notes=%q", warns, notes)
	}
	// Per-page symbol lines are added up; a few in a long text is a note.
	sym := []string{"some symbols in this PDF couldn't be read as text (2)", "some symbols in this PDF couldn't be read as text (3)"}
	warns, notes = splitNotes(sym, 40000)
	if len(warns) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "(5)") {
		t.Fatalf("few symbols: warns=%q notes=%q", warns, notes)
	}
	warns, _ = splitNotes(sym, 100)
	if len(warns) != 1 {
		t.Fatalf("many symbols should warn: %q", warns)
	}
}
