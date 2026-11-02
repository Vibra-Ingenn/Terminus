package tools

import (
	"strings"
	"testing"
)

func TestCleanPDFExtractedText_SymbolJunk(t *testing.T) {
	raw := "•\uFFFD Key combination\uFFFD – several keys pressed together, e.g.\uFFFD Ctrl\uFFFD +A\uFFFD E\uFFFD"
	got, warns := CleanPDFExtractedText(raw)
	want := "• Key combination – several keys pressed together, e.g. Ctrl +A E"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "some symbols") {
		t.Fatalf("expected symbol warning, got %v", warns)
	}
}

func TestRepairUTF8ReadAsLatin1(t *testing.T) {
	got := repairUTF8ReadAsLatin1("Sâ€¦ Oâ€¦")
	if got != "S… O…" {
		t.Fatalf("got %q want S… O…", got)
	}
	got = repairUTF8ReadAsLatin1("cafÃ©")
	if got != "café" {
		t.Fatalf("got %q want café", got)
	}
	got = repairUTF8ReadAsLatin1("dashâ€”end")
	if got != "dash—end" {
		t.Fatalf("em dash: got %q", got)
	}
	got = repairUTF8ReadAsLatin1("â€œquoteâ€\u009d")
	if got != "“quote”" {
		t.Fatalf("quotes: got %q", got)
	}
}

func TestSmallCapsHeadingsRejoined(t *testing.T) {
	got, _ := CleanPDFExtractedText("B\nACKHAUL AND \nF\nRONTHAUL\nA list\nItem")
	for _, want := range []string{"BACKHAUL AND", "FRONTHAUL", "A list"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
