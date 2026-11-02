package tools

// pdflayout.go — puts back the spaces a PDF never stored.
//
// Many PDFs (LaTeX papers, invoices from accounting programs) store no space
// characters at all: words are separated only by a gap in the letters'
// positions. The library's GetPlainText joins the letters and loses the gaps,
// so "Published as a conference paper" came out "Publishedasaconferencepaper".
//
// Here the letters are taken in the PDF's own order (which is reading order
// for nearly every file) and only two things are added: a space where the gap
// to the next letter is wider than a fraction of the font size, and a line
// break where the text moves down to a new line. Letters are never re-sorted,
// so small capitals and superscripts stay where they belong.

import (
	"math"
	"strings"

	"github.com/ledongthuc/pdf"
)

func pageTextByLayout(p pdf.Page) (text string, ok bool) {
	defer func() {
		if recover() != nil {
			text, ok = "", false
		}
	}()
	text = joinLayoutItems(p.Content().Text)
	return text, text != ""
}

// joinLayoutItems turns the page's letters, in the PDF's order, into lines
// of words.
func joinLayoutItems(all []pdf.Text) string {
	// The library adds a line-break item at every change of font, even in
	// the middle of a word ("S" + "ELF" in small capitals). Real line breaks
	// are worked out from the positions below instead.
	var items []pdf.Text
	for _, t := range all {
		if t.S == "" || (strings.TrimSpace(t.S) == "" && strings.ContainsAny(t.S, "\r\n")) {
			continue
		}
		if t.FontSize <= 0 {
			t.FontSize = 10
		}
		items = append(items, t)
	}
	if len(items) == 0 {
		return ""
	}

	// First pass: where each line starts, and the gap before each letter.
	newLine := make([]bool, len(items))
	gap := make([]float64, len(items))
	for i := 1; i < len(items); i++ {
		prev, t := items[i-1], items[i]
		ref := math.Max(t.FontSize, prev.FontSize)
		dy := prev.Y - t.Y
		switch {
		case dy > ref*0.9 || dy < -ref*1.5:
			// Down to the next line (or jumping back up to a new column).
			newLine[i] = true
		case t.X < prev.X-ref*2 && math.Abs(dy) > ref*0.3:
			// Same height band but back at the left: a wrapped line.
			newLine[i] = true
		default:
			gap[i] = t.X - (prev.X + math.Max(prev.W, 0))
		}
	}

	// Some PDFs spread letters apart ("D Y Q - M T 7 6"), so every letter has
	// a gap. A word gap is one clearly wider than the line's usual letter gap.
	var b strings.Builder
	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && !newLine[end] {
			end++
		}
		letterGap := math.Inf(1)
		for i := start + 1; i < end; i++ {
			if gap[i] >= 0 && gap[i] < letterGap {
				letterGap = gap[i]
			}
		}
		if math.IsInf(letterGap, 1) {
			letterGap = 0
		}
		if start > 0 {
			b.WriteByte('\n')
		}
		for i := start; i < end; i++ {
			t := items[i]
			ref := math.Max(t.FontSize, items[max(i-1, 0)].FontSize)
			if i > start && gap[i]-letterGap > ref*0.15 && !strings.HasPrefix(t.S, " ") && !strings.HasSuffix(b.String(), " ") {
				b.WriteByte(' ')
			}
			b.WriteString(t.S)
		}
		start = end
	}
	return b.String()
}

// spaceRatio is the share of spaces among the non-newline characters. Normal
// prose sits around 0.15; text with its spaces lost is near 0.
func spaceRatio(s string) float64 {
	spaces, total := 0, 0
	for _, r := range s {
		if r == '\n' || r == '\r' {
			continue
		}
		total++
		if r == ' ' || r == '\t' {
			spaces++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(spaces) / float64(total)
}
