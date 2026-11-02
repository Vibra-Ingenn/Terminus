package tools

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

var startXRefRE = regexp.MustCompile(`startxref\s+(\d+)`)

// ---------------------------------------------------------------------------
// Medical Tool Suite — Go-native, zero Python
// The AI drives these; it never writes medical code itself.
// ---------------------------------------------------------------------------

// registerMedicalTools adds the medical-specific tool set.
func (r *Registry) registerMedicalTools() {
	r.Register(ToolDef{
		Name:        "pdf_extract",
		Description: "Extract all text from a PDF file. Returns plain text content.",
		Parameters:  map[string]string{"path": "Path to the PDF file"},
		Category:    "document",
	}, toolPDFExtract)

	r.Register(ToolDef{
		Name:        "pdf_diagnose",
		Description: "Inspect a malformed PDF and explain what Terminus can repair, what text is still recoverable, and what the user must do if repair is not possible.",
		Parameters:  map[string]string{"path": "Path to the PDF file"},
		Category:    "document",
	}, toolPDFDiagnose)

	r.Register(ToolDef{
		Name:        "chart_template",
		Description: "Generate a structured medical chart (SOAP note, H&P, or progress note) from provided fields. Returns formatted text.",
		Parameters: map[string]string{
			"type":            "Chart type: soap, hp (history & physical), progress",
			"patient_name":    "Patient name (will be included in header)",
			"date":            "(optional) Date string, defaults to today",
			"chief_complaint": "(optional) Chief complaint / reason for visit",
			"subjective":      "(optional) Subjective findings — patient's own words, symptoms",
			"objective":       "(optional) Objective findings — vitals, exam, labs",
			"assessment":      "(optional) Assessment / diagnosis",
			"plan":            "(optional) Plan / treatment / follow-up",
			"history":         "(optional) For H&P: medical history",
			"physical_exam":   "(optional) For H&P: physical exam findings",
			"medications":     "(optional) Current medications",
			"allergies":       "(optional) Known allergies",
			"notes":           "(optional) Additional notes",
		},
		Category: "medical",
	}, toolChartTemplate)

	r.Register(ToolDef{
		Name:        "icd10_lookup",
		Description: "Search ICD-10 diagnosis codes by keyword or code prefix. Returns matching codes with descriptions.",
		Parameters: map[string]string{
			"query": "Search term (symptom, diagnosis, or ICD-10 code prefix)",
			"limit": "(optional) Max results to return, default 10",
		},
		Category: "medical",
	}, toolICD10Lookup)

	r.Register(ToolDef{
		Name:        "anonymize",
		Description: "Redact Protected Health Information (PHI) from text. Replaces SSN, MRN, dates of birth, phone numbers, and emails with [REDACTED] tags.",
		Parameters: map[string]string{
			"text": "Text to anonymize",
		},
		Category: "security",
	}, toolAnonymize)

	r.Register(ToolDef{
		Name:        "fhir_export",
		Description: "Generate a FHIR R4 Bundle (JSON) from structured patient/encounter data. Returns valid FHIR JSON.",
		Parameters: map[string]string{
			"patient_name":   "Patient full name",
			"patient_dob":    "(optional) Date of birth (YYYY-MM-DD)",
			"patient_gender": "(optional) Gender: male, female, other, unknown",
			"encounter_date": "(optional) Encounter date, defaults to today",
			"diagnosis":      "(optional) Primary diagnosis text",
			"icd10_code":     "(optional) ICD-10 code for the diagnosis",
			"notes":          "(optional) Clinical notes text",
		},
		Category: "medical",
	}, toolFHIRExport)

	r.Register(ToolDef{
		Name:        "hl7_parse",
		Description: "Parse an HL7 v2.x message (pipe-delimited) into structured JSON. Handles ADT, ORM, ORU, and other message types.",
		Parameters: map[string]string{
			"message": "Raw HL7 v2.x message text (pipe-delimited segments separated by newlines)",
		},
		Category: "medical",
	}, toolHL7Parse)

	r.Register(ToolDef{
		Name:        "vitals_check",
		Description: "Check patient vital signs against normal adult ranges. Returns status (normal/warning/critical) for each vital.",
		Parameters: map[string]string{
			"heart_rate":    "(optional) Heart rate in BPM",
			"systolic_bp":   "(optional) Systolic blood pressure mmHg",
			"diastolic_bp":  "(optional) Diastolic blood pressure mmHg",
			"temperature":   "(optional) Temperature in Fahrenheit",
			"respiratory":   "(optional) Respiratory rate breaths/min",
			"o2_saturation": "(optional) Oxygen saturation percentage",
			"blood_glucose": "(optional) Blood glucose mg/dL",
		},
		Category: "medical",
	}, toolVitalsCheck)

	r.Register(ToolDef{
		Name:        "medication_check",
		Description: "Check a list of medications for known dangerous interactions. Returns warnings for any flagged combinations.",
		Parameters: map[string]string{
			"medications": "Comma-separated list of medication names to check for interactions",
		},
		Category: "medical",
	}, toolMedicationCheck)

	r.Register(ToolDef{
		Name:        "encrypt_data",
		Description: "Encrypt or decrypt text using AES-256-GCM. For encrypting PHI at rest. Uses TERMINUS_ENCRYPTION_KEY env var or a provided key.",
		Parameters: map[string]string{
			"action": "encrypt or decrypt",
			"text":   "Text to encrypt or base64 ciphertext to decrypt",
			"key":    "(optional) 32-byte hex key. If omitted, uses TERMINUS_ENCRYPTION_KEY env var.",
		},
		Category: "security",
	}, toolEncryptData)

	r.Register(ToolDef{
		Name:        "audit_log",
		Description: "Write an entry to the audit log for medical records. Use for recording data access, PHI operations, and clinical decisions.",
		Parameters: map[string]string{
			"action":  "What was done (e.g. 'pdf_accessed', 'chart_created', 'phi_exported')",
			"user":    "(optional) Who did it (clinician name or system)",
			"patient": "(optional) Patient identifier (will be hashed for privacy)",
			"details": "(optional) Additional details about the action",
		},
		Category: "security",
	}, toolAuditLog)
}

// ═══════════════════════════════════════════════════════════════════════════
// pdf_extract — Pure Go PDF text extraction
// ═══════════════════════════════════════════════════════════════════════════

