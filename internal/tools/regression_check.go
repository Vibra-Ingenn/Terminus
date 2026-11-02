package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RegressionSummary counts parse outcomes for a folder of PDFs.
type RegressionSummary struct {
	Total          int
	Clean          int
	WithWarnings   int
	Failed         int
	Failures       []string
}

// CheckPDFDirectory parses each PDF and applies regression rules from the launch card.
func CheckPDFDirectory(dir string) (RegressionSummary, error) {
	sum := RegressionSummary{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return sum, err
	}
	r := NewRegistry()
	for _, ent := range entries {
		if ent.IsDir() || strings.ToLower(filepath.Ext(ent.Name())) != ".pdf" {
			continue
		}
		sum.Total++
		path := filepath.Join(dir, ent.Name())
		var res ToolResult
		func() {
			defer func() {
				if p := recover(); p != nil {
					res = ToolResult{
						ToolName: "document_parse",
						Success:  false,
						Error:    fmt.Sprintf("parser crashed: %v", p),
					}
				}
			}()
			res = r.Dispatch(ToolCall{Name: "document_parse", Arguments: map[string]any{"path": path, "include_raw": "true"}})
		}()
		if !res.Success {
			sum.Failed++
			sum.Failures = append(sum.Failures, fmt.Sprintf("%s: %s", ent.Name(), res.Error))
			continue
		}
		var parsed documentParseOutput
		if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
			sum.Failed++
			sum.Failures = append(sum.Failures, fmt.Sprintf("%s: bad JSON: %v", ent.Name(), err))
			continue
		}
		text := parsed.RawText
		for _, p := range parsed.Pages {
			text += "\n" + p.Text
		}
		if strings.Contains(text, "\uFFFD") || strings.Contains(text, "â€") || strings.Contains(text, "Ã©") {
			sum.Failed++
			sum.Failures = append(sum.Failures, fmt.Sprintf("%s: unreadable characters in text", ent.Name()))
			continue
		}
		hasText := strings.TrimSpace(text) != ""
		hasPageText := false
		for _, p := range parsed.Pages {
			if strings.TrimSpace(p.Text) != "" {
				hasPageText = true
				break
			}
		}
		if !hasText && !hasPageText {
			// Nothing was read: that's a failure, even when a warning says why.
			reason := strings.Join(parsed.Warnings, "; ")
			if reason == "" {
				reason = "no text and no warning"
			}
			sum.Failed++
			sum.Failures = append(sum.Failures, fmt.Sprintf("%s: %s", ent.Name(), reason))
			continue
		}
		if len(parsed.Warnings) > 0 {
			sum.WithWarnings++
		} else {
			sum.Clean++
		}
	}
	return sum, nil
}
