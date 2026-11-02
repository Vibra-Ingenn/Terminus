// Package docpipeline implements the Stage 0-4 layered document perception
// pipeline described in docs/DOCUMENT_PARSING_GUIDE.md.
//
// Core principle: ambiguity must never reach execution.
// Each stage narrows uncertainty — no stage forces premature certainty.
//
//	Stage 0 — Layout / OCR extraction  (caller-supplied raw text)
//	Stage 1 — Candidate generator      (regex + heuristics, multi-hypothesis)
//	Stage 2 — Verifier                 (consistency check only, never rewrites)
//	Stage 3 — Deterministic resolver   (merge, dedup, schema-enforce)
//	Stage 4 — Structured output        (ready for tool dispatch, no LLM needed)
package docpipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// Candidate is one extracted hypothesis for a field.
// The model (or caller) never picks a single answer — it proposes candidates.
type Candidate struct {
	Field      string  `json:"field"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"` // 0.0–1.0
	Span       [2]int  `json:"span"`       // byte offsets in source text
	Source     string  `json:"source"`     // which pattern/method produced this
}

// ExtractionResult is the output of Stage 1.
// Never commits — always a set of candidates.
type ExtractionResult struct {
	Candidates []Candidate `json:"candidates"`
	RawText    string      `json:"raw_text"`
	Warnings   []string    `json:"warnings,omitempty"`
}

// VerificationStatus classifies how clean the extraction is.
type VerificationStatus string

const (
	StatusValid     VerificationStatus = "valid"
	StatusAmbiguous VerificationStatus = "ambiguous"
	StatusInvalid   VerificationStatus = "invalid"
)

// VerificationResult is the output of Stage 2.
// Never invents new entities — only reports consistency issues.
type VerificationResult struct {
	Status    VerificationStatus `json:"status"`
	Conflicts []string           `json:"conflicts,omitempty"`
	Approved  []Candidate        `json:"approved,omitempty"` // only when valid
}

// ResolvedEntity is a single field after Stage 3 deterministic resolution.
type ResolvedEntity struct {
	Field       string   `json:"field"`
	Value       string   `json:"value"`
	Confidence  float64  `json:"confidence"`
	Sources     []string `json:"sources"`
	NeedsReview bool     `json:"needs_review,omitempty"`
}

