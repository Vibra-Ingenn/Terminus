package tools

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const pdfReplacementChar = '\uFFFD'

var spaceBeforePunct = regexp.MustCompile(`\s+([,.;:!?])`)
var multiSpace = regexp.MustCompile(`[ \t]{2,}`)

// smallCapsSplit finds a heading in small capitals whose big first letter
// came out on a line of its own ("B" / "ACKHAUL AND"), because the PDF draws
// it in a separate font.
var smallCapsSplit = regexp.MustCompile(`(?m)^([A-Z])[ \t]*\n[ \t]*([A-Z]{2,})`)

// CleanPDFExtractedText fixes mojibake, strips unreadable symbol placeholders, and tidies spacing.
func CleanPDFExtractedText(text string) (string, []string) {
	var warnings []string
	text = repairUTF8ReadAsLatin1(text)
	removed := 0
	text = stripReplacementChars(text, &removed)
	text = smallCapsSplit.ReplaceAllString(text, "$1$2")
	text = multiSpace.ReplaceAllString(text, " ")
	text = spaceBeforePunct.ReplaceAllString(text, "$1")
	text = strings.TrimSpace(text)
	if removed > 0 {
		warnings = append(warnings, fmt.Sprintf("some symbols in this PDF couldn't be read as text (%d)", removed))
	}
	return text, warnings
}

func stripReplacementChars(s string, count *int) string {
	var b strings.Builder
	for _, r := range s {
		if r == pdfReplacementChar {
			*count++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// repairUTF8ReadAsLatin1 fixes text where UTF-8 bytes were interpreted as Windows-1252 / Latin-1.
var commonMojibake = strings.NewReplacer(
	"â€¦", "…",
	"â€”", "—",
	"â€œ", "“",
	"â€\u009d", "”",
	"â€™", "’",
	"Ã©", "é",
)

func repairUTF8ReadAsLatin1(s string) string {
	if s == "" {
		return s
	}
	fixed := commonMojibake.Replace(s)
	if fixed != s {
		return fixed
	}
	if !strings.Contains(s, "â") {
		return s
	}
	bytes := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			return s
		}
		bytes = append(bytes, byte(r))
	}
	if !utf8.Valid(bytes) {
		return s
	}
	decoded := string(bytes)
	if strings.Count(decoded, string(pdfReplacementChar)) > strings.Count(s, string(pdfReplacementChar)) {
		return s
	}
	if strings.Contains(decoded, "…") || strings.Contains(decoded, "—") ||
		strings.Contains(decoded, "“") || strings.Contains(decoded, "é") {
		return decoded
	}
	if looksMoreReadable(decoded, s) {
		return decoded
	}
	return s
}

func looksMoreReadable(decoded, original string) bool {
	badOrig := strings.Count(original, "â") + strings.Count(original, "€")
	badDec := strings.Count(decoded, "â") + strings.Count(decoded, "€")
	if badDec < badOrig {
		return true
	}
	for _, r := range decoded {
		if unicode.Is(unicode.Latin, r) && r > 127 {
			return true
		}
	}
	return false
}