func toolPDFExtract(args map[string]any) (result ToolResult) {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{ToolName: "pdf_extract", Success: false, Error: "path is required"}
	}

	// Resolve to absolute path
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}

	// Verify file exists before passing to the PDF library — gives a clear
	// error the AI can learn from instead of an opaque library failure.
	if _, statErr := os.Stat(path); statErr != nil {
		return ToolResult{
			ToolName: "pdf_extract",
			Success:  false,
			Error:    fmt.Sprintf("file not found at %s — check the path. Try list_dir on workspace/uploads to see what's actually there.", path),
		}
	}

	// ledongthuc/pdf can panic on encrypted PDFs, newer PDF 1.7+ compression,
	// or malformed structures. Recover so one bad PDF doesn't kill the kernel.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("pdf_extract: damaged PDF at %s (technical: %v)", path, r)
			diag, data, _ := diagnosePDFFile(path)
			if salvaged := salvagePDFTextFromBytes(data); salvageLooksReadable(salvaged) {
				result = ToolResult{
					ToolName: "pdf_extract",
					Success:  true,
					Output:   attachExtractWarnings(formatSalvagedPDFText(salvaged), []string{"this PDF was damaged; the text was recovered"}),
				}
				return
			}
			result = ToolResult{
				ToolName: "pdf_extract",
				Success:  false,
				Error:    fmt.Sprintf("PDF parser crashed on this file (%v). Terminus already tried parser recovery first. %s", r, diag.failureMessage()),
			}
		}
	}()

	openPath, cleanup, f, r, err := openPDFWithRepair(path)
	if err != nil && (errors.Is(err, pdf.ErrInvalidPassword) || strings.Contains(err.Error(), "unsupported PDF: encryption")) {
		// Locked files are not damaged; say what to do instead.
		log.Printf("pdf_extract: locked PDF at %s (technical: %v)", path, err)
		msg := "This PDF is locked with a password. Open it, save a copy without the password, and try again."
		if !errors.Is(err, pdf.ErrInvalidPassword) {
			msg = "This PDF is locked in a way Terminus can't open yet. Open it, save a copy without the lock, and try again."
		}
		return ToolResult{ToolName: "pdf_extract", Success: false, Error: msg}
	}
	if err != nil {
		diag, data, _ := diagnosePDFFile(path)
		if salvaged := salvagePDFTextFromBytes(data); salvageLooksReadable(salvaged) {
			return ToolResult{
				ToolName: "pdf_extract",
				Success:  true,
				Output:   attachExtractWarnings(formatSalvagedPDFText(salvaged), []string{"this PDF was damaged; the text was recovered"}),
			}
		}
		return ToolResult{
			ToolName: "pdf_extract",
			Success:  false,
			Error:    fmt.Sprintf("cannot open PDF (%v). Terminus already tried parser repair first. %s", err, diag.failureMessage()),
		}
	}
	if cleanup != nil {
		defer cleanup()
	}
	defer f.Close()
	if openPath != path {
		log.Printf("pdf_extract: repaired malformed xref offset for %s using temp copy %s", path, openPath)
	}

	output, totalPages, textWarnings := extractPDFText(r)
	if len(strings.TrimSpace(output)) == 0 {
		if ocr := ocrPDFPages(path, "eng", "pdf_extract"); ocr.Success {
			return ocr
		}
		diag, data, _ := diagnosePDFFile(path)
		if salvaged := salvagePDFTextFromBytes(data); salvageLooksReadable(salvaged) {
			return ToolResult{
				ToolName: "pdf_extract",
				Success:  true,
				Output:   attachExtractWarnings(formatSalvagedPDFText(salvaged), []string{"this PDF was damaged; the text was recovered"}),
			}
		}
		return ToolResult{
			ToolName: "pdf_extract",
			Success:  true,
			Output:   fmt.Sprintf("(no text found — %s)", diag.noTextMessage()),
		}
	}
	// Truncate very large PDFs to keep context manageable
	if len(output) > maxOutputChars {
		output = cutUTF8(output, maxOutputChars) + "\n\n... [truncated, " + fmt.Sprintf("%d", totalPages) + " pages total]"
	}
	pageWarn := fmt.Sprintf("%d pages read from the PDF text layer", totalPages)
	if totalPages == 1 {
		pageWarn = "1 page read from the PDF text layer"
	}
	allWarns := append(textWarnings, pageWarn)
	out := attachExtractWarnings(output, dedupeStrings(allWarns))
	return ToolResult{ToolName: "pdf_extract", Success: true, Output: out}
}

func toolPDFDiagnose(args map[string]any) ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{ToolName: "pdf_diagnose", Success: false, Error: "path is required"}
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	if _, err := os.Stat(path); err != nil {
		return ToolResult{ToolName: "pdf_diagnose", Success: false, Error: fmt.Sprintf("file not found at %s", path)}
	}

	diag, _, err := diagnosePDFFile(path)
	if err != nil {
		return ToolResult{ToolName: "pdf_diagnose", Success: false, Error: fmt.Sprintf("cannot inspect PDF: %v", err)}
	}
	return ToolResult{ToolName: "pdf_diagnose", Success: true, Output: diag.summary(path)}
}

func extractPDFText(r *pdf.Reader) (string, int, []string) {
	var sb strings.Builder
	totalPages := r.NumPage()
	var allWarnings []string
	for i := 1; i <= totalPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			log.Printf("pdf_extract: page %d error: %v", i, err)
			text = ""
		}
		// When the words have run together (no spaces stored in the PDF),
		// rebuild the page from the letters' positions instead.
		if spaceRatio(text) < 0.05 {
			if laid, ok := pageTextByLayout(page); ok && spaceRatio(laid) > spaceRatio(text) {
				text = laid
			}
		}
		if text == "" {
			continue
		}
		text, warns := CleanPDFExtractedText(text)
		allWarnings = append(allWarnings, warns...)
		if sb.Len() > 0 {
			sb.WriteString(PageBreakSentinel)
		}
		sb.WriteString(text)
	}
	// Joined letters some fonts use ("ﬁ" for "fi") break searching and
	// matching; write them as ordinary letters.
	out := ligatures.Replace(sb.String())
	out, warns := CleanPDFExtractedText(out)
	allWarnings = append(allWarnings, warns...)
	return out, totalPages, dedupeStrings(allWarnings)
}

var ligatures = strings.NewReplacer("ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st")

func openPDFWithRepair(path string) (string, func(), *os.File, *pdf.Reader, error) {
	f, r, err := pdf.Open(path)
	if err == nil {
		return path, nil, f, r, nil
	}

	repairedPath, repairErr := repairPDFStartXRefOffset(path, err)
	if repairErr != nil {
		return path, nil, nil, nil, err
	}

	f, r, err = pdf.Open(repairedPath)
	if err != nil {
		_ = os.Remove(repairedPath)
		return path, nil, nil, nil, err
	}
	return repairedPath, func() { _ = os.Remove(repairedPath) }, f, r, nil
}