// PipelineResult is the final output of Stage 3 — ready for Stage 4 execution.
type PipelineResult struct {
	Entities      []ResolvedEntity `json:"entities"`
	AmbiguousKeys []string         `json:"ambiguous_keys,omitempty"`
	Summary       string           `json:"summary"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Stage 1 — Candidate generator
// ─────────────────────────────────────────────────────────────────────────────

// patternDef associates a regex pattern with a field name and base confidence.
type patternDef struct {
	field      string
	pattern    *regexp.Regexp
	confidence float64
	source     string
}

// IsMedicalField returns true if the field name is specific to the medical pack.
func IsMedicalField(field string) bool {
	switch strings.ToLower(field) {
	case "mrn", "patient_name", "icd10", "npi", "provider_name", "dos",
		"admitted", "discharged", "attending":
		return true
	default:
		return false
	}
}

// HasMedicalPack checks if the medical pack is enabled in the provided packs
// or via the TERMINUS_PACKS environment variable.
func HasMedicalPack(packs []string) bool {
	for _, p := range packs {
		for _, part := range strings.Split(p, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "medical") {
				return true
			}
		}
	}
	if v := os.Getenv("TERMINUS_PACKS"); v != "" {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "medical") {
				return true
			}
		}
	}
	return false
}

var generalPatterns = []patternDef{
	// Phone numbers
	{
		field:      "phone",
		pattern:    regexp.MustCompile(`(?:^|[^\d])(\(?\d{3}\)?[-.\s]\d{3}[-.\s]\d{4})\b`),
		confidence: 0.82,
		source:     "phone",
	},
	// Email
	{
		field:      "email",
		pattern:    regexp.MustCompile(`\b([a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,})\b`),
		confidence: 0.88,
		source:     "email",
	},
	// Zip code
	{
		field:      "zip",
		pattern:    regexp.MustCompile(`\b(\d{5}(?:-\d{4})?)\b`),
		confidence: 0.65,
		source:     "zip",
	},
	// SSN — highest confidence on canonical form
	{
		field:      "ssn",
		pattern:    regexp.MustCompile(`\b(\d{3}-\d{2}-\d{4})\b`),
		confidence: 0.90,
		source:     "ssn_canonical",
	},
	// SSN — no-dash form, only next to an SSN label. A bare 9-digit number is
	// far more often a part, order or account number (a telecom manual came
	// out with 30 "Social Security numbers").
	{
		field:      "ssn",
		pattern:    regexp.MustCompile(`(?i)\b(?:ssn|social\s+security(?:\s+(?:no\.?|number|#))?)\s*[:#.]?\s*(\d{9})\b`),
		confidence: 0.55,
		source:     "ssn_nodash",
	},
	// Date of birth
	{
		field:      "dob",
		pattern:    regexp.MustCompile(`(?i)(?:dob|date\s+of\s+birth|born)[:\s]+(\d{1,2}[/-]\d{1,2}[/-]\d{2,4})`),
		confidence: 0.85,
		source:     "dob_labeled",
	},
	{
		field:      "dob",
		pattern:    regexp.MustCompile(`\b(\d{1,2}/\d{1,2}/\d{4})\b`),
		confidence: 0.55,
		source:     "dob_slash",
	},
	// Invoice / receipt number (must contain a digit; see acceptInvoiceNumber)
	{
		field:      "invoice_number",
		pattern:    regexp.MustCompile(`(?i)(?:invoice|receipt)(?:\s*(?:#|no\.?|num|number))?[:\s#]+([A-Z0-9][A-Z0-9\-]{1,28})\b`),
		confidence: 0.88,
		source:     "invoice_number_labeled",
	},
	{
		field:      "invoice_number",
		pattern:    regexp.MustCompile(`(?im)^invoice\s+([A-Z0-9][A-Z0-9\-]{1,28})\b`),
		confidence: 0.87,
		source:     "invoice_number_inline",
	},
	{
		field:      "invoice_number",
		pattern:    regexp.MustCompile(`(?i)inv\s*#\s*([0-9]{3,})\b`),
		confidence: 0.86,
		source:     "invoice_number_hash",
	},
	{
		field:      "invoice_number",
		pattern:    regexp.MustCompile(`(?i)receipt\s+no\.\s+([A-Z0-9\-]{3,})\b`),
		confidence: 0.86,
		source:     "receipt_number_labeled",
	},
	// Vendor / merchant / biller
	{
		field:      "vendor",
		pattern:    regexp.MustCompile(`(?im)^(?:vendor|merchant|seller|store|company|biller|from)[ \t]*:[ \t]*([A-Za-z0-9&',.\- ]{2,50})`),
		confidence: 0.80,
		source:     "vendor_labeled",
	},
	{
		field:      "vendor",
		pattern:    regexp.MustCompile(`(?im)^([A-Za-z0-9][A-Za-z0-9&',.\- ]{2,50})\s*-\s*(?:INVOICE|RECEIPT|BILL)\b`),
		confidence: 0.78,
		source:     "vendor_header",
	},
	{
		field:      "vendor",
		pattern:    regexp.MustCompile(`(?im)^([A-Z][A-Z0-9 &',.\-]{2,48})\s*$`),
		confidence: 0.74,
		source:     "vendor_banner",
	},
	{
		field:      "vendor",
		pattern:    regexp.MustCompile(`(?im)^([A-Z][a-z]+(?:[ \t]+[A-Za-z][a-z]+){0,3})\s*\nInv`),
		confidence: 0.76,
		source:     "vendor_title_line",
	},
	// Total amount due (subtotal matches are dropped in acceptTotalAmount)
	{
		field:      "total",
		pattern:    regexp.MustCompile(`(?i)(?:total\s+due|amount\s+due|balance\s+due|grand\s+total)[:\s]*\$?\s*([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{2})|[0-9]+(?:\.[0-9]{2}))\b`),
		confidence: 0.90,
		source:     "total_due_labeled",
	},
	{
		field:      "total",
		pattern:    regexp.MustCompile(`(?i)total\s+charged[^$\n]*\$?\s*([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{2})|[0-9]+(?:\.[0-9]{2}))\b`),
		confidence: 0.88,
		source:     "total_charged",
	},
	{
		field:      "total",
		pattern:    regexp.MustCompile(`(?im)^[^\S\n]*\btotal\b[:\s]+\$?\s*([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{2})|[0-9]+(?:\.[0-9]{2}))\b`),
		confidence: 0.84,
		source:     "total_labeled",
	},
	// General document date
	{
		field:      "date",
		pattern:    regexp.MustCompile(`(?i)(?:issued|date|dated|invoice\s+date|receipt\s+date|statement\s+date)[:\s]+(\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|\d{4}-\d{2}-\d{2}|[A-Za-z]+\s+\d{1,2},\s+\d{4})`),
		confidence: 0.82,
		source:     "date_labeled",
	},
	{
		field:      "date",
		pattern:    regexp.MustCompile(`(?im)^Inv\s*#\S+\s+(\d{1,2}/\d{1,2}/\d{4})\b`),
		confidence: 0.80,
		source:     "date_after_invoice",
	},
	{
		field:      "due_date",
		pattern:    regexp.MustCompile(`(?i)(?:payment\s+due|due\s+date|due)[:\s]+(\d{4}-\d{2}-\d{2})`),
		confidence: 0.82,
		source:     "due_date_labeled",
	},
	{
		field:      "due_date",
		pattern:    regexp.MustCompile(`(?i)\bby\s+(\d{4}-\d{2}-\d{2})\b`),
		confidence: 0.78,
		source:     "due_date_by",
	},
	// Customer on invoice (seller is vendor, not name)
	{
		field:      "customer",
		pattern:    regexp.MustCompile(`(?im)^(?:customer|bill\s+to|sold\s+to)[ \t]*:[ \t]*([A-Z][A-Za-z0-9&',.\- ]{2,60})`),
		confidence: 0.78,
		source:     "customer_labeled",
	},
	{
		field:      "customer",
		pattern:    regexp.MustCompile(`(?im)^account[ \t]*:[ \t]*([A-Z][A-Za-z0-9&',.\- ]{2,60})`),
		confidence: 0.76,
		source:     "customer_account",
	},
	// General person / contact name (not bill-to; that is customer)
	{
		field:      "name",
		pattern:    regexp.MustCompile(`(?im)^(?:name|client|contact)[ \t]*:[ \t]*([A-Z][A-Za-z'\-]+(?:[ \t]+(?:[A-Z][A-Za-z'\-]+|[A-Z]\.?|[A-Za-z0-9]+)){1,3})`),
		confidence: 0.75,
		source:     "name_labeled",
	},
	// Address
	{
		field:      "address",
		pattern:    regexp.MustCompile(`(?im)^(?:address|ship\s+to|bill\s+to\s+address)[ \t]*:[ \t]*([0-9]+\s+[A-Za-z0-9\s,.\-#]{5,60})`),
		confidence: 0.75,
		source:     "address_labeled",
	},
}

var medicalPatterns = []patternDef{
	// MRN — labeled
	{
		field:      "mrn",
		pattern:    regexp.MustCompile(`(?i)(?:mrn|medical\s+record(?:\s+number)?|patient\s+id)[:\s#]+([A-Z0-9\-]{4,16})\b`),
		confidence: 0.80,
		source:     "mrn_labeled",
	},
	// ICD-10 codes
	{
		field:      "icd10",
		pattern:    regexp.MustCompile(`\b([A-Z]\d{2}(?:\.\d{1,4})?)\b`),
		confidence: 0.72,
		source:     "icd10",
	},
	// Patient name — labeled (stop at the next field label on the same line)
	{
		field:      "patient_name",
		pattern:    regexp.MustCompile(`(?im)^(?:patient(?:[ \t]+name)?|pt(?:[ \t]+name)?)[ \t]*:[ \t]*(.+?)(?:\s{2,}(?:MRN|DOB|Sex|Acct|Account|ID)\s*:|$)`),
		confidence: 0.78,
		source:     "patient_name_labeled",
	},
	{
		field:      "admitted",
		pattern:    regexp.MustCompile(`(?i)admitted\s*:\s*(\d{1,2}/\d{1,2}/\d{2,4})`),
		confidence: 0.85,
		source:     "admitted_labeled",
	},
	{
		field:      "discharged",
		pattern:    regexp.MustCompile(`(?i)discharged\s*:\s*(\d{1,2}/\d{1,2}/\d{2,4})`),
		confidence: 0.85,
		source:     "discharged_labeled",
	},
	{
		field:      "attending",
		pattern:    regexp.MustCompile(`(?i)attending\s*:\s*(Dr\.?\s+.+?)(?:\s{2,}|\s+Service\s*:|$)`),
		confidence: 0.82,
		source:     "attending_labeled",
	},
	// NPI — National Provider Identifier
	{
		field:      "npi",
		pattern:    regexp.MustCompile(`(?i)(?:npi|provider\s+id)[:\s#]+(\d{10})\b`),
		confidence: 0.88,
		source:     "npi_labeled",
	},
	// Provider name — labeled
	{
		field:      "provider_name",
		pattern:    regexp.MustCompile(`(?im)^(?:provider|physician|doctor|dr\.?)[ \t]*:[ \t]*(?:dr\.?\s+)?([A-Z][A-Za-z'\-]+(?:[ \t]+(?:[A-Z][A-Za-z'\-]+|[A-Z]\.?)){1,3})`),
		confidence: 0.78,
		source:     "provider_name_labeled",
	},
	// Date of service — labeled
	{
		field:      "dos",
		pattern:    regexp.MustCompile(`(?i)(?:dos|date\s+of\s+service|service\s+date)[:\s]+(\d{1,2}[/-]\d{1,2}[/-]\d{2,4})`),
		confidence: 0.85,
		source:     "dos_labeled",
	},
}

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func acceptInvoiceNumber(value string) bool {
	return hasDigit(value) && !strings.EqualFold(value, "file")
}

func acceptTotalAmount(text string, matchStart int) bool {
	if matchStart >= 3 {
		prefix := strings.ToLower(text[matchStart-3 : matchStart])
		if prefix == "sub" {
			return false
		}
	}
	return true
}

func trimPatientName(value string) string {
	return strings.TrimSpace(value)
}

func acceptZipCode(text string, start int) bool {
	if start > 0 && text[start-1] == '-' {
		return false
	}
	return true
}

func acceptVendorBanner(value string) bool {
	if !mostlyUppercase(value) {
		return false
	}
	up := strings.ToUpper(value)
	if strings.Contains(up, "DISCHARGE") || strings.Contains(up, "SUMMARY") ||
		strings.Contains(up, "STATEMENT") || strings.Contains(up, "HOSPITAL") ||
		strings.Contains(up, "REPORT") || strings.Contains(up, "IMAGING") ||
		strings.Contains(up, "DIAGNOSTIC") || strings.Contains(up, "PRESCRIPTION") {
		return false
	}
	trim := strings.TrimSpace(value)
	if strings.HasSuffix(trim, "  .") || strings.HasSuffix(trim, " .") {
		return false
	}
	return true
}

func mostlyUppercase(s string) bool {
	letters, upper := 0, 0
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			letters++
		case r >= 'A' && r <= 'Z':
			letters++
			upper++
		}
	}
	if letters == 0 {
		return false
	}
	return upper*3 >= letters*2
}

