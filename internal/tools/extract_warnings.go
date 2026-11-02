package tools

import "strings"

const extractWarningsMarker = "\n\n[terminus_warnings]\n"

func attachExtractWarnings(body string, warnings []string) string {
	if len(warnings) == 0 {
		return body
	}
	var sb strings.Builder
	sb.WriteString(body)
	sb.WriteString(extractWarningsMarker)
	for _, w := range warnings {
		sb.WriteString("- ")
		sb.WriteString(w)
		sb.WriteString("\n")
	}
	return sb.String()
}

func splitExtractWarnings(raw string) (string, []string) {
	idx := strings.Index(raw, extractWarningsMarker)
	if idx < 0 {
		return raw, nil
	}
	body := raw[:idx]
	rest := strings.TrimSpace(raw[idx+len(extractWarningsMarker):])
	if rest == "" {
		return body, nil
	}
	var warnings []string
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			warnings = append(warnings, strings.TrimPrefix(line, "- "))
		}
	}
	return body, warnings
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