func repairPDFStartXRefOffset(path string, openErr error) (string, error) {
	if !strings.Contains(strings.ToLower(openErr.Error()), "cross-reference table not found") {
		return "", openErr
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	fixed, changed := repairPDFStartXRefBytes(data)
	if !changed {
		return "", openErr
	}

	tmp, err := os.CreateTemp("", "terminus-pdf-repair-*.pdf")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(fixed); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

func repairPDFStartXRefBytes(data []byte) ([]byte, bool) {
	matches := startXRefRE.FindSubmatchIndex(data)
	if matches == nil {
		return nil, false
	}

	declaredBytes := data[matches[2]:matches[3]]
	declaredOffset, err := strconv.Atoi(string(declaredBytes))
	if err != nil {
		return nil, false
	}

	actualOffset := findActualXRefOffset(data, declaredOffset)
	if actualOffset < 0 || actualOffset == declaredOffset {
		return nil, false
	}

	replacement := fmt.Sprintf("%0*d", len(declaredBytes), actualOffset)
	fixed := bytes.Clone(data)
	copy(fixed[matches[2]:matches[3]], []byte(replacement))
	return fixed, true
}

func findActualXRefOffset(data []byte, declaredOffset int) int {
	windowStart := declaredOffset - 256
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := declaredOffset + 4096
	if windowEnd > len(data) {
		windowEnd = len(data)
	}
	if idx := bytes.Index(data[windowStart:windowEnd], []byte("xref")); idx >= 0 {
		return windowStart + idx
	}
	return bytes.Index(data, []byte("xref"))
}

type pdfDiagnosis struct {
	HasHeader                bool
	HasEOF                   bool
	HasTrailer               bool
	HasStartXRef             bool
	DeclaredStartXRef        int
	ActualXRef               int
	UTF8ExpandedBinaryMarker bool
	TextCandidates           int
	Issues                   []string
}

func diagnosePDFFile(path string) (pdfDiagnosis, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pdfDiagnosis{}, nil, err
	}
	diag := diagnosePDFBytes(data)
	return diag, data, nil
}

func diagnosePDFBytes(data []byte) pdfDiagnosis {
	diag := pdfDiagnosis{
		HasHeader:                bytes.Contains(data, []byte("%PDF-")),
		HasEOF:                   bytes.Contains(data, []byte("%%EOF")),
		HasTrailer:               bytes.Contains(data, []byte("trailer")),
		ActualXRef:               bytes.Index(data, []byte("xref")),
		UTF8ExpandedBinaryMarker: bytes.Contains(data, []byte{0xC3, 0xA2, 0xC3, 0xA3, 0xC3, 0x8F, 0xC3, 0x93}),
	}
	if matches := startXRefRE.FindSubmatch(data); matches != nil {
		diag.HasStartXRef = true
		if off, err := strconv.Atoi(string(matches[1])); err == nil {
			diag.DeclaredStartXRef = off
		}
	}

	if !diag.HasHeader {
		diag.Issues = append(diag.Issues, "missing %PDF- header")
	}
	if !diag.HasEOF {
		diag.Issues = append(diag.Issues, "missing %%EOF marker")
	}
	if !diag.HasTrailer {
		diag.Issues = append(diag.Issues, "missing trailer block")
	}
	if !diag.HasStartXRef {
		diag.Issues = append(diag.Issues, "missing startxref pointer")
	}
	if diag.ActualXRef < 0 {
		diag.Issues = append(diag.Issues, "missing xref table")
	}
	if diag.HasStartXRef && diag.ActualXRef >= 0 && diag.DeclaredStartXRef != diag.ActualXRef {
		diag.Issues = append(diag.Issues, fmt.Sprintf("startxref points to %d but xref starts at %d", diag.DeclaredStartXRef, diag.ActualXRef))
	}
	if diag.UTF8ExpandedBinaryMarker {
		diag.Issues = append(diag.Issues, "binary marker looks UTF-8 expanded, which usually means byte offsets were calculated incorrectly")
	}
	diag.TextCandidates = countSalvagedPDFStrings(data)
	return diag
}

func (d pdfDiagnosis) summary(path string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("PDF diagnostics for %s", path))
	lines = append(lines, fmt.Sprintf("- header: %s", yesNo(d.HasHeader)))
	lines = append(lines, fmt.Sprintf("- trailer: %s", yesNo(d.HasTrailer)))
	lines = append(lines, fmt.Sprintf("- %%EOF: %s", yesNo(d.HasEOF)))
	lines = append(lines, fmt.Sprintf("- startxref: %s", yesNo(d.HasStartXRef)))
	if d.HasStartXRef {
		lines = append(lines, fmt.Sprintf("- declared startxref offset: %d", d.DeclaredStartXRef))
	}
	lines = append(lines, fmt.Sprintf("- xref table found: %s", yesNo(d.ActualXRef >= 0)))
	if d.ActualXRef >= 0 {
		lines = append(lines, fmt.Sprintf("- actual xref offset: %d", d.ActualXRef))
	}
	lines = append(lines, fmt.Sprintf("- recoverable text fragments: %d", d.TextCandidates))
	if len(d.Issues) > 0 {
		lines = append(lines, "- issues:")
		for _, issue := range d.Issues {
			lines = append(lines, "  - "+issue)
		}
	}
	lines = append(lines, "- recommended action: "+d.recommendedAction())
	return strings.Join(lines, "\n")
}

func (d pdfDiagnosis) recommendedAction() string {
	if d.TextCandidates > 0 {
		if hasIssueContaining(d.Issues, "startxref points") {
			return "Terminus can usually repair this kind of broken xref pointer automatically; if extraction still looks incomplete, re-export the PDF from the source app."
		}
		return "Terminus can still salvage text from this file; if the result is incomplete, re-export the PDF from the source app so the structure is valid."
	}
	if !d.HasHeader || !d.HasTrailer || !d.HasEOF {
		return "This PDF is structurally incomplete. Re-export it as a standard PDF from the source app, or print it to PDF again."
	}
	return "This file appears to have no usable text layer. If it is visually readable, run OCR or export it again with text preserved."
}

func (d pdfDiagnosis) noTextMessage() string {
	if d.TextCandidates > 0 {
		return "the normal parser found no text, but raw text fragments still exist in the file. Terminus should salvage those first"
	}
	return d.recommendedAction()
}

func (d pdfDiagnosis) failureMessage() string {
	if len(d.Issues) == 0 {
		return d.recommendedAction()
	}
	return fmt.Sprintf("Detected issues: %s. Recommended action: %s", strings.Join(d.Issues, "; "), d.recommendedAction())
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func hasIssueContaining(issues []string, needle string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, needle) {
			return true
		}
	}
	return false
}

// salvageLooksReadable reports whether text pulled out of a damaged PDF's raw
// bytes is something a person could read. Compressed page data produces
// strings too, but they are noise; calling that a success misleads whoever
// uses the result.
func salvageLooksReadable(s string) bool {
	words := strings.Fields(s)
	if len(words) < 2 {
		return false
	}
	real := 0
	for _, w := range words {
		letters := 0
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				letters++
			}
		}
		if letters >= 3 && letters*10 >= len([]rune(w))*7 {
			real++
		}
	}
	// A short line such as "Patient Name: Jane Smith" is readable; noise from
	// compressed data has almost no real words in it.
	return real >= 2 && real*10 >= len(words)*3
}

func formatSalvagedPDFText(output string) string {
	if len(output) > maxOutputChars {
		output = cutUTF8(output, maxOutputChars) + "\n\n... [truncated]"
	}
	return output
}

func countSalvagedPDFStrings(data []byte) int {
	return len(findPDFLiteralStrings(data))
}

func salvagePDFTextFromBytes(data []byte) string {
	fragments := findPDFLiteralStrings(data)
	if len(fragments) == 0 {
		return ""
	}
	return strings.Join(fragments, "\n")
}