// ExtractCandidates runs Stage 1: extracts multiple candidate values per field.
// Never commits — always returns a full candidate set.
// An optional packs list can enable pack-specific patterns (e.g. "medical").
func ExtractCandidates(text string, packs ...string) ExtractionResult {
	result := ExtractionResult{RawText: text}

	patterns := make([]patternDef, len(generalPatterns))
	copy(patterns, generalPatterns)
	if HasMedicalPack(packs) {
		patterns = append(patterns, medicalPatterns...)
	}

	for _, pd := range patterns {
		matches := pd.pattern.FindAllStringSubmatchIndex(text, -1)
		for _, m := range matches {
			// Use capture group 1 if present, else full match
			start, end := m[0], m[1]
			if len(m) >= 4 && m[2] >= 0 {
				start, end = m[2], m[3]
			}
			value := strings.TrimSpace(text[start:end])
			if value == "" {
				continue
			}
			switch pd.field {
			case "invoice_number":
				if !acceptInvoiceNumber(value) {
					continue
				}
			case "total":
				if !acceptTotalAmount(text, m[0]) {
					continue
				}
			case "patient_name":
				value = trimPatientName(value)
				if value == "" {
					continue
				}
			case "vendor":
				if pd.source == "vendor_banner" && !acceptVendorBanner(value) {
					continue
				}
			case "zip":
				if !acceptZipCode(text, start) {
					continue
				}
			}
			result.Candidates = append(result.Candidates, Candidate{
				Field:      pd.field,
				Value:      value,
				Confidence: pd.confidence,
				Span:       [2]int{start, end},
				Source:     pd.source,
			})
		}
	}

	if len(result.Candidates) == 0 {
		result.Warnings = append(result.Warnings, "no structured fields detected in text")
	}

	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// Stage 2 — Verifier
// ─────────────────────────────────────────────────────────────────────────────

// confidenceThreshold is the minimum confidence to consider a candidate reliable.
const confidenceThreshold = 0.60

// Verify runs Stage 2: checks internal consistency of candidates.
// Never invents new values — only reports conflicts and approves clean sets.
func Verify(r ExtractionResult) VerificationResult {
	var conflicts []string

	// Group by field
	byField := make(map[string][]Candidate)
	for _, c := range r.Candidates {
		byField[c.Field] = append(byField[c.Field], c)
	}

	// Check for conflicting high-confidence values in same field
	for field, cands := range byField {
		// Deduplicate by normalized value
		seen := make(map[string]bool)
		var unique []Candidate
		for _, c := range cands {
			norm := strings.ToLower(strings.ReplaceAll(c.Value, " ", ""))
			if !seen[norm] {
				seen[norm] = true
				unique = append(unique, c)
			}
		}
		byField[field] = unique
		if len(unique) > 3 {
			// A long list helps nobody; name a few.
			vals := make([]string, 3)
			for i, c := range unique[:3] {
				vals[i] = fmt.Sprintf("%q", c.Value)
			}
			conflicts = append(conflicts, fmt.Sprintf(
				"field %q has %d distinct candidates, none clear (e.g. %s)",
				field, len(unique), strings.Join(vals, ", "),
			))
		} else if len(unique) > 1 {
			vals := make([]string, len(unique))
			for i, c := range unique {
				vals[i] = fmt.Sprintf("%q(%.0f%%)", c.Value, c.Confidence*100)
			}
			conflicts = append(conflicts, fmt.Sprintf(
				"field %q has %d distinct candidates: %s",
				field, len(unique), strings.Join(vals, ", "),
			))
		}
	}

	// Check for overlapping spans (same characters claimed by different fields)
	type span struct {
		start, end int
		field      string
		value      string
	}
	var spans []span
	for _, c := range r.Candidates {
		spans = append(spans, span{c.Span[0], c.Span[1], c.Field, c.Value})
	}
	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			a, b := spans[i], spans[j]
			// Overlapping spans across different fields signal potential confusion
			if a.field != b.field && a.start < b.end && b.start < a.end {
				conflicts = append(conflicts, fmt.Sprintf(
					"span overlap: %q(%s) overlaps %q(%s) at [%d,%d]",
					a.value, a.field, b.value, b.field, b.start, a.end,
				))
			}
		}
	}

	// Collect all candidates above threshold as approved set
	var approved []Candidate
	for _, cands := range byField {
		for _, c := range cands {
			if c.Confidence >= confidenceThreshold {
				approved = append(approved, c)
			}
		}
	}

	switch {
	case len(approved) == 0 && len(r.Candidates) > 0:
		return VerificationResult{
			Status:    StatusInvalid,
			Conflicts: append(conflicts, "all candidates below confidence threshold"),
		}
	case len(conflicts) > 0:
		return VerificationResult{
			Status:    StatusAmbiguous,
			Conflicts: conflicts,
			Approved:  approved,
		}
	default:
		return VerificationResult{
			Status:   StatusValid,
			Approved: approved,
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Stage 3 — Deterministic resolver
// ─────────────────────────────────────────────────────────────────────────────

func pickFieldCandidate(field string, unique []Candidate) (Candidate, bool) {
	if len(unique) == 0 {
		return Candidate{}, false
	}
	if len(unique) == 1 {
		return unique[0], false
	}
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(s, " ", ""))
	}
	allSame := true
	for i := 1; i < len(unique); i++ {
		if norm(unique[i].Value) != norm(unique[0].Value) {
			allSame = false
			break
		}
	}
	if allSame {
		return unique[0], false
	}

	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Confidence != unique[j].Confidence {
			return unique[i].Confidence > unique[j].Confidence
		}
		return unique[i].Span[0] < unique[j].Span[0]
	})

	switch field {
	case "total", "invoice_number", "date", "due_date":
		last := unique[len(unique)-1]
		if last.Confidence >= unique[0].Confidence-0.02 {
			return last, false
		}
		return unique[0], true
	default:
		return unique[0], true
	}
}

