package tools

import (
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

func TestSpaceRatio(t *testing.T) {
	// Zero spaces: words run together
	noSpaces := "Publishedasaconferencepaper"
	if r := spaceRatio(noSpaces); r != 0 {
		t.Errorf("expected spaceRatio of 0, got %f", r)
	}

	// Normal English prose has ~15-20% spaces
	normal := "Published as a conference paper at the international conference."
	if r := spaceRatio(normal); r < 0.10 || r > 0.30 {
		t.Errorf("expected normal prose spaceRatio between 0.10 and 0.30, got %f", r)
	}

	// Empty string should be 0
	if r := spaceRatio(""); r != 0 {
		t.Errorf("expected 0 for empty string, got %f", r)
	}
}

func TestLigaturesReplacement(t *testing.T) {
	input := "The patient has ﬀ, ﬁ, ﬂ, ﬃ, ﬄ, ﬅ, and ﬆ."
	expected := "The patient has ff, fi, fl, ffi, ffl, st, and st."
	actual := ligatures.Replace(input)
	if actual != expected {
		t.Errorf("expected %q, got %q", expected, actual)
	}
}

func TestRenderAndExtractPDFSpaces(t *testing.T) {
	r := NewRegistry()
	res := r.Dispatch(ToolCall{
		Name: "render_pdf",
		Arguments: map[string]any{
			"title":    "Spacing Test",
			"content":  "Published as a conference paper with normal words and spaces.",
			"filename": "spacing-test",
		},
	})
	if !res.Success {
		t.Fatalf("render_pdf failed: %s", res.Error)
	}

	pdfPath := strings.TrimPrefix(res.Output, "PDF created at ")

	parseRes := r.Dispatch(ToolCall{
		Name: "document_parse",
		Arguments: map[string]any{
			"path": pdfPath,
		},
	})
	if !parseRes.Success {
		t.Fatalf("document_parse failed: %s", parseRes.Error)
	}

	if !strings.Contains(parseRes.Output, "Published as a conference paper") {
		t.Errorf("expected parsed output to maintain spaces between words, got: %s", parseRes.Output)
	}
}

func TestLayoutSpacedLettersStayOneWord(t *testing.T) {
	// The letter gaps are all 2 points; the one word gap is 6 points wider.
	var items []pdf.Text
	x := 0.0
	for _, s := range []string{"D", "Y", "Q", " ", "V", "1"} {
		if s == " " {
			x += 6
			continue
		}
		items = append(items, pdf.Text{FontSize: 10, X: x, Y: 100, W: 5, S: s})
		x += 7
	}
	if got := joinLayoutItems(items); got != "DYQ V1" {
		t.Errorf("got %q, want %q", got, "DYQ V1")
	}
}