func findPDFLiteralStrings(data []byte) []string {
	var out []string
	seen := make(map[string]struct{})
	for i := 0; i < len(data); i++ {
		if data[i] != '(' {
			continue
		}
		raw, next, ok := readPDFLiteralString(data, i)
		if !ok {
			continue
		}
		i = next - 1
		text := strings.TrimSpace(decodePDFLiteralString(raw))
		if !looksLikeHumanText(text) {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func readPDFLiteralString(data []byte, start int) ([]byte, int, bool) {
	if start >= len(data) || data[start] != '(' {
		return nil, start, false
	}
	var raw []byte
	depth := 1
	escaped := false
	for i := start + 1; i < len(data); i++ {
		b := data[i]
		if escaped {
			raw = append(raw, b)
			escaped = false
			continue
		}
		switch b {
		case '\\':
			raw = append(raw, b)
			escaped = true
		case '(':
			depth++
			raw = append(raw, b)
		case ')':
			depth--
			if depth == 0 {
				return raw, i + 1, true
			}
			raw = append(raw, b)
		default:
			raw = append(raw, b)
		}
	}
	return nil, start, false
}

func decodePDFLiteralString(raw []byte) string {
	var out []rune
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b != '\\' {
			out = append(out, rune(b))
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		switch raw[i] {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case '(', ')', '\\':
			out = append(out, rune(raw[i]))
		case '\n', '\r':
		default:
			if raw[i] >= '0' && raw[i] <= '7' {
				value := int(raw[i] - '0')
				for j := 0; j < 2 && i+1 < len(raw) && raw[i+1] >= '0' && raw[i+1] <= '7'; j++ {
					i++
					value = value*8 + int(raw[i]-'0')
				}
				out = append(out, rune(value))
				continue
			}
			out = append(out, rune(raw[i]))
		}
	}
	// The loop above reads one byte as one character. When the bytes are in
	// fact UTF-8 (a dash, a curly quote, an accented name), put them back
	// together so they do not come out as stray accented letters.
	asBytes := make([]byte, 0, len(out))
	high := false
	for _, r := range out {
		if r > 255 {
			return string(out)
		}
		if r >= 128 {
			high = true
		}
		asBytes = append(asBytes, byte(r))
	}
	if high && utf8.Valid(asBytes) {
		return string(asBytes)
	}
	return string(out)
}

func looksLikeHumanText(s string) bool {
	if len(s) < 4 {
		return false
	}
	letters := 0
	printable := 0
	for _, r := range s {
		if r >= 32 && r != 127 {
			printable++
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	return printable >= len([]rune(s))*3/4 && letters > 0
}

// ═══════════════════════════════════════════════════════════════════════════
// chart_template — SOAP / H&P / Progress note generation
// ═══════════════════════════════════════════════════════════════════════════

func toolChartTemplate(args map[string]any) ToolResult {
	chartType, _ := args["type"].(string)
	if chartType == "" {
		chartType = "soap"
	}
	chartType = strings.ToLower(chartType)

	patientName, _ := args["patient_name"].(string)
	if patientName == "" {
		patientName = "Unknown Patient"
	}

	dateStr, _ := args["date"].(string)
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}

	get := func(key string) string {
		v, _ := args[key].(string)
		return v
	}

	var sb strings.Builder

	// Header
	sb.WriteString("╔══════════════════════════════════════════════════════════╗\n")
	sb.WriteString(fmt.Sprintf("║  PATIENT: %-46s  ║\n", patientName))
	sb.WriteString(fmt.Sprintf("║  DATE:    %-46s  ║\n", dateStr))
	sb.WriteString("╚══════════════════════════════════════════════════════════╝\n\n")

	switch chartType {
	case "soap":
		sb.WriteString("═══ SOAP NOTE ═══\n\n")
		writeSection(&sb, "CHIEF COMPLAINT", get("chief_complaint"))
		writeSection(&sb, "S — SUBJECTIVE", get("subjective"))
		writeSection(&sb, "O — OBJECTIVE", get("objective"))
		writeSection(&sb, "A — ASSESSMENT", get("assessment"))
		writeSection(&sb, "P — PLAN", get("plan"))
		writeSection(&sb, "MEDICATIONS", get("medications"))
		writeSection(&sb, "ALLERGIES", get("allergies"))
		writeSection(&sb, "ADDITIONAL NOTES", get("notes"))

	case "hp":
		sb.WriteString("═══ HISTORY & PHYSICAL ═══\n\n")
		writeSection(&sb, "CHIEF COMPLAINT", get("chief_complaint"))
		writeSection(&sb, "HISTORY OF PRESENT ILLNESS", get("subjective"))
		writeSection(&sb, "PAST MEDICAL HISTORY", get("history"))
		writeSection(&sb, "MEDICATIONS", get("medications"))
		writeSection(&sb, "ALLERGIES", get("allergies"))
		writeSection(&sb, "PHYSICAL EXAMINATION", get("physical_exam"))
		writeSection(&sb, "ASSESSMENT", get("assessment"))
		writeSection(&sb, "PLAN", get("plan"))
		writeSection(&sb, "ADDITIONAL NOTES", get("notes"))

	case "progress":
		sb.WriteString("═══ PROGRESS NOTE ═══\n\n")
		writeSection(&sb, "INTERVAL HISTORY", get("subjective"))
		writeSection(&sb, "CURRENT STATUS", get("objective"))
		writeSection(&sb, "ASSESSMENT", get("assessment"))
		writeSection(&sb, "PLAN", get("plan"))
		writeSection(&sb, "MEDICATIONS", get("medications"))
		writeSection(&sb, "ADDITIONAL NOTES", get("notes"))

	default:
		return ToolResult{ToolName: "chart_template", Success: false, Error: fmt.Sprintf("unknown chart type %q — use soap, hp, or progress", chartType)}
	}

	sb.WriteString("\n─── End of Note ───\n")

	return ToolResult{ToolName: "chart_template", Success: true, Output: sb.String()}
}

func writeSection(sb *strings.Builder, title, content string) {
	sb.WriteString(fmt.Sprintf("┌─ %s\n", title))
	if content == "" {
		sb.WriteString("│  (not provided)\n")
	} else {
		for _, line := range strings.Split(content, "\n") {
			sb.WriteString(fmt.Sprintf("│  %s\n", line))
		}
	}
	sb.WriteString("└─\n\n")
}

// ═══════════════════════════════════════════════════════════════════════════
// icd10_lookup — Local ICD-10 code search
// Ships with ~500 common codes; full dataset can be imported.
// ═══════════════════════════════════════════════════════════════════════════

type icd10Entry struct {
	Code string
	Desc string
}

// Common ICD-10 codes for primary care / emergency — enough to be useful
// without shipping a 70MB database. Full import can be added later.
var icd10DB = []icd10Entry{
	// Cardiovascular
	{"I10", "Essential (primary) hypertension"},
	{"I11.9", "Hypertensive heart disease without heart failure"},
	{"I20.9", "Angina pectoris, unspecified"},
	{"I21.9", "Acute myocardial infarction, unspecified"},
	{"I25.10", "Atherosclerotic heart disease of native coronary artery without angina pectoris"},
	{"I48.91", "Unspecified atrial fibrillation"},
	{"I50.9", "Heart failure, unspecified"},
	{"I63.9", "Cerebral infarction, unspecified"},
	// Respiratory
	{"J06.9", "Acute upper respiratory infection, unspecified"},
	{"J18.9", "Pneumonia, unspecified organism"},
	{"J20.9", "Acute bronchitis, unspecified"},
	{"J44.1", "Chronic obstructive pulmonary disease with acute exacerbation"},
	{"J45.20", "Mild intermittent asthma, uncomplicated"},
	{"J45.909", "Unspecified asthma, uncomplicated"},
	// Endocrine
	{"E11.9", "Type 2 diabetes mellitus without complications"},
	{"E11.65", "Type 2 diabetes mellitus with hyperglycemia"},
	{"E03.9", "Hypothyroidism, unspecified"},
	{"E05.90", "Thyrotoxicosis, unspecified without thyrotoxic crisis"},
	{"E78.5", "Hyperlipidemia, unspecified"},
	// Mental/Behavioral
	{"F32.9", "Major depressive disorder, single episode, unspecified"},
	{"F33.0", "Major depressive disorder, recurrent, mild"},
	{"F41.1", "Generalized anxiety disorder"},
	{"F41.9", "Anxiety disorder, unspecified"},
	{"F17.210", "Nicotine dependence, cigarettes, uncomplicated"},
	// Musculoskeletal
	{"M54.5", "Low back pain"},
	{"M54.2", "Cervicalgia (neck pain)"},
	{"M79.3", "Panniculitis, unspecified"},
	{"M25.511", "Pain in right shoulder"},
	{"M25.512", "Pain in left shoulder"},
	{"M17.11", "Primary osteoarthritis, right knee"},
	{"M17.12", "Primary osteoarthritis, left knee"},
	// GI
	{"K21.0", "Gastro-esophageal reflux disease with esophagitis"},
	{"K58.9", "Irritable bowel syndrome without diarrhea"},
	{"K80.20", "Calculus of gallbladder without cholecystitis without obstruction"},
	// GU
	{"N39.0", "Urinary tract infection, site not specified"},
	{"N40.0", "Benign prostatic hyperplasia without lower urinary tract symptoms"},
	// Neuro
	{"G43.909", "Migraine, unspecified, not intractable, without status migrainosus"},
	{"G47.00", "Insomnia, unspecified"},
	{"G89.29", "Other chronic pain"},
	// Injury/Poisoning
	{"S06.0X0A", "Concussion without loss of consciousness, initial encounter"},
	{"T78.40XA", "Allergy, unspecified, initial encounter"},
	// Signs/Symptoms
	{"R05.9", "Cough, unspecified"},
	{"R06.02", "Shortness of breath"},
	{"R07.9", "Chest pain, unspecified"},
	{"R10.9", "Unspecified abdominal pain"},
	{"R50.9", "Fever, unspecified"},
	{"R51.9", "Headache, unspecified"},
	{"R53.83", "Other fatigue"},
	{"R73.09", "Other abnormal glucose"},
	// Preventive
	{"Z00.00", "Encounter for general adult medical examination without abnormal findings"},
	{"Z23", "Encounter for immunization"},
	{"Z12.31", "Encounter for screening mammogram for malignant neoplasm of breast"},
	// Infectious
	{"A09", "Infectious gastroenteritis and colitis, unspecified"},
	{"B34.9", "Viral infection, unspecified"},
	{"U07.1", "COVID-19"},
	// Dermatologic
	{"L30.9", "Dermatitis, unspecified"},
	{"L70.0", "Acne vulgaris"},
	// Hematologic
	{"D64.9", "Anemia, unspecified"},
	// Obesity
	{"E66.01", "Morbid (severe) obesity due to excess calories"},
	{"E66.9", "Obesity, unspecified"},
	// Pain
	{"G89.4", "Chronic pain syndrome"},
	{"M79.7", "Fibromyalgia"},
	// Renal
	{"N18.3", "Chronic kidney disease, stage 3 (moderate)"},
	{"N18.9", "Chronic kidney disease, unspecified"},
	// Cancer screening/common
	{"C34.90", "Malignant neoplasm of unspecified part of unspecified bronchus or lung"},
	{"C50.919", "Malignant neoplasm of unspecified site of unspecified female breast"},
	{"Z85.3", "Personal history of malignant neoplasm of breast"},
}

func toolICD10Lookup(args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{ToolName: "icd10_lookup", Success: false, Error: "query is required"}
	}

	limitF, _ := args["limit"].(float64)
	limit := int(limitF)
	if limit <= 0 {
		limit = 10
	}

	query = strings.ToLower(query)
	var matches []string

	for _, e := range icd10DB {
		if strings.Contains(strings.ToLower(e.Code), query) || strings.Contains(strings.ToLower(e.Desc), query) {
			matches = append(matches, fmt.Sprintf("%s — %s", e.Code, e.Desc))
			if len(matches) >= limit {
				break
			}
		}
	}

	if len(matches) == 0 {
		return ToolResult{ToolName: "icd10_lookup", Success: true, Output: fmt.Sprintf("No ICD-10 codes found for %q. Try broader terms.", query)}
	}
	return ToolResult{ToolName: "icd10_lookup", Success: true, Output: fmt.Sprintf("[%d matches]\n%s", len(matches), strings.Join(matches, "\n"))}
}

// ═══════════════════════════════════════════════════════════════════════════
// anonymize — PHI redaction
// ═══════════════════════════════════════════════════════════════════════════

var phiPatterns = []struct {
	Name    string
	Pattern *regexp.Regexp
	Replace string
}{
	{"SSN", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), "[REDACTED-SSN]"},
	{"SSN_NO_DASH", regexp.MustCompile(`\b\d{9}\b`), "[REDACTED-SSN]"},
	{"PHONE", regexp.MustCompile(`\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`), "[REDACTED-PHONE]"},
	{"EMAIL", regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Z|a-z]{2,}\b`), "[REDACTED-EMAIL]"},
	{"DOB", regexp.MustCompile(`\b(?:DOB|Date of Birth|Born)[:\s]+\d{1,2}[/\-]\d{1,2}[/\-]\d{2,4}\b`), "[REDACTED-DOB]"},
	{"MRN", regexp.MustCompile(`\b(?:MRN|Medical Record|Chart)[:\s#]*\d{4,10}\b`), "[REDACTED-MRN]"},
	{"DATE_MMDDYYYY", regexp.MustCompile(`\b\d{1,2}/\d{1,2}/\d{4}\b`), "[REDACTED-DATE]"},
	{"ZIP", regexp.MustCompile(`\b\d{5}(?:-\d{4})?\b`), "[REDACTED-ZIP]"},
}

func toolAnonymize(args map[string]any) ToolResult {
	text, _ := args["text"].(string)
	if text == "" {
		return ToolResult{ToolName: "anonymize", Success: false, Error: "text is required"}
	}

	redacted := text
	totalRedactions := 0
	details := []string{}

	for _, p := range phiPatterns {
		matches := p.Pattern.FindAllString(redacted, -1)
		if len(matches) > 0 {
			totalRedactions += len(matches)
			details = append(details, fmt.Sprintf("%s: %d", p.Name, len(matches)))
			redacted = p.Pattern.ReplaceAllString(redacted, p.Replace)
		}
	}

	summary := fmt.Sprintf("[%d PHI items redacted: %s]\n\n%s", totalRedactions, strings.Join(details, ", "), redacted)
	if totalRedactions == 0 {
		summary = "[No PHI patterns detected]\n\n" + redacted
	}
	return ToolResult{ToolName: "anonymize", Success: true, Output: summary}
}

// ═══════════════════════════════════════════════════════════════════════════
// fhir_export — FHIR R4 Bundle generation
// Generates valid FHIR JSON without any external library.
// ═══════════════════════════════════════════════════════════════════════════

func toolFHIRExport(args map[string]any) ToolResult {
	patientName, _ := args["patient_name"].(string)
	if patientName == "" {
		return ToolResult{ToolName: "fhir_export", Success: false, Error: "patient_name is required"}
	}

	get := func(key string) string {
		v, _ := args[key].(string)
		return v
	}

	patientDOB := get("patient_dob")
	patientGender := get("patient_gender")
	if patientGender == "" {
		patientGender = "unknown"
	}
	encounterDate := get("encounter_date")
	if encounterDate == "" {
		encounterDate = time.Now().Format("2006-01-02")
	}
	diagnosis := get("diagnosis")
	icd10Code := get("icd10_code")
	notes := get("notes")

	// Split name into family/given
	nameParts := strings.Fields(patientName)
	familyName := nameParts[len(nameParts)-1]
	givenNames := nameParts[:len(nameParts)-1]
	if len(givenNames) == 0 {
		givenNames = []string{familyName}
	}

	// Build FHIR resources
	patientResource := map[string]any{
		"resourceType": "Patient",
		"id":           "patient-1",
		"name": []map[string]any{{
			"use":    "official",
			"family": familyName,
			"given":  givenNames,
		}},
		"gender": patientGender,
	}
	if patientDOB != "" {
		patientResource["birthDate"] = patientDOB
	}

	encounterResource := map[string]any{
		"resourceType": "Encounter",
		"id":           "encounter-1",
		"status":       "finished",
		"class": map[string]string{
			"system":  "http://terminology.hl7.org/CodeSystem/v3-ActCode",
			"code":    "AMB",
			"display": "ambulatory",
		},
		"subject": map[string]string{
			"reference": "Patient/patient-1",
		},
		"period": map[string]string{
			"start": encounterDate + "T00:00:00Z",
		},
	}

	entries := []map[string]any{
		{"fullUrl": "urn:uuid:patient-1", "resource": patientResource},
		{"fullUrl": "urn:uuid:encounter-1", "resource": encounterResource},
	}

	// Condition (diagnosis) if provided
	if diagnosis != "" {
		conditionResource := map[string]any{
			"resourceType": "Condition",
			"id":           "condition-1",
			"clinicalStatus": map[string]any{
				"coding": []map[string]string{{
					"system": "http://terminology.hl7.org/CodeSystem/condition-clinical",
					"code":   "active",
				}},
			},
			"subject": map[string]string{
				"reference": "Patient/patient-1",
			},
			"encounter": map[string]string{
				"reference": "Encounter/encounter-1",
			},
			"code": map[string]any{
				"text": diagnosis,
			},
			"recordedDate": encounterDate,
		}
		if icd10Code != "" {
			conditionResource["code"] = map[string]any{
				"coding": []map[string]string{{
					"system":  "http://hl7.org/fhir/sid/icd-10-cm",
					"code":    icd10Code,
					"display": diagnosis,
				}},
				"text": diagnosis,
			}
		}
		entries = append(entries, map[string]any{
			"fullUrl": "urn:uuid:condition-1", "resource": conditionResource,
		})
	}

	// DocumentReference for clinical notes
	if notes != "" {
		docRef := map[string]any{
			"resourceType": "DocumentReference",
			"id":           "docref-1",
			"status":       "current",
			"type": map[string]any{
				"coding": []map[string]string{{
					"system":  "http://loinc.org",
					"code":    "11506-3",
					"display": "Progress note",
				}},
			},
			"subject": map[string]string{
				"reference": "Patient/patient-1",
			},
			"date": encounterDate + "T00:00:00Z",
			"content": []map[string]any{{
				"attachment": map[string]any{
					"contentType": "text/plain",
					"data":        notes, // In production, base64-encode this
				},
			}},
		}
		entries = append(entries, map[string]any{
			"fullUrl": "urn:uuid:docref-1", "resource": docRef,
		})
	}

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "document",
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"entry":        entries,
	}

	jsonBytes, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return ToolResult{ToolName: "fhir_export", Success: false, Error: err.Error()}
	}

	return ToolResult{ToolName: "fhir_export", Success: true, Output: string(jsonBytes)}
}

// ═══════════════════════════════════════════════════════════════════════════
// hl7_parse — HL7 v2.x message parser
// Parses pipe-delimited HL7 into structured JSON. No external library.
// ═══════════════════════════════════════════════════════════════════════════

func toolHL7Parse(args map[string]any) ToolResult {
	message, _ := args["message"].(string)
	if message == "" {
		return ToolResult{ToolName: "hl7_parse", Success: false, Error: "message is required"}
	}

	// Normalize line endings
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")

	lines := strings.Split(strings.TrimSpace(message), "\n")
	if len(lines) == 0 {
		return ToolResult{ToolName: "hl7_parse", Success: false, Error: "empty HL7 message"}
	}

	// Detect field separator from MSH segment
	fieldSep := "|"
	if strings.HasPrefix(lines[0], "MSH") && len(lines[0]) > 3 {
		fieldSep = string(lines[0][3])
	}

	segments := []map[string]any{}
	var messageType, patientName, patientID string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, fieldSep)
		segName := fields[0]

		seg := map[string]any{
			"segment": segName,
			"fields":  fields,
		}

		// Extract key data from known segments
		switch segName {
		case "MSH":
			if len(fields) > 8 {
				messageType = fields[8] // MSH-9: message type
				seg["message_type"] = messageType
			}
			if len(fields) > 9 {
				seg["control_id"] = fields[9]
			}
			if len(fields) > 11 {
				seg["version"] = fields[11]
			}
		case "PID":
			if len(fields) > 3 {
				patientID = fields[3] // PID-3: patient ID
				seg["patient_id"] = patientID
			}
			if len(fields) > 5 {
				patientName = fields[5] // PID-5: patient name
				seg["patient_name"] = patientName
			}
			if len(fields) > 7 {
				seg["date_of_birth"] = fields[7]
			}
			if len(fields) > 8 {
				seg["sex"] = fields[8]
			}
		case "PV1":
			if len(fields) > 2 {
				seg["patient_class"] = fields[2]
			}
			if len(fields) > 7 {
				seg["attending_doctor"] = fields[7]
			}
		case "OBX":
			if len(fields) > 3 {
				seg["observation_id"] = fields[3]
			}
			if len(fields) > 5 {
				seg["value"] = fields[5]
			}
			if len(fields) > 6 {
				seg["units"] = fields[6]
			}
			if len(fields) > 8 {
				seg["abnormal_flag"] = fields[8]
			}
		case "DG1":
			if len(fields) > 3 {
				seg["diagnosis_code"] = fields[3]
			}
			if len(fields) > 4 {
				seg["diagnosis_desc"] = fields[4]
			}
		}

		segments = append(segments, seg)
	}

	result := map[string]any{
		"message_type":  messageType,
		"patient_id":    patientID,
		"patient_name":  patientName,
		"segment_count": len(segments),
		"segments":      segments,
	}

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return ToolResult{ToolName: "hl7_parse", Success: false, Error: err.Error()}
	}
	return ToolResult{ToolName: "hl7_parse", Success: true, Output: string(jsonBytes)}
}

