package docpipeline

import (
	"regexp"
	"strings"
)

var (
	reTitleLetters = regexp.MustCompile(`\pL`)
	reDear         = regexp.MustCompile(`(?im)\bDear\s+([A-Za-z][A-Za-z .'\-]{1,60})`)
	reSignOff      = regexp.MustCompile(`(?im)\b(Sincerely|Regards|Best regards|Yours truly|Respectfully),?\s*$`)
	reSubject      = regexp.MustCompile(`(?im)^Subject:\s*(.+)$`)
	reStatement    = regexp.MustCompile(`(?i)\bstatement\b`)
	reOpenBal      = regexp.MustCompile(`(?i)(?:opening|beginning)\s+balance[:\s]*\$?\s*([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{2})|[0-9]+(?:\.[0-9]{2}))`)
	reCloseBal     = regexp.MustCompile(`(?i)(?:closing|ending)\s+balance[:\s]*\$?\s*([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{2})|[0-9]+(?:\.[0-9]{2}))`)
	reBankName     = regexp.MustCompile(`(?im)^([A-Z][A-Za-z0-9 &',.\-]{4,60})\s*$`)
	reReceiptWord  = regexp.MustCompile(`(?i)\breceipt\b`)
)

// ClassifyDocument picks document_type and adds type-specific fields when found on the page.
func ClassifyDocument(rawText string, fields map[string]string, medicalPack bool) map[string]string {
	out := make(map[string]string)
	for k, v := range fields {
		out[k] = v
	}
	docType := "document"
	if medicalPack && hasMedicalSignal(fields) {
		docType = "medical_record"
	} else if documentLooksLikeBill(fields) {
		if strings.TrimSpace(fields["invoice_number"]) != "" {
			docType = "invoice"
		} else if reReceiptWord.MatchString(rawText) {
			docType = "receipt"
		} else {
			docType = "invoice"
		}
	} else if bankStatementFields(rawText, out) {
		docType = "bank_statement"
	} else if letterFields(rawText, out) {
		docType = "letter"
	} else {
		documentMetaFields(rawText, out)
	}
	out["document_type"] = docType
	return out
}

func hasMedicalSignal(m map[string]string) bool {
	for _, k := range []string{"patient_name", "mrn", "admitted", "discharged", "attending", "npi"} {
		if strings.TrimSpace(m[k]) != "" {
			return true
		}
	}
	return false
}

func bankStatementFields(raw string, out map[string]string) bool {
	if !reStatement.MatchString(raw) {
		return false
	}
	open := reOpenBal.FindStringSubmatch(raw)
	close := reCloseBal.FindStringSubmatch(raw)
	if open == nil && close == nil {
		return false
	}
	if open != nil {
		out["opening_balance"] = strings.TrimSpace(open[1])
	}
	if close != nil {
		out["closing_balance"] = strings.TrimSpace(close[1])
	}
	lines := strings.Split(raw, "\n")
	for _, line := range lines[:min(12, len(lines))] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if reStatement.MatchString(line) && reBankName.MatchString(line) {
			out["bank"] = strings.TrimSpace(line)
			break
		}
		if strings.Contains(strings.ToUpper(line), "BANK") && len(line) < 80 {
			out["bank"] = line
			break
		}
	}
	if m := regexp.MustCompile(`(?i)statement\s+(?:for|period)?\s*([A-Za-z]+ \d{4}|\d{1,2}/\d{1,2}/\d{2,4})`).FindStringSubmatch(raw); len(m) > 1 {
		out["period"] = strings.TrimSpace(m[1])
	}
	return true
}

func letterFields(raw string, out map[string]string) bool {
	if !reDear.MatchString(raw) || !reSignOff.MatchString(raw) {
		return false
	}
	if m := reDear.FindStringSubmatch(raw); len(m) > 1 {
		out["to"] = strings.TrimSpace(m[1])
	}
	if sub := reSubject.FindStringSubmatch(raw); len(sub) > 1 {
		out["subject"] = strings.TrimSpace(sub[1])
	}
	lines := strings.Split(raw, "\n")
	for _, line := range lines[:min(8, len(lines))] {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(strings.ToLower(line), "dear") {
			out["from"] = line
			break
		}
	}
	return true
}

func documentMetaFields(raw string, out map[string]string) {
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip page numbers, single letters and other stray marks.
		if len(reTitleLetters.FindAllString(line, -1)) < 3 {
			continue
		}
		out["title"] = line
		break
	}
	if m := regexp.MustCompile(`(?i)(?:author|by)\s*:\s*([^\n]{2,80})`).FindStringSubmatch(raw); len(m) > 1 {
		out["author"] = strings.TrimSpace(m[1])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