// Resolve runs Stage 3: deterministic merge and dedup of approved candidates.
// For each field, picks the highest-confidence non-overlapping candidate.
// Ambiguous fields (multiple distinct high-confidence values) are flagged.
func Resolve(vr VerificationResult) PipelineResult {
	// Group approved candidates by field
	byField := make(map[string][]Candidate)
	for _, c := range vr.Approved {
		byField[c.Field] = append(byField[c.Field], c)
	}

	var entities []ResolvedEntity
	var ambiguousKeys []string

	// Sort field names for deterministic output
	fields := make([]string, 0, len(byField))
	for f := range byField {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	for _, field := range fields {
		cands := byField[field]
		// Sort by confidence descending
		sort.Slice(cands, func(i, j int) bool {
			return cands[i].Confidence > cands[j].Confidence
		})

		// Deduplicate by value
		seen := make(map[string]bool)
		var unique []Candidate
		for _, c := range cands {
			norm := strings.ToLower(strings.ReplaceAll(c.Value, " ", ""))
			if !seen[norm] {
				seen[norm] = true
				unique = append(unique, c)
			}
		}

		best, needsReview := pickFieldCandidate(field, unique)
		if needsReview {
			ambiguousKeys = append(ambiguousKeys, field)
		}

		sources := make([]string, len(unique))
		for i, c := range unique {
			sources[i] = c.Source
		}

		entities = append(entities, ResolvedEntity{
			Field:       field,
			Value:       best.Value,
			Confidence:  best.Confidence,
			Sources:     sources,
			NeedsReview: needsReview,
		})
	}

	// Build summary
	var sb strings.Builder
	fmt.Fprintf(&sb, "Resolved %d field(s)", len(entities))
	if len(ambiguousKeys) > 0 {
		fmt.Fprintf(&sb, "; %d ambiguous (needs review): %s",
			len(ambiguousKeys), strings.Join(ambiguousKeys, ", "))
	}
	if vr.Status == StatusAmbiguous {
		sb.WriteString("; verification reported conflicts")
	}

	return PipelineResult{
		Entities:      entities,
		AmbiguousKeys: ambiguousKeys,
		Summary:       sb.String(),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Stage 4 — Structured output builder
// ─────────────────────────────────────────────────────────────────────────────

// ToFieldMap converts resolved entities to a simple string map for tool dispatch.
// Only includes non-ambiguous fields (or all if includeAmbiguous is true).
func (pr PipelineResult) ToFieldMap(includeAmbiguous bool) map[string]string {
	m, _ := pr.ToProcessedFieldMap(includeAmbiguous)
	return m
}

// ToProcessedFieldMap runs post-processing (dates, vendor rules) and returns warnings.
func (pr PipelineResult) ToProcessedFieldMap(includeAmbiguous bool) (map[string]string, []string) {
	out := make(map[string]string, len(pr.Entities))
	ambiguous := make(map[string]bool, len(pr.AmbiguousKeys))
	for _, k := range pr.AmbiguousKeys {
		ambiguous[k] = true
	}
	for _, e := range pr.Entities {
		if !includeAmbiguous && ambiguous[e.Field] {
			continue
		}
		out[e.Field] = e.Value
	}
	warns := PostProcessFields(out)
	return out, warns
}

// AIEngine is the cognitive inference backend interface.
type AIEngine interface {
	Predict(ctx context.Context, prompt string) (string, error)
}

// DocumentSegment represents one logical record/section within a larger document.
type DocumentSegment struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Run — convenience function that executes Stage 1→2→3 in sequence
// ─────────────────────────────────────────────────────────────────────────────

// Run executes the full Stage 1→2→3 pipeline on raw text from Stage 0 without LLM.
// Returns the resolved result and the verification result for diagnostic use.
func Run(rawText string, packs ...string) (PipelineResult, VerificationResult) {
	return RunWithEngine(context.Background(), nil, rawText, false, packs...)
}

func filterMedicalResults(pr PipelineResult) PipelineResult {
	var cleanEntities []ResolvedEntity
	for _, e := range pr.Entities {
		if !IsMedicalField(e.Field) {
			cleanEntities = append(cleanEntities, e)
		}
	}
	var cleanAmbiguous []string
	for _, k := range pr.AmbiguousKeys {
		if !IsMedicalField(k) {
			cleanAmbiguous = append(cleanAmbiguous, k)
		}
	}
	pr.Entities = cleanEntities
	pr.AmbiguousKeys = cleanAmbiguous
	return pr
}

// RunWithEngine executes the full Stage 1→2→3 pipeline, optionally using an LLM.
func RunWithEngine(ctx context.Context, engine AIEngine, rawText string, useLLM bool, packs ...string) (PipelineResult, VerificationResult) {
	if !useLLM || engine == nil {
		extracted := ExtractCandidates(rawText, packs...)
		verified := Verify(extracted)
		resolved := Resolve(verified)
		if !HasMedicalPack(packs) {
			resolved = filterMedicalResults(resolved)
		}
		return resolved, verified
	}

	// Structural Segmentation Stage
	segments, err := SegmentDocument(ctx, engine, rawText, packs...)
	if err != nil {
		log.Printf("docpipeline: segmenting failed: %v — falling back to full text", err)
		segments = []DocumentSegment{{Title: "Full Document", Text: rawText}}
	}

	var allCandidates []Candidate
	var warnings []string

	for _, seg := range segments {
		// Find offset of segment in rawText to adjust spans correctly
		offset := strings.Index(rawText, seg.Text)
		if offset < 0 {
			offset = 0
		}

		// 1. Heuristic regex extraction on this segment
		regexRes := ExtractCandidates(seg.Text, packs...)
		for _, c := range regexRes.Candidates {
			c.Span[0] += offset
			c.Span[1] += offset
			allCandidates = append(allCandidates, c)
		}

		// 2. Cognitive LLM extraction on this segment
		llmCandidates, err := ExtractLLMCandidates(ctx, engine, seg.Text, packs...)
		if err != nil {
			log.Printf("docpipeline: LLM extraction failed for segment %q: %v", seg.Title, err)
			continue
		}
		for _, c := range llmCandidates {
			c.Span[0] += offset
			c.Span[1] += offset
			allCandidates = append(allCandidates, c)
		}
	}

	extracted := ExtractionResult{
		Candidates: allCandidates,
		RawText:    rawText,
		Warnings:   warnings,
	}
	if len(allCandidates) == 0 {
		extracted.Warnings = append(extracted.Warnings, "no structured fields detected in text")
	}

	verified := Verify(extracted)
	resolved := Resolve(verified)
	if !HasMedicalPack(packs) {
		resolved = filterMedicalResults(resolved)
	}
	return resolved, verified
}

// SegmentDocument partitions a large document into individual patient entries or logical units.
func SegmentDocument(ctx context.Context, engine AIEngine, text string, packs ...string) ([]DocumentSegment, error) {
	if len(text) < 1500 {
		return []DocumentSegment{{Title: "Full Document", Text: text}}, nil
	}

	isMedical := HasMedicalPack(packs)
	var prompt string
	if isMedical {
		prompt = `You are an expert document structural segmenter.
Given the following raw text from a medical document, identify if there are distinct sections, pages, or patient records that should be processed separately (e.g. separate patient referrals, multi-page faxes, or distinct medical record entries).
Segment the text into logical parts and return them as a JSON array of sections.

Rules:
1. Each segment must capture the exact relevant block of text from the source document.
2. Output ONLY a valid JSON array matching the schema below. No explanation, no Markdown formatting, no code blocks, no other text.

JSON Schema:
[
  {
    "title": "Patient A Referral Info / Section name",
    "text": "Exact text block from source document..."
  }
]

Raw Text:
` + text
	} else {
		prompt = `You are an expert document structural segmenter.
Given the following raw text from a document, identify if there are distinct sections, pages, or records that should be processed separately (e.g. separate invoices, statements, forms, multi-page documents, or distinct entries).
Segment the text into logical parts and return them as a JSON array of sections.

Rules:
1. Each segment must capture the exact relevant block of text from the source document.
2. Output ONLY a valid JSON array matching the schema below. No explanation, no Markdown formatting, no code blocks, no other text.

JSON Schema:
[
  {
    "title": "Section name / Document part",
    "text": "Exact text block from source document..."
  }
]

Raw Text:
` + text
	}

	res, err := engine.Predict(ctx, prompt)
	if err != nil {
		return nil, err
	}

	res = cleanJSONString(res)

	// Accept array format
	if !strings.HasPrefix(res, "[") {
		start := strings.Index(res, "[")
		end := strings.LastIndex(res, "]")
		if start >= 0 && end > start {
			res = res[start : end+1]
		}
	}

	var segments []DocumentSegment
	if err := json.Unmarshal([]byte(res), &segments); err != nil {
		return nil, err
	}

	if len(segments) == 0 {
		return []DocumentSegment{{Title: "Full Document", Text: text}}, nil
	}

	return segments, nil
}

// ExtractLLMCandidates asks the LLM to locate candidates using a single comprehensive prompt.
func ExtractLLMCandidates(ctx context.Context, engine AIEngine, text string, packs ...string) ([]Candidate, error) {
	isMedical := HasMedicalPack(packs)
	var allFields string
	if isMedical {
		allFields = `- vendor (Vendor or merchant name)
- invoice_number (Invoice or receipt number)
- total (Total amount due)
- date (Document or invoice date)
- name (Customer, client, or person name)
- address (Street or billing address)
- phone (Phone number)
- email (Email address)
- zip (Zip code)
- ssn (Social Security Number)
- dob (Date of birth)
- patient_name (Patient's full name)
- mrn (Medical Record Number)
- icd10 (ICD-10 clinical codes)
- npi (National Provider Identifier)
- provider_name (Physician/Provider name)
- dos (Date of service)`
	} else {
		allFields = `- vendor (Vendor or merchant name)
- invoice_number (Invoice or receipt number)
- total (Total amount due)
- date (Document or invoice date)
- name (Customer, client, or person name)
- address (Street or billing address)
- phone (Phone number)
- email (Email address)
- zip (Zip code)
- ssn (Social Security Number)
- dob (Date of birth)`
	}

	cands, err := extractLLMGroup(ctx, engine, text, allFields, isMedical)
	if err != nil {
		log.Printf("docpipeline: LLM candidate extraction warning: %v", err)
		return nil, err
	}

	return cands, nil
}

func extractLLMGroup(ctx context.Context, engine AIEngine, text, fieldsDesc string, isMedical bool) ([]Candidate, error) {
	var prompt string
	if isMedical {
		prompt = `You are an expert clinical data extraction assistant.
Given the following raw text from a medical document, extract all candidate values for these specific target fields:
` + fieldsDesc + `

Rules:
1. Return candidates for any of these fields found in the text. If there are multiple candidate values for a field, return ALL of them as separate candidates.
2. For each candidate, estimate your confidence (0.0 to 1.0) and identify the exact text snippet in the source document that contains this value.
3. Output ONLY a valid JSON object matching the schema below. No explanation, no Markdown formatting, no code blocks, no other text.

JSON Schema:
{
  "candidates": [
    {
      "field": "patient_name",
      "value": "John Smith",
      "confidence": 0.85,
      "source": "Patient: John Smith"
    }
  ]
}

Raw Text:
` + text
	} else {
		prompt = `You are an expert document data extraction assistant.
Given the following raw text from a document, extract all candidate values for these specific target fields:
` + fieldsDesc + `

Rules:
1. Return candidates for any of these fields found in the text. If there are multiple candidate values for a field, return ALL of them as separate candidates.
2. For each candidate, estimate your confidence (0.0 to 1.0) and identify the exact text snippet in the source document that contains this value.
3. Output ONLY a valid JSON object matching the schema below. No explanation, no Markdown formatting, no code blocks, no other text.

JSON Schema:
{
  "candidates": [
    {
      "field": "invoice_number",
      "value": "INV-10023",
      "confidence": 0.85,
      "source": "Invoice #: INV-10023"
    }
  ]
}

Raw Text:
` + text
	}

	res, err := engine.Predict(ctx, prompt)
	if err != nil {
		return nil, err
	}

	res = cleanJSONString(res)

	type llmOutput struct {
		Candidates []struct {
			Field      string  `json:"field"`
			Value      string  `json:"value"`
			Confidence float64 `json:"confidence"`
			Source     string  `json:"source"`
		} `json:"candidates"`
	}

	var data llmOutput
	if err := json.Unmarshal([]byte(res), &data); err != nil {
		return nil, err
	}

	var candidates []Candidate
	for _, c := range data.Candidates {
		field := strings.ToLower(strings.TrimSpace(c.Field))
		value := strings.TrimSpace(c.Value)
		if field == "" || value == "" {
			continue
		}

		start := strings.Index(text, value)
		end := start + len(value)
		if start < 0 {
			start = 0
			end = 0
		}

		conf := c.Confidence
		if conf <= 0 {
			conf = 0.50
		} else if conf > 1.0 {
			conf = 1.0
		}

		candidates = append(candidates, Candidate{
			Field:      field,
			Value:      value,
			Confidence: conf,
			Span:       [2]int{start, end},
			Source:     "llm_" + c.Source,
		})
	}

	return candidates, nil
}

// cleanJSONString handles conversational markdown code block syntax from local models.
func cleanJSONString(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		var cleanLines []string
		for _, l := range lines {
			if !strings.HasPrefix(l, "```") {
				cleanLines = append(cleanLines, l)
			}
		}
		s = strings.Join(cleanLines, "\n")
	}
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return s
}