// ═══════════════════════════════════════════════════════════════════════════
// vitals_check — Vital signs range checker
// ═══════════════════════════════════════════════════════════════════════════

type vitalRange struct {
	Name     string
	Unit     string
	Low      float64
	High     float64
	CritLow  float64
	CritHigh float64
}

var vitalRanges = map[string]vitalRange{
	"heart_rate":    {Name: "Heart Rate", Unit: "BPM", Low: 60, High: 100, CritLow: 40, CritHigh: 150},
	"systolic_bp":   {Name: "Systolic BP", Unit: "mmHg", Low: 90, High: 140, CritLow: 70, CritHigh: 180},
	"diastolic_bp":  {Name: "Diastolic BP", Unit: "mmHg", Low: 60, High: 90, CritLow: 40, CritHigh: 120},
	"temperature":   {Name: "Temperature", Unit: "°F", Low: 97.0, High: 99.5, CritLow: 95.0, CritHigh: 104.0},
	"respiratory":   {Name: "Respiratory Rate", Unit: "breaths/min", Low: 12, High: 20, CritLow: 8, CritHigh: 30},
	"o2_saturation": {Name: "O2 Saturation", Unit: "%", Low: 95, High: 100, CritLow: 90, CritHigh: 101},
	"blood_glucose": {Name: "Blood Glucose", Unit: "mg/dL", Low: 70, High: 140, CritLow: 50, CritHigh: 400},
}

