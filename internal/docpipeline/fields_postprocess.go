package docpipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var titleLikeVendor = regexp.MustCompile(`(?i)(report|summary|discharge|imaging|diagnostic|prescription|statement|invoice|receipt)`)

var postprocessDateKeys = []struct {
	key      string
	original string
}{
	{"date", "date_on_document"},
	{"due_date", "due_date_on_document"},
	{"dob", "dob_on_document"},
	{"dos", "dos_on_document"},
	{"admitted", "admitted_on_document"},
	{"discharged", "discharged_on_document"},
}

// PostProcessFields trims values, normalizes dates, and drops vendor on non-bills.
// Returns warnings (for example ambiguous US-style dates).
func PostProcessFields(fieldMap map[string]string) []string {
	var warns []string
	for k, v := range fieldMap {
		fieldMap[k] = trimFieldValue(v)
	}
	for _, pair := range postprocessDateKeys {
		if w := normalizeDateField(fieldMap, pair.key, pair.original); w != "" {
			warns = append(warns, w)
		}
	}
	if !documentLooksLikeBill(fieldMap) {
		delete(fieldMap, "vendor")
	} else if isDocumentTitleVendor(fieldMap["vendor"]) {
		delete(fieldMap, "vendor")
	}
	return warns
}

func trimFieldValue(v string) string {
	v = strings.TrimSpace(v)
	for strings.HasSuffix(v, "  .") || strings.HasSuffix(v, " .") {
		v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(v, "."), "."))
	}
	return strings.TrimSpace(v)
}

func documentLooksLikeBill(m map[string]string) bool {
	if strings.TrimSpace(m["invoice_number"]) != "" {
		return true
	}
	if strings.TrimSpace(m["total"]) != "" {
		return true
	}
	return false
}

func isDocumentTitleVendor(v string) bool {
	up := strings.ToUpper(strings.TrimSpace(v))
	if up == "" {
		return false
	}
	if strings.Contains(up, "SUPPLY") || strings.Contains(up, "ELECTRIC") ||
		strings.Contains(up, "INC.") || strings.Contains(up, " LLC") {
		return false
	}
	if titleLikeVendor.MatchString(up) {
		return true
	}
	if strings.HasSuffix(up, "  .") || strings.HasSuffix(up, " .") {
		return true
	}
	return false
}

func normalizeDateField(m map[string]string, key, originalKey string) string {
	raw := strings.TrimSpace(m[key])
	if raw == "" {
		return ""
	}
	norm, ambig := normalizeDateString(raw)
	if norm != "" && norm != raw {
		m[originalKey] = raw
		m[key] = norm
	} else if norm != "" {
		m[key] = norm
	}
	return ambig
}

func normalizeDateString(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.Format("2006-01-02"), ""
	}
	for _, layout := range []string{"January 2, 2006", "Jan 2, 2006", "Jan. 2, 2006"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01-02"), ""
		}
	}
	if m := regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{2,4})$`).FindStringSubmatch(raw); len(m) == 4 {
		mo, _ := strconv.Atoi(m[1])
		day, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if y < 100 {
			y += 2000
		}
		t := time.Date(y, time.Month(mo), day, 0, 0, 0, 0, time.UTC)
		warn := ""
		if mo <= 12 && day <= 12 && mo != day {
			warn = fmt.Sprintf("date %q was read as month/day/year (US order); change it on the document if that is wrong", raw)
		}
		return t.Format("2006-01-02"), warn
	}
	return raw, ""
}
