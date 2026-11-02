package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PageBreakSentinel separates page texts inside tool output before JSON assembly.
const PageBreakSentinel = "\n\n\x1eterminus-page\x1f\n\n"

var (
	rePagesExtracted = regexp.MustCompile(`^\[\d+ pages extracted\]\s*`)
	rePagesOCR       = regexp.MustCompile(`^\[\d+ pages read by OCR\]\s*`)
	reSalvagePrefix  = regexp.MustCompile(`^\[(?:best-effort salvage from malformed PDF|best-effort salvage from weak PDF text layer)\]\s*`)
	reLegacyPageSep  = regexp.MustCompile(`\n\n--- Page \d+ ---\n\n`)
)

func joinPageTexts(pages []string) string {
	var parts []string
	for _, p := range pages {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, PageBreakSentinel)
}

func splitPageTexts(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.Contains(raw, PageBreakSentinel) {
		var out []string
		for _, p := range strings.Split(raw, PageBreakSentinel) {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	if reLegacyPageSep.MatchString(raw) {
		parts := reLegacyPageSep.Split(raw, -1)
		var out []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return []string{raw}
}

// CleanExtractionBody strips machine labels from extracted text and returns warnings.
func CleanExtractionBody(body string) (string, []string) {
	body = strings.TrimSpace(body)
	var warns []string
	if m := rePagesExtracted.FindString(body); m != "" {
		n := parseLeadingPageCount(m)
		if n == 1 {
			warns = append(warns, "1 page read from the PDF text layer")
		} else if n > 1 {
			warns = append(warns, fmt.Sprintf("%d pages read from the PDF text layer", n))
		}
		body = strings.TrimSpace(rePagesExtracted.ReplaceAllString(body, ""))
	}
	if m := rePagesOCR.FindString(body); m != "" {
		n := parseLeadingPageCount(m)
		if n == 1 {
			warns = append(warns, "1 page read by OCR")
		} else if n > 1 {
			warns = append(warns, fmt.Sprintf("%d pages read by OCR", n))
		}
		body = strings.TrimSpace(rePagesOCR.ReplaceAllString(body, ""))
	}
	if reSalvagePrefix.MatchString(body) {
		warns = append(warns, "this PDF was damaged; the text was recovered")
		body = strings.TrimSpace(reSalvagePrefix.ReplaceAllString(body, ""))
	}
	body = strings.TrimSpace(reLegacyPageSep.ReplaceAllString(body, PageBreakSentinel))
	return body, warns
}

func parseLeadingPageCount(prefix string) int {
	digits := regexp.MustCompile(`\d+`).FindString(prefix)
	if digits == "" {
		return 0
	}
	n, _ := strconv.Atoi(digits)
	return n
}

func ocrReadablePageWarning(pageNum int) string {
	return fmt.Sprintf("page %d is a picture and could not be read", pageNum)
}

// cutUTF8 shortens s to at most n bytes without splitting a character, so a
// cut never leaves a broken symbol at the end.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