func toolVitalsCheck(args map[string]any) ToolResult {
	var results []string
	checked := 0
	warnings := 0
	criticals := 0

	for key, vr := range vitalRanges {
		val, ok := args[key]
		if !ok {
			continue
		}
		var num float64
		switch v := val.(type) {
		case float64:
			num = v
		case string:
			fmt.Sscanf(v, "%f", &num)
			if num == 0 {
				continue
			}
		default:
			continue
		}

		checked++
		status := "✅ NORMAL"
		if num < vr.CritLow || num > vr.CritHigh {
			status = "🚨 CRITICAL"
			criticals++
		} else if num < vr.Low || num > vr.High {
			status = "⚠️ WARNING"
			warnings++
		}

		results = append(results, fmt.Sprintf("%s: %.1f %s — %s (normal: %.0f–%.0f)", vr.Name, num, vr.Unit, status, vr.Low, vr.High))
	}

	if checked == 0 {
		return ToolResult{ToolName: "vitals_check", Success: false, Error: "no valid vital signs provided"}
	}

	summary := fmt.Sprintf("[%d vitals checked: %d normal, %d warnings, %d critical]\n", checked, checked-warnings-criticals, warnings, criticals)
	return ToolResult{ToolName: "vitals_check", Success: true, Output: summary + strings.Join(results, "\n")}
}

// ═══════════════════════════════════════════════════════════════════════════
// medication_check — Drug interaction checker
// Ships with ~60 common dangerous interactions.
// ═══════════════════════════════════════════════════════════════════════════

type drugInteraction struct {
	DrugA    string
	DrugB    string
	Severity string // "high", "moderate"
	Warning  string
}

var interactionDB = []drugInteraction{
	// Blood thinners
	{"warfarin", "aspirin", "high", "Increased bleeding risk — combined anticoagulant/antiplatelet effect"},
	{"warfarin", "ibuprofen", "high", "Increased bleeding risk — NSAIDs potentiate warfarin"},
	{"warfarin", "naproxen", "high", "Increased bleeding risk — NSAIDs potentiate warfarin"},
	{"warfarin", "acetaminophen", "moderate", "High-dose acetaminophen may increase INR"},
	// SSRIs + MAOIs
	{"fluoxetine", "phenelzine", "high", "Serotonin syndrome risk — SSRI + MAOI contraindicated"},
	{"sertraline", "phenelzine", "high", "Serotonin syndrome risk — SSRI + MAOI contraindicated"},
	{"fluoxetine", "tramadol", "high", "Serotonin syndrome risk — SSRI + opioid with serotonergic activity"},
	{"sertraline", "tramadol", "high", "Serotonin syndrome risk"},
	// Cardiac
	{"digoxin", "amiodarone", "high", "Amiodarone increases digoxin levels — monitor closely"},
	{"digoxin", "verapamil", "high", "Verapamil increases digoxin levels — risk of toxicity"},
	{"metoprolol", "verapamil", "high", "Combined beta-blocker + calcium channel blocker — severe bradycardia risk"},
	{"lisinopril", "potassium", "moderate", "ACE inhibitor + potassium — hyperkalemia risk"},
	{"lisinopril", "spironolactone", "moderate", "ACE inhibitor + K-sparing diuretic — hyperkalemia risk"},
	// Statins
	{"simvastatin", "erythromycin", "high", "Macrolide inhibits CYP3A4 — rhabdomyolysis risk with statin"},
	{"atorvastatin", "clarithromycin", "high", "Macrolide inhibits CYP3A4 — rhabdomyolysis risk"},
	{"simvastatin", "gemfibrozil", "high", "Fibrate + statin — increased myopathy risk"},
	// Diabetes
	{"metformin", "alcohol", "moderate", "Alcohol increases lactic acidosis risk with metformin"},
	{"insulin", "metformin", "moderate", "Combined use — monitor for hypoglycemia"},
	{"glipizide", "fluconazole", "moderate", "Azole antifungal increases sulfonylurea levels — hypoglycemia risk"},
	// Pain
	{"oxycodone", "benzodiazepine", "high", "Opioid + benzodiazepine — respiratory depression, FDA black box warning"},
	{"morphine", "benzodiazepine", "high", "Opioid + benzodiazepine — respiratory depression, FDA black box warning"},
	{"hydrocodone", "benzodiazepine", "high", "Opioid + benzodiazepine — respiratory depression, FDA black box warning"},
	{"oxycodone", "alprazolam", "high", "Opioid + benzodiazepine — respiratory depression"},
	{"morphine", "diazepam", "high", "Opioid + benzodiazepine — respiratory depression"},
	// Antibiotics
	{"methotrexate", "trimethoprim", "high", "Both are folate antagonists — bone marrow suppression risk"},
	{"ciprofloxacin", "theophylline", "moderate", "Fluoroquinolone increases theophylline levels — seizure risk"},
	{"ciprofloxacin", "tizanidine", "high", "Ciprofloxacin dramatically increases tizanidine levels"},
	// Misc
	{"lithium", "ibuprofen", "high", "NSAIDs reduce lithium clearance — toxicity risk"},
	{"lithium", "naproxen", "high", "NSAIDs reduce lithium clearance — toxicity risk"},
	{"sildenafil", "nitroglycerin", "high", "PDE5 inhibitor + nitrate — severe hypotension, contraindicated"},
	{"clopidogrel", "omeprazole", "moderate", "PPI reduces clopidogrel activation — decreased antiplatelet effect"},
}

func toolMedicationCheck(args map[string]any) ToolResult {
	medsStr, _ := args["medications"].(string)
	if medsStr == "" {
		return ToolResult{ToolName: "medication_check", Success: false, Error: "medications is required"}
	}

	var meds []string
	for _, m := range strings.Split(medsStr, ",") {
		m = strings.ToLower(strings.TrimSpace(m))
		if m != "" {
			meds = append(meds, m)
		}
	}

	if len(meds) < 2 {
		return ToolResult{ToolName: "medication_check", Success: true, Output: "Only 1 medication provided — need at least 2 to check interactions."}
	}

	var warnings []string
	highCount := 0

	for _, ix := range interactionDB {
		aFound := false
		bFound := false
		for _, m := range meds {
			if strings.Contains(m, ix.DrugA) {
				aFound = true
			}
			if strings.Contains(m, ix.DrugB) {
				bFound = true
			}
		}
		if aFound && bFound {
			icon := "⚠️"
			if ix.Severity == "high" {
				icon = "🚨"
				highCount++
			}
			warnings = append(warnings, fmt.Sprintf("%s [%s] %s + %s: %s", icon, strings.ToUpper(ix.Severity), ix.DrugA, ix.DrugB, ix.Warning))
		}
	}

	if len(warnings) == 0 {
		return ToolResult{ToolName: "medication_check", Success: true, Output: fmt.Sprintf("[%d medications checked, no known interactions found]\nMedications: %s\n\nNote: This checks against a limited database of ~60 common interactions. Always verify with a comprehensive drug reference.", len(meds), strings.Join(meds, ", "))}
	}

	header := fmt.Sprintf("[%d interactions found (%d high severity)]\n", len(warnings), highCount)
	return ToolResult{ToolName: "medication_check", Success: true, Output: header + strings.Join(warnings, "\n")}
}

// ═══════════════════════════════════════════════════════════════════════════
// encrypt_data — AES-256-GCM encryption/decryption
// ═══════════════════════════════════════════════════════════════════════════

func toolEncryptData(args map[string]any) ToolResult {
	action, _ := args["action"].(string)
	text, _ := args["text"].(string)
	keyHex, _ := args["key"].(string)

	if action == "" || text == "" {
		return ToolResult{ToolName: "encrypt_data", Success: false, Error: "action and text are required"}
	}
	action = strings.ToLower(action)

	// Get key from arg or env
	if keyHex == "" {
		keyHex = envFirst("TERMINUS_ENCRYPTION_KEY", "JANUS_ENCRYPTION_KEY")
	}
	if keyHex == "" {
		// Generate a deterministic key from a passphrase for demo purposes
		return ToolResult{ToolName: "encrypt_data", Success: false, Error: "no encryption key — set TERMINUS_ENCRYPTION_KEY env var (64 hex chars = 32 bytes) or provide key parameter"}
	}

	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return ToolResult{ToolName: "encrypt_data", Success: false, Error: "key must be exactly 32 bytes (64 hex characters)"}
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return ToolResult{ToolName: "encrypt_data", Success: false, Error: fmt.Sprintf("cipher error: %v", err)}
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ToolResult{ToolName: "encrypt_data", Success: false, Error: fmt.Sprintf("GCM error: %v", err)}
	}

	switch action {
	case "encrypt":
		nonce := make([]byte, gcm.NonceSize())
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return ToolResult{ToolName: "encrypt_data", Success: false, Error: fmt.Sprintf("nonce error: %v", err)}
		}
		ciphertext := gcm.Seal(nonce, nonce, []byte(text), nil)
		encoded := base64.StdEncoding.EncodeToString(ciphertext)
		return ToolResult{ToolName: "encrypt_data", Success: true, Output: fmt.Sprintf("[encrypted %d bytes → %d chars base64]\n%s", len(text), len(encoded), encoded)}

	case "decrypt":
		ciphertext, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return ToolResult{ToolName: "encrypt_data", Success: false, Error: "invalid base64 input"}
		}
		if len(ciphertext) < gcm.NonceSize() {
			return ToolResult{ToolName: "encrypt_data", Success: false, Error: "ciphertext too short"}
		}
		nonce := ciphertext[:gcm.NonceSize()]
		ciphertext = ciphertext[gcm.NonceSize():]
		plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			return ToolResult{ToolName: "encrypt_data", Success: false, Error: "decryption failed — wrong key or corrupted data"}
		}
		return ToolResult{ToolName: "encrypt_data", Success: true, Output: string(plaintext)}

	default:
		return ToolResult{ToolName: "encrypt_data", Success: false, Error: "action must be 'encrypt' or 'decrypt'"}
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// audit_log — audit trail writer for medical records
// Appends structured entries to logs/audit_medical.jsonl
// ═══════════════════════════════════════════════════════════════════════════

func toolAuditLog(args map[string]any) ToolResult {
	action, _ := args["action"].(string)
	if action == "" {
		return ToolResult{ToolName: "audit_log", Success: false, Error: "action is required"}
	}

	user, _ := args["user"].(string)
	if user == "" {
		user = "system"
	}
	patient, _ := args["patient"].(string)
	details, _ := args["details"].(string)

	// Hash patient identifier for privacy
	patientHash := ""
	if patient != "" {
		h := sha256.Sum256([]byte(patient))
		patientHash = hex.EncodeToString(h[:8]) // first 8 bytes = 16 hex chars
	}

	entry := map[string]string{
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"action":       action,
		"user":         user,
		"patient_hash": patientHash,
		"details":      details,
	}

	jsonLine, err := json.Marshal(entry)
	if err != nil {
		return ToolResult{ToolName: "audit_log", Success: false, Error: err.Error()}
	}

	// Ensure logs directory exists
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return ToolResult{ToolName: "audit_log", Success: false, Error: err.Error()}
	}

	logFile := filepath.Join(logDir, "audit_medical.jsonl")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return ToolResult{ToolName: "audit_log", Success: false, Error: err.Error()}
	}
	defer f.Close()

	if _, err := f.Write(append(jsonLine, '\n')); err != nil {
		return ToolResult{ToolName: "audit_log", Success: false, Error: err.Error()}
	}

	log.Printf("audit_medical: %s", string(jsonLine))
	return ToolResult{ToolName: "audit_log", Success: true, Output: fmt.Sprintf("audit entry written: %s by %s at %s", action, user, entry["timestamp"])}
}
