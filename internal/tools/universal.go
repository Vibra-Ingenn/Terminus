package tools

// Universal ingestion + output tools. These are the "any input / any output"
// layer that lets Terminus deal with messy real-world medical data (scanned
// faxes, Word docs, images, etc.) and emit polished documents back out.
//
// Design notes:
//   - Pure Go wherever possible. Only ocr_extract and image_extract shell out
//     to tesseract via run_command, because there is no viable pure-Go OCR.
//   - Every tool here is best-effort and degrades gracefully. A failure in
//     one parser does not take down the kernel.
//   - Output renderers always write to workspace/outputs/ with a timestamped
//     filename so parallel calls never clobber each other.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"terminus/internal/docpipeline"
)

const outputDir = "workspace/outputs"

// ═══════════════════════════════════════════════════════════════════════════
// registerUniversalTools — plugs the 6 universal tools into the registry.
// ═══════════════════════════════════════════════════════════════════════════

func (r *Registry) registerUniversalTools() {
	r.Register(ToolDef{
		Name: "document_parse",
		Description: "Canonical document parser. Use this first for any PDF, DOCX, text, image, scan, or medical document. " +
			"Returns structured JSON with status, extraction method, text blocks, fields, warnings, and fallback guidance.",
		Parameters: map[string]string{
			"path":        "Path to the document file.",
			"include_raw": "(optional) If 'true', include extracted raw text in the JSON output. Default false.",
		},
		Category: "document",
	}, r.toolDocumentParse)

	r.Register(ToolDef{
		Name:        "auto_ingest",
		Description: "Detects a file's type by magic bytes and extension, then routes it to the correct parser (PDF, DOCX, HL7, image OCR, plain text). Use this when you do not know what kind of file the user uploaded.",
		Parameters: map[string]string{
			"path": "Path to any file. Supported: PDF, DOCX, TXT/MD/CSV/JSON/XML/HL7, PNG/JPG (OCR).",
		},
		Category: "document",
	}, toolAutoIngest)

	r.Register(ToolDef{
		Name:        "ocr_extract",
		Description: "Runs Tesseract OCR on a scanned PDF or image file. Use this when pdf_extract returned '(no text found)' — the PDF is a scanned fax or image-only.",
		Parameters: map[string]string{
			"path":     "Path to a PDF or image file (PNG, JPG, TIFF, BMP).",
			"language": "(optional) Tesseract language code, e.g. 'eng', 'spa', 'eng+spa'. Default 'eng'.",
		},
		Category: "document",
	}, toolOCRExtract)

	r.Register(ToolDef{
		Name:        "docx_extract",
		Description: "Extracts plain text from a Microsoft Word .docx file using pure Go (no external tools). Use this for Word documents, referral letters, transcribed notes.",
		Parameters: map[string]string{
			"path": "Path to a .docx file.",
		},
		Category: "document",
	}, toolDOCXExtract)

	r.Register(ToolDef{
		Name:        "image_extract",
		Description: "Runs OCR on an image file (PNG, JPG, TIFF, BMP) using Tesseract. Use this for photos of documents, handwritten notes, whiteboards, etc.",
		Parameters: map[string]string{
			"path":     "Path to an image file.",
			"language": "(optional) Tesseract language code, default 'eng'.",
		},
		Category: "document",
	}, toolImageExtract)

	r.Register(ToolDef{
		Name:        "render_pdf",
		Description: "Generates a formatted PDF file from plain text or simple Markdown. Writes to workspace/outputs/ and returns the path. Use this when the user asks for a polished PDF output (letter, report, discharge summary).",
		Parameters: map[string]string{
			"content":  "The text or Markdown content to render.",
			"title":    "(optional) Document title shown at top of the page.",
			"filename": "(optional) Base filename without extension. Default: 'document'.",
		},
		Category: "document",
	}, toolRenderPDF)

	r.Register(ToolDef{
		Name:        "render_docx",
		Description: "Generates a Microsoft Word .docx file from plain text. Writes to workspace/outputs/ and returns the path. Use this when the user wants an editable Word document.",
		Parameters: map[string]string{
			"content":  "The text content to write into the document.",
			"title":    "(optional) Document title shown at top as a heading.",
			"filename": "(optional) Base filename without extension. Default: 'document'.",
		},
		Category: "document",
	}, toolRenderDOCX)

	r.Register(ToolDef{
		Name:        "heal_document",
		Description: "Attempt to read, diagnose, and automatically repair any document (PDF, DOCX, HL7, text, image). Returns a structured report with recovered text, issues found, repairs applied, and specific instructions for the user if full auto-repair was not possible. Use this when a file fails to open, parse, or extract normally.",
		Parameters: map[string]string{
			"path":       "Path to the document to heal (any supported file type).",
			"output_dir": "(optional) Directory to write a repaired copy of the file. Defaults to workspace/outputs.",
		},
		Category: "document",
	}, toolHealDocument)

	r.Register(ToolDef{
		Name: "extract_medical_entities",
		Description: "Run the Stage 0-4 layered document perception pipeline on any document. " +
			"Stage 0: extract raw text (PDF/DOCX/OCR). Stage 1: generate multi-hypothesis candidates with confidence scores. " +
			"Stage 2: verify consistency (never rewrites, only flags conflicts). Stage 3: deterministically resolve to best candidates. " +
			"Stage 4: return structured JSON. Use this instead of pdf_extract+anonymize when you need structured fields from messy documents.",
		Parameters: map[string]string{
			"path":              "Path to the document (PDF, DOCX, image, or plain text file).",
			"include_ambiguous": "(optional) If 'true', include fields with conflicting candidates in output. Default false (safe).",
		},
		Category: "document",
	}, r.toolExtractMedicalEntities)

	r.Register(ToolDef{
		Name: "generate_tool",
		Description: "Generate a custom community tool definition (JSON recipe) from a description of what you want the tool to do. " +
			"The tool JSON will be written to internal/protocols/ so it is auto-ingested by the system.",
		Parameters: map[string]string{
			"description": "Description of what the tool should do (e.g. 'Run speedtest and return output', 'Make a POST request to slack with a message'). Be specific about the command or URL, parameters expected, and tags.",
			"tool_name":   "(optional) Suggested lowercase_snake_case name for the tool.",
		},
		Category: "skills",
	}, r.toolGenerateTool)
}

type documentParsePage struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type documentTextBlock struct {
	Page int    `json:"page"`
	Type string `json:"type"`
	Text string `json:"text"`
}

type documentParseOutput struct {
	Status             string                         `json:"status"`
	Method             string                         `json:"method"`
	Path               string                         `json:"path"`
	Pages              []documentParsePage            `json:"pages"`
	TextBlocks         []documentTextBlock            `json:"text_blocks"`
	Tables             []any                          `json:"tables"`
	Fields             map[string]string              `json:"fields"`
	VerificationStatus docpipeline.VerificationStatus `json:"verification_status,omitempty"`
	Entities           []docpipeline.ResolvedEntity   `json:"entities,omitempty"`
	AmbiguousKeys      []string                       `json:"ambiguous_keys,omitempty"`
	Warnings           []string                       `json:"warnings,omitempty"`
	Notes              []string                       `json:"notes,omitempty"`
	DocumentType       string                         `json:"document_type,omitempty"`
	RawText            string                         `json:"raw_text,omitempty"`
}

func (r *Registry) toolDocumentParse(args map[string]any) ToolResult {
	const toolName = "document_parse"

	path := firstStringArg(args, "path", "file_path", "document_path", "file")
	if path == "" {
		log.Printf("[DOC_PARSE_ERROR] missing path argument")
		return ToolResult{ToolName: toolName, Success: false, Error: "path is required"}
	}
	log.Printf("[DOC_PARSE_START] parsing document at: %s", path)
	includeRaw := strings.EqualFold(firstStringArg(args, "include_raw"), "true")

	pack := firstStringArg(args, "pack", "packs")
	if pack == "" {
		pack = os.Getenv("TERMINUS_PACKS")
	}
	var packs []string
	if pack != "" {
		for _, p := range strings.Split(pack, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				packs = append(packs, p)
			}
		}
	}

	parsed, err := r.parseDocumentForTools(path, includeRaw, packs...)
	if err != nil {
		log.Printf("[DOC_PARSE_FAIL] error parsing %s: %v", path, err)
		return ToolResult{ToolName: toolName, Success: false, Error: err.Error()}
	}
	textLen := len(parsed.RawText)
	for _, p := range parsed.Pages {
		textLen += len(p.Text)
	}
	parsed.Warnings, parsed.Notes = splitNotes(parsed.Warnings, textLen)
	b, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: fmt.Sprintf("marshal: %v", err)}
	}
	log.Printf("[DOC_PARSE_SUCCESS] method=%s status=%s fields_extracted=%d verification=%s", parsed.Method, parsed.Status, len(parsed.Fields), parsed.VerificationStatus)
	if len(parsed.Warnings) > 0 {
		log.Printf("[DOC_PARSE_WARNINGS] %s: %s", path, strings.Join(parsed.Warnings, " | "))
	}
	return ToolResult{ToolName: toolName, Success: true, Output: string(b)}
}

func (r *Registry) parseDocumentForTools(path string, includeRaw bool, packs ...string) (documentParseOutput, error) {
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	if _, err := os.Stat(path); err != nil {
		return documentParseOutput{}, fmt.Errorf("file not found at %s", path)
	}

	out := documentParseOutput{
		Status: "failed",
		Method: "none",
		Path:   path,
		Fields: make(map[string]string),
		Tables: []any{},
	}

	rawText, method, extractWarns, err := extractRawText(path)
	if err != nil {
		out.Status = classifyDocumentParseFailure(err)
		out.Method = method
		out.Warnings = append(out.Warnings, err.Error())
		if out.Status == "needs_ocr" {
			out.Warnings = append(out.Warnings, "install or repair OCR, then retry document_parse")
		}
		if out.Status == "needs_vision" {
			out.Warnings = append(out.Warnings, "native text extraction failed; use OCR or vision fallback")
		}
		return out, nil
	}

	out.Warnings = append(out.Warnings, extractWarns...)
	rawText, labelWarns := CleanExtractionBody(rawText)
	out.Warnings = append(out.Warnings, labelWarns...)
	rawText = strings.TrimSpace(rawText)
	if rawText == "" || strings.Contains(rawText, "returned no text") {
		out.Status = "needs_vision"
		out.Method = method
		out.Warnings = append(out.Warnings, "no usable text was extracted")
		return out, nil
	}

	pr, vr := docpipeline.RunWithEngine(context.Background(), r.Engine, rawText, r.Engine != nil, packs...)
	out.Status = "ok"
	out.Method = method
	out.VerificationStatus = vr.Status
	out.Entities = pr.Entities
	out.AmbiguousKeys = pr.AmbiguousKeys
	var postWarns []string
	out.Fields, postWarns = pr.ToProcessedFieldMap(true)
	out.Warnings = append(out.Warnings, postWarns...)
	out.Warnings = append(out.Warnings, vr.Conflicts...)
	out.Fields = docpipeline.ClassifyDocument(rawText, out.Fields, docpipeline.HasMedicalPack(packs))
	out.DocumentType = out.Fields["document_type"]
	if !docpipeline.HasMedicalPack(packs) {
		filteredFields := make(map[string]string)
		for k, v := range out.Fields {
			if !docpipeline.IsMedicalField(k) {
				filteredFields[k] = v
			}
		}
		out.Fields = filteredFields

		var filteredEntities []docpipeline.ResolvedEntity
		for _, e := range out.Entities {
			if !docpipeline.IsMedicalField(e.Field) {
				filteredEntities = append(filteredEntities, e)
			}
		}
		out.Entities = filteredEntities

		var filteredAmbiguous []string
		for _, k := range out.AmbiguousKeys {
			if !docpipeline.IsMedicalField(k) {
				filteredAmbiguous = append(filteredAmbiguous, k)
			}
		}
		out.AmbiguousKeys = filteredAmbiguous
	}
	if out.DocumentType == "document" {
		// Not a bill, statement, letter or medical record. Bill fields pulled
		// from a manual or a book are only guesses (a part number as a
		// "Social Security number", a heading as the "vendor"), so leave them
		// out along with their warnings.
		out.Fields = documentOnlyFields(out.Fields)
		out.Entities = nil
		out.AmbiguousKeys = nil
		out.VerificationStatus = ""
		out.Warnings = dropFieldWarnings(out.Warnings)
	}
	docType := strings.TrimSpace(out.Fields["document_type"])
	nonMeta := 0
	for k, v := range out.Fields {
		if k == "document_type" || k == "pages" {
			continue
		}
		if strings.TrimSpace(v) != "" {
			nonMeta++
		}
	}
	if nonMeta == 0 {
		if docpipeline.HasMedicalPack(packs) && docType == "medical_record" {
			out.Warnings = append(out.Warnings, "text extracted, but no structured medical fields were detected")
		} else if docType != "document" {
			out.Warnings = append(out.Warnings, "text extracted, but no structured fields were detected")
		}
	}

	pageTexts := splitPageTexts(rawText)
	if len(pageTexts) == 0 {
		pageTexts = []string{rawText}
	}
	for i, pt := range pageTexts {
		if len(pt) > 12000 {
			pt = cutUTF8(pt, 12000) + "\n\n... [truncated]"
		}
		out.Pages = append(out.Pages, documentParsePage{Number: i + 1, Text: pt})
		for _, block := range splitTextBlocks(pt) {
			out.TextBlocks = append(out.TextBlocks, documentTextBlock{
				Page: i + 1,
				Type: "paragraph",
				Text: block,
			})
		}
	}
	if docType == "document" && strings.TrimSpace(out.Fields["pages"]) == "" {
		out.Fields["pages"] = strconv.Itoa(len(pageTexts))
	}
	if includeRaw || len(out.Fields) < 3 {
		out.RawText = rawText
	}
	return out, nil
}

// documentOnlyFields keeps what a plain document has: its type, title, author
// and page count.
func documentOnlyFields(fields map[string]string) map[string]string {
	out := make(map[string]string)
	for _, k := range []string{"document_type", "title", "author", "pages"} {
		if v := strings.TrimSpace(fields[k]); v != "" {
			out[k] = v
		}
	}
	return out
}

// dropFieldWarnings removes the warnings about individual fields (candidate
// lists, overlaps, low confidence), for documents whose fields were dropped.
func dropFieldWarnings(warnings []string) []string {
	var out []string
	for _, w := range warnings {
		if strings.HasPrefix(w, "field ") || strings.HasPrefix(w, "span overlap:") ||
			w == "all candidates below confidence threshold" {
			continue
		}
		out = append(out, w)
	}
	return out
}

var (
	reInfoNote = regexp.MustCompile(`^\d+ pages? read (?:from the PDF text layer|by OCR)$`)
	reSymbols  = regexp.MustCompile(`^some symbols in this PDF couldn't be read as text \((\d+)\)$`)
)

// splitNotes moves plain information ("12 pages read from the PDF text
// layer") out of warnings, so a warning always means something to look at.
// The per-page "symbols couldn't be read" lines become one line; when they
// are under 1% of the text (a few maths signs in a book) it's only a note.
func splitNotes(warnings []string, textLen int) (warns, notes []string) {
	symbols := 0
	for _, w := range warnings {
		switch m := reSymbols.FindStringSubmatch(w); {
		case m != nil:
			n, _ := strconv.Atoi(m[1])
			symbols += n
		case reInfoNote.MatchString(w):
			notes = append(notes, w)
		default:
			warns = append(warns, w)
		}
	}
	if symbols > 0 {
		line := fmt.Sprintf("some symbols in this PDF couldn't be read as text (%d)", symbols)
		if symbols*100 < textLen {
			notes = append(notes, line)
		} else {
			warns = append(warns, line)
		}
	}
	return warns, notes
}

func classifyDocumentParseFailure(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "turned into drawings") || strings.Contains(msg, "is locked"):
		// The message already says what to do.
		return "failed"
	case strings.Contains(msg, "pdf reading is not supported") || strings.Contains(msg, "cannot read pdf"):
		return "needs_vision"
	case strings.Contains(msg, "tesseract") || strings.Contains(msg, "ocr"):
		return "needs_ocr"
	case strings.Contains(msg, "no text") || strings.Contains(msg, "scanned") ||
		strings.Contains(msg, "image") || strings.Contains(msg, "encrypted"):
		return "needs_vision"
	default:
		return "failed"
	}
}

func splitTextBlocks(text string) []string {
	var blocks []string
	for _, part := range regexp.MustCompile(`\n\s*\n`).Split(text, -1) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(part) > 2000 {
			part = part[:2000] + "\n... [truncated block]"
		}
		blocks = append(blocks, part)
		if len(blocks) >= 40 {
			break
		}
	}
	if len(blocks) == 0 && strings.TrimSpace(text) != "" {
		blocks = append(blocks, strings.TrimSpace(text))
	}
	return blocks
}

func firstStringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (r *Registry) repairRenderContentArgs(args map[string]any) map[string]any {
	if firstStringArg(args, "content") != "" {
		return args
	}
	path := firstStringArg(args, "path")
	if path == "" {
		return args
	}

	repaired := cloneArgs(args)
	parsed, err := r.parseDocumentForTools(path, false)
	if err != nil {
		repaired["content"] = fmt.Sprintf("Document parse failed for %s:\n%s", path, err)
		return repaired
	}
	repaired["content"] = documentParseReport(parsed)
	if firstStringArg(repaired, "title") == "" {
		repaired["title"] = "Parsed Document"
	}
	if firstStringArg(repaired, "filename") == "" {
		repaired["filename"] = "parsed-document"
	}
	return repaired
}

func (r *Registry) repairChartTemplateArgs(args map[string]any) map[string]any {
	path := firstStringArg(args, "path")
	if path == "" {
		return args
	}
	if firstStringArg(args, "subjective", "objective", "assessment", "plan", "notes") != "" {
		return args
	}

	repaired := cloneArgs(args)
	parsed, err := r.parseDocumentForTools(path, false, "medical")
	if err != nil {
		repaired["notes"] = fmt.Sprintf("Document parse failed for %s: %s", path, err)
		return repaired
	}

	if firstStringArg(repaired, "patient_name") == "" {
		if name := parsed.Fields["patient_name"]; name != "" {
			repaired["patient_name"] = name
		}
	}
	if firstStringArg(repaired, "subjective") == "" {
		repaired["subjective"] = bestDocumentText(parsed)
	}
	if firstStringArg(repaired, "objective") == "" {
		repaired["objective"] = formatParsedFields(parsed.Fields)
	}
	if firstStringArg(repaired, "notes") == "" {
		repaired["notes"] = documentParseReport(parsed)
	}
	return repaired
}

func cloneArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

func bestDocumentText(parsed documentParseOutput) string {
	if len(parsed.TextBlocks) > 0 {
		return parsed.TextBlocks[0].Text
	}
	if len(parsed.Pages) > 0 {
		return parsed.Pages[0].Text
	}
	if len(parsed.Warnings) > 0 {
		return "Document parsing did not produce clean text: " + strings.Join(parsed.Warnings, "; ")
	}
	return "(document parser returned no text)"
}

func formatParsedFields(fields map[string]string) string {
	if len(fields) == 0 {
		return "(no structured fields detected)"
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&sb, "%s: %s\n", key, fields[key])
	}
	return strings.TrimSpace(sb.String())
}

func documentParseReport(parsed documentParseOutput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Document parse status: %s\n", parsed.Status)
	fmt.Fprintf(&sb, "Method: %s\n", parsed.Method)
	if parsed.VerificationStatus != "" {
		fmt.Fprintf(&sb, "Verification: %s\n", parsed.VerificationStatus)
	}
	if len(parsed.Fields) > 0 {
		sb.WriteString("\nFields:\n")
		sb.WriteString(formatParsedFields(parsed.Fields))
		sb.WriteString("\n")
	}
	if len(parsed.Warnings) > 0 {
		sb.WriteString("\nWarnings:\n")
		for _, warning := range parsed.Warnings {
			fmt.Fprintf(&sb, "- %s\n", warning)
		}
	}
	if text := bestDocumentText(parsed); text != "" {
		sb.WriteString("\nExtracted text:\n")
		sb.WriteString(text)
	}
	return strings.TrimSpace(sb.String())
}

// ═══════════════════════════════════════════════════════════════════════════
// auto_ingest — Universal file-type router
// ═══════════════════════════════════════════════════════════════════════════

func toolAutoIngest(args map[string]any) ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{ToolName: "auto_ingest", Success: false, Error: "path is required"}
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return ToolResult{ToolName: "auto_ingest", Success: false, Error: fmt.Sprintf("cannot stat %s: %v", path, err)}
	}
	if info.IsDir() {
		return ToolResult{ToolName: "auto_ingest", Success: false, Error: "path is a directory, not a file"}
	}

	kind, err := detectFileType(path)
	if err != nil {
		return ToolResult{ToolName: "auto_ingest", Success: false, Error: fmt.Sprintf("cannot detect file type: %v", err)}
	}

	log.Printf("auto_ingest: %s detected as %s (size=%d)", filepath.Base(path), kind, info.Size())

	// Route to the appropriate extractor. Every branch returns a ToolResult
	// tagged as "auto_ingest" with the underlying tool's output, so the AI
	// can see which path was taken.
	switch kind {
	case "pdf":
		res := toolPDFExtract(map[string]any{"path": path})
		return wrapIngest("pdf", res)
	case "docx":
		res := toolDOCXExtract(map[string]any{"path": path})
		return wrapIngest("docx", res)
	case "image":
		res := toolImageExtract(map[string]any{"path": path})
		return wrapIngest("image", res)
	case "hl7":
		// HL7 needs the message body, not a path — read the file first.
		data, err := os.ReadFile(path)
		if err != nil {
			return ToolResult{ToolName: "auto_ingest", Success: false, Error: fmt.Sprintf("read hl7: %v", err)}
		}
		res := toolHL7Parse(map[string]any{"message": string(data)})
		return wrapIngest("hl7", res)
	case "text":
		// Plain text, CSV, JSON, XML, MD — read directly with size cap.
		data, err := readCapped(path, 128*1024)
		if err != nil {
			return ToolResult{ToolName: "auto_ingest", Success: false, Error: fmt.Sprintf("read text: %v", err)}
		}
		return ToolResult{
			ToolName: "auto_ingest",
			Success:  true,
			Output:   fmt.Sprintf("[detected=text size=%d]\n%s", info.Size(), string(data)),
		}
	default:
		return ToolResult{
			ToolName: "auto_ingest",
			Success:  false,
			Error:    fmt.Sprintf("unsupported file type %q — try read_file for raw bytes or tell the user the format is not supported", kind),
		}
	}
}

// wrapIngest rewrites a sub-tool's result to look like an auto_ingest result
// so the AI sees a consistent tool name in the conversation history.
func wrapIngest(detected string, res ToolResult) ToolResult {
	prefix := fmt.Sprintf("[detected=%s via=%s]\n", detected, res.ToolName)
	return ToolResult{
		ToolName: "auto_ingest",
		Success:  res.Success,
		Output:   prefix + res.Output,
		Error:    res.Error,
	}
}

// detectFileType sniffs magic bytes with a filename extension fallback.
// Returns one of: pdf, docx, image, hl7, text, unknown.
func detectFileType(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read enough bytes to cover every magic header we care about.
	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]

	// Magic bytes take precedence over extension because users rename files.
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "pdf", nil
	case bytes.HasPrefix(head, []byte{0x50, 0x4B, 0x03, 0x04}): // ZIP header — docx/xlsx/pptx
		// Differentiate by extension since they share the ZIP header.
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".docx" {
			return "docx", nil
		}
		return "unknown", nil
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}): // JPEG
		return "image", nil
	case bytes.HasPrefix(head, []byte{0x89, 0x50, 0x4E, 0x47}): // PNG
		return "image", nil
	case bytes.HasPrefix(head, []byte{0x47, 0x49, 0x46}): // GIF
		return "image", nil
	case bytes.HasPrefix(head, []byte{0x42, 0x4D}): // BMP
		return "image", nil
	}

	// HL7 v2 messages start with "MSH|".
	if bytes.HasPrefix(bytes.TrimLeft(head, "\r\n\t "), []byte("MSH|")) {
		return "hl7", nil
	}

	// If it's mostly printable ASCII/UTF-8, treat as text.
	if isProbablyText(head) {
		return "text", nil
	}
	return "unknown", nil
}

// isProbablyText returns true if the buffer is at least 90% printable / UTF-8.
func isProbablyText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	printable := 0
	for _, c := range b {
		if c == '\t' || c == '\n' || c == '\r' || (c >= 0x20 && c < 0x7F) || c >= 0x80 {
			printable++
		}
	}
	return float64(printable)/float64(len(b)) > 0.90
}

func readCapped(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(max)))
}

// ═══════════════════════════════════════════════════════════════════════════
// ocr_extract — Tesseract wrapper for scanned PDFs & images
// ═══════════════════════════════════════════════════════════════════════════

func toolOCRExtract(args map[string]any) ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{ToolName: "ocr_extract", Success: false, Error: "path is required"}
	}
	lang, _ := args["language"].(string)
	if lang == "" || lang == "{{language}}" {
		lang = "eng"
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	if _, err := os.Stat(path); err != nil {
		return ToolResult{ToolName: "ocr_extract", Success: false, Error: fmt.Sprintf("file not found: %s", path)}
	}

	// Locate tesseract on PATH. If missing, give the user install instructions
	// instead of a cryptic exec failure.
	if _, err := findTesseractExecutable(); err != nil {
		return ToolResult{
			ToolName: "ocr_extract",
			Success:  false,
			Error:    "Tesseract is not installed. On Windows, install from https://github.com/UB-Mannheim/tesseract/wiki (default folder is fine). On Linux: apt install tesseract-ocr. Then restart Terminus.",
		}
	}

	// For PDFs we need to rasterize each page first — tesseract cannot read
	// PDFs directly unless compiled with Leptonica PDF support. We shell out
	// to `tesseract` with the PDF path; modern Windows builds DO support it.
	// If that fails, we fall back to informing the user.
	return runTesseract(path, lang, "ocr_extract")
}

// toolImageExtract is the same code path as OCR but named differently so
// the AI picks the right tool for the right intent.
func toolImageExtract(args map[string]any) ToolResult {
	res := toolOCRExtract(args)
	res.ToolName = "image_extract"
	return res
}

// ocrDPI is how finely a scanned PDF's pages are turned into pictures before
// OCR. 300 is the setting the scanned-statement results were checked at.
// TERMINUS_OCR_DPI can lower it (for example 150 or 200) to go faster; check
// the amounts on a real statement before trusting a lower setting.
func ocrDPI() string {
	if v := strings.TrimSpace(os.Getenv("TERMINUS_OCR_DPI")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 72 && n <= 600 {
			return strconv.Itoa(n)
		}
	}
	return "300"
}

// ocrPDFPages reads a scanned PDF: embedded page images first, then pdftoppm if needed.
func ocrPDFPages(path, lang, toolName string) ToolResult {
	tess, err := findTesseractExecutable()
	if err != nil {
		return ToolResult{
			ToolName: toolName,
			Success:  false,
			Error:    "This PDF is made of pictures. Reading it needs Tesseract. Install Tesseract, then try again.",
		}
	}
	dir, err := os.MkdirTemp("", "terminus-pages-*")
	if err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: fmt.Sprintf("tempdir: %v", err)}
	}
	defer os.RemoveAll(dir)

	pageImages, _ := extractPDFPageImagePaths(path, dir)
	if anyNonEmpty(pageImages) {
		res := ocrPageImageList(pageImages, lang, toolName, tess)
		if res.Success {
			return res
		}
	}
	if pdftoppm, err := findPdftoppmExecutable(); err == nil {
		return ocrPDFPagesWithPdftoppm(path, lang, toolName, tess, pdftoppm, dir)
	}
	// A scan has a picture on (nearly) every page. With pictures on only a
	// few pages (a logo), the words were most likely turned into drawn
	// shapes, and only Poppler can turn those pages into pictures for OCR.
	withImage := 0
	for _, p := range pageImages {
		if p != "" {
			withImage++
		}
	}
	if withImage > 0 && withImage*2 >= len(pageImages) {
		return ToolResult{ToolName: toolName, Success: false, Error: "This PDF is made of pictures and could not be read."}
	}
	return ToolResult{ToolName: toolName, Success: false, Error: "Terminus couldn't find any text in this PDF, and its pages aren't pictures it can read with OCR. If the text shows in a PDF viewer, it was probably turned into drawings: install Poppler (see README) and Terminus will read it with OCR."}
}

func anyNonEmpty(paths []string) bool {
	for _, p := range paths {
		if p != "" {
			return true
		}
	}
	return false
}

func ocrPageImageList(pageImages []string, lang, toolName, tess string) ToolResult {
	pages := make([]string, len(pageImages))
	var warns []string
	readable := 0
	for i, img := range pageImages {
		pageNum := i + 1
		if img == "" {
			warns = append(warns, ocrReadablePageWarning(pageNum))
			continue
		}
		text, err := runTesseractOnFile(tess, img, lang)
		if err != nil || strings.TrimSpace(text) == "" {
			warns = append(warns, ocrReadablePageWarning(pageNum))
			continue
		}
		pages[i] = strings.TrimSpace(text)
		readable++
	}
	if readable == 0 {
		return ToolResult{ToolName: toolName, Success: false, Error: strings.Join(warns, "; ")}
	}
	if readable == 1 {
		warns = append(warns, "1 page read by OCR")
	} else {
		warns = append(warns, fmt.Sprintf("%d pages read by OCR", readable))
	}
	body := attachExtractWarnings(joinPageTexts(pages), warns)
	return ToolResult{ToolName: toolName, Success: true, Output: body}
}

func ocrPDFPagesWithPdftoppm(path, lang, toolName, tess, pdftoppm, dir string) ToolResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, pdftoppm, "-r", ocrDPI(), "-png", path, filepath.Join(dir, "page"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ToolResult{
			ToolName: toolName,
			Success:  false,
			Error:    fmt.Sprintf("could not turn the PDF's pages into pictures: %v. %s", err, truncateStr(stderr.String(), 300)),
		}
	}
	pages, _ := filepath.Glob(filepath.Join(dir, "page*.png"))
	sort.Strings(pages)
	if len(pages) == 0 {
		return ToolResult{ToolName: toolName, Success: false, Error: "the PDF produced no page pictures to read"}
	}
	paths := make([]string, len(pages))
	copy(paths, pages)
	return ocrPageImageList(paths, lang, toolName, tess)
}

func runTesseractOnFile(tess, imagePath, lang string) (string, error) {
	text, _, err := ocrBestReading(tess, imagePath, lang)
	return text, err
}

// moneyAmount matches prices like 8.49 or 1,150.00.
var moneyAmount = regexp.MustCompile(`\b\d{1,3}(?:,\d{3})*\.\d{2}\b`)

// ocrBestReading reads an image twice: Tesseract's normal page layout, and "one block of text".
// On receipts the normal layout often drops the column of prices on the right; the block reading
// keeps each item on one line with its price. The reading with more prices wins; a tie keeps the
// normal layout, so books, letters and manuals read exactly as before.
func ocrBestReading(tess, imagePath, lang string) (string, string, error) {
	normal, stderr, err := tesseractOnce(tess, imagePath, lang, "")
	if err != nil {
		return "", stderr, err
	}
	block, _, err2 := tesseractOnce(tess, imagePath, lang, "6")
	if err2 == nil && len(moneyAmount.FindAllString(block, -1)) > len(moneyAmount.FindAllString(normal, -1)) {
		return block, stderr, nil
	}
	return normal, stderr, nil
}

func tesseractOnce(tess, imagePath, lang, psm string) (string, string, error) {
	tmp, err := os.CreateTemp("", "terminus-ocr-*")
	if err != nil {
		return "", "", err
	}
	tmp.Close()
	outBase := tmp.Name()
	defer os.Remove(outBase)
	defer os.Remove(outBase + ".txt")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	args := []string{imagePath, outBase, "-l", lang}
	if psm != "" {
		args = append(args, "--psm", psm)
	}
	cmd := exec.CommandContext(ctx, tess, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", stderr.String(), err
	}
	data, err := os.ReadFile(outBase + ".txt")
	if err != nil {
		return "", stderr.String(), err
	}
	return string(data), stderr.String(), nil
}

func runTesseract(path, lang, toolName string) ToolResult {
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		return ocrPDFPages(path, lang, toolName)
	}

	tess, err := findTesseractExecutable()
	if err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: "Tesseract is not installed."}
	}
	data, stderr, err := ocrBestReading(tess, path, lang)
	if err != nil {
		return ToolResult{
			ToolName: toolName,
			Success:  false,
			Error:    fmt.Sprintf("tesseract failed: %v. stderr: %s", err, truncateStr(stderr, 300)),
		}
	}

	text := strings.TrimSpace(data)
	if text == "" {
		return ToolResult{ToolName: toolName, Success: true, Output: "(OCR returned no text — image may be too low resolution or not contain recognizable text)"}
	}
	if len(text) > maxOutputChars {
		text = cutUTF8(text, maxOutputChars) + "\n\n... [truncated]"
	}
	return ToolResult{ToolName: toolName, Success: true, Output: fmt.Sprintf("[OCR lang=%s]\n%s", lang, text)}
}

// ═══════════════════════════════════════════════════════════════════════════
// docx_extract — Pure Go .docx text extraction
// ═══════════════════════════════════════════════════════════════════════════

// docxParagraph and docxText mirror the OOXML structure we care about.
// A .docx is just a zip. word/document.xml contains <w:p> paragraphs, each
// holding <w:r> runs, each holding <w:t> text nodes. We ignore formatting,
// drawings, and headers — clinicians just want the words.
type docxDocument struct {
	XMLName xml.Name `xml:"document"`
	Body    struct {
		Paragraphs []struct {
			Runs []struct {
				Texts []struct {
					Value string `xml:",chardata"`
				} `xml:"t"`
			} `xml:"r"`
		} `xml:"p"`
	} `xml:"body"`
}

// docxPlainText reads a Word document's body in order and keeps its tables:
// each table row becomes one line with a tab between cells, so a table of
// figures survives as rows and columns instead of vanishing. The older reader
// only looked at paragraphs directly under the body and dropped every table.
func docxPlainText(raw []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var sb strings.Builder
	cellDepth := 0 // inside a table cell
	cellHasText := false
	inText := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				sb.WriteString("\t")
			case "br":
				if cellDepth > 0 {
					sb.WriteString(" ")
				} else {
					sb.WriteString("\n")
				}
			case "tc":
				cellDepth++
				cellHasText = false
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if cellDepth > 0 {
					// several paragraphs in one cell stay on one line
					if cellHasText {
						sb.WriteString(" ")
					}
				} else {
					sb.WriteString("\n")
				}
			case "tc":
				cellDepth--
				if cellDepth == 0 {
					sb.WriteString("\t")
				}
			case "tr":
				sb.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				sb.Write(t)
				if cellDepth > 0 {
					cellHasText = true
				}
			}
		}
	}
	// Tidy: no space or tab left hanging at the end of a line.
	lines := strings.Split(sb.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(strings.ReplaceAll(line, " \t", "\t"), " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func toolDOCXExtract(args map[string]any) (result ToolResult) {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{ToolName: "docx_extract", Success: false, Error: "path is required"}
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	if _, err := os.Stat(path); err != nil {
		return ToolResult{ToolName: "docx_extract", Success: false, Error: fmt.Sprintf("file not found: %s", path)}
	}

	defer func() {
		if r := recover(); r != nil {
			result = ToolResult{
				ToolName: "docx_extract",
				Success:  false,
				Error:    fmt.Sprintf("docx parser crashed: %v — file may be corrupted or password-protected", r),
			}
		}
	}()

	zr, err := zip.OpenReader(path)
	if err != nil {
		return ToolResult{ToolName: "docx_extract", Success: false, Error: fmt.Sprintf("not a valid .docx (zip) file: %v", err)}
	}
	defer zr.Close()

	// Find word/document.xml inside the zip.
	var docXML *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docXML = f
			break
		}
	}
	if docXML == nil {
		return ToolResult{ToolName: "docx_extract", Success: false, Error: "word/document.xml not found — file is not a standard .docx"}
	}

	rc, err := docXML.Open()
	if err != nil {
		return ToolResult{ToolName: "docx_extract", Success: false, Error: fmt.Sprintf("open document.xml: %v", err)}
	}
	defer rc.Close()

	raw, err := io.ReadAll(rc)
	if err != nil {
		return ToolResult{ToolName: "docx_extract", Success: false, Error: fmt.Sprintf("read document.xml: %v", err)}
	}

	text := docxPlainText(raw)
	if text == "" {
		return ToolResult{ToolName: "docx_extract", Success: true, Output: "(docx contained no text — possibly only images or tables)"}
	}
	if len(text) > maxOutputChars {
		text = cutUTF8(text, maxOutputChars) + "\n\n... [truncated]"
	}
	return ToolResult{ToolName: "docx_extract", Success: true, Output: text}
}

// ═══════════════════════════════════════════════════════════════════════════
// render_pdf — Generate a PDF from text/markdown
// ═══════════════════════════════════════════════════════════════════════════

// Minimal Markdown handling: we recognize #/##/### headings and bullet lists.
// Anything fancier is treated as plain text. This is intentional — the AI
// owns formatting, we own rendering.
var (
	headingRE = regexp.MustCompile(`^(#{1,3})\s+(.+)$`)
	bulletRE  = regexp.MustCompile(`^[-*]\s+(.+)$`)
	dividerRE = regexp.MustCompile(`^[-*_]{3,}\s*$`)
)

func toolRenderPDF(args map[string]any) ToolResult {
	content, _ := args["content"].(string)
	if content == "" {
		return ToolResult{ToolName: "render_pdf", Success: false, Error: "content is required"}
	}
	title, _ := args["title"].(string)
	baseName, _ := args["filename"].(string)
	if baseName == "" {
		baseName = "document"
	}
	baseName = sanitizeOutputName(baseName)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return ToolResult{ToolName: "render_pdf", Success: false, Error: fmt.Sprintf("mkdir: %v", err)}
	}

	pdfDoc := fpdf.New("P", "mm", "Letter", "")
	pdfDoc.SetMargins(20, 20, 20)
	pdfDoc.SetAutoPageBreak(true, 20)
	pdfDoc.AddPage()

	if title != "" {
		pdfDoc.SetFont("Helvetica", "B", 18)
		pdfDoc.MultiCell(0, 9, title, "", "L", false)
		pdfDoc.Ln(2)
		pdfDoc.SetDrawColor(180, 180, 180)
		pdfDoc.Line(20, pdfDoc.GetY(), 195, pdfDoc.GetY())
		pdfDoc.Ln(5)
	}

	var tableBuffer []string

	flushTable := func() {
		if len(tableBuffer) > 0 {
			renderTable(pdfDoc, tableBuffer)
			tableBuffer = nil
		}
	}

	pdfDoc.SetFont("Helvetica", "", 11)
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)

		// Table row detection
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			tableBuffer = append(tableBuffer, line)
			continue
		}

		// Flush any buffered table before rendering other elements
		flushTable()

		if trimmed == "" {
			pdfDoc.Ln(3)
			continue
		}
		if dividerRE.MatchString(line) {
			pdfDoc.Ln(2)
			pdfDoc.SetDrawColor(180, 180, 180)
			pdfDoc.Line(20, pdfDoc.GetY(), 195, pdfDoc.GetY())
			pdfDoc.Ln(3)
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			size := map[int]float64{1: 15, 2: 13, 3: 12}[level]
			pdfDoc.Ln(2)
			pdfDoc.SetFont("Helvetica", "B", size)
			pdfDoc.MultiCell(0, 7, m[2], "", "L", false)
			pdfDoc.SetFont("Helvetica", "", 11)
			continue
		}
		if m := bulletRE.FindStringSubmatch(line); m != nil {
			pdfDoc.SetFont("Helvetica", "", 11)
			pdfDoc.Write(6.0, "  • ")
			writeFormattedText(pdfDoc, m[1], 6.0)
			pdfDoc.Ln(6.0)
			continue
		}
		writeFormattedText(pdfDoc, line, 6.0)
		pdfDoc.Ln(6.0)
	}

	flushTable()

	stamp := time.Now().UTC().Format("20060102-150405")
	outPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s.pdf", stamp, baseName))
	if err := pdfDoc.OutputFileAndClose(outPath); err != nil {
		return ToolResult{ToolName: "render_pdf", Success: false, Error: fmt.Sprintf("write pdf: %v", err)}
	}
	return ToolResult{
		ToolName: "render_pdf",
		Success:  true,
		Output:   fmt.Sprintf("PDF created at %s", filepath.ToSlash(outPath)),
	}
}

func writeFormattedText(pdfDoc *fpdf.Fpdf, text string, height float64) {
	parts := strings.Split(text, "**")
	isBold := false
	for _, part := range parts {
		if part == "" {
			isBold = !isBold
			continue
		}
		if isBold {
			pdfDoc.SetFont("Helvetica", "B", 11)
			pdfDoc.Write(height, part)
			pdfDoc.SetFont("Helvetica", "", 11)
		} else {
			subParts := strings.Split(part, "__")
			subBold := false
			for _, subPart := range subParts {
				if subPart == "" {
					subBold = !subBold
					continue
				}
				if subBold {
					pdfDoc.SetFont("Helvetica", "B", 11)
					pdfDoc.Write(height, subPart)
					pdfDoc.SetFont("Helvetica", "", 11)
				} else {
					pdfDoc.Write(height, subPart)
				}
				subBold = !subBold
			}
		}
		isBold = !isBold
	}
}

func renderTable(pdfDoc *fpdf.Fpdf, rows []string) {
	var matrix [][]string
	for _, r := range rows {
		trimmed := strings.TrimSpace(r)
		isSep := true
		for _, c := range trimmed {
			if c != '|' && c != '-' && c != ':' && c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				isSep = false
				break
			}
		}
		if isSep {
			continue
		}

		parts := strings.Split(trimmed, "|")
		if len(parts) > 2 {
			var cells []string
			for i := 1; i < len(parts)-1; i++ {
				val := strings.TrimSpace(parts[i])
				val = strings.ReplaceAll(val, "**", "")
				val = strings.ReplaceAll(val, "__", "")
				cells = append(cells, val)
			}
			matrix = append(matrix, cells)
		}
	}

	if len(matrix) == 0 {
		return
	}

	maxCols := 0
	for _, cells := range matrix {
		if len(cells) > maxCols {
			maxCols = len(cells)
		}
	}
	if maxCols == 0 {
		return
	}

	const totalWidth = 175.0
	colWidths := make([]float64, maxCols)
	for col := 0; col < maxCols; col++ {
		maxLen := 0
		for _, row := range matrix {
			if col < len(row) {
				if len(row[col]) > maxLen {
					maxLen = len(row[col])
				}
			}
		}
		if maxLen < 5 {
			maxLen = 5
		}
		colWidths[col] = float64(maxLen)
	}

	sum := 0.0
	for _, w := range colWidths {
		sum += w
	}
	for i := range colWidths {
		colWidths[i] = (colWidths[i] / sum) * totalWidth
	}

	pdfDoc.SetFont("Helvetica", "B", 10)
	pdfDoc.SetFillColor(240, 240, 240)
	pdfDoc.SetDrawColor(180, 180, 180)

	header := matrix[0]
	h := 7.0
	for col, val := range header {
		pdfDoc.CellFormat(colWidths[col], h, val, "1", 0, "L", true, 0, "")
	}
	pdfDoc.Ln(h)

	pdfDoc.SetFont("Helvetica", "", 10)
	for i := 1; i < len(matrix); i++ {
		row := matrix[i]
		for col := 0; col < maxCols; col++ {
			val := ""
			if col < len(row) {
				val = row[col]
			}
			pdfDoc.CellFormat(colWidths[col], 6.0, val, "1", 0, "L", false, 0, "")
		}
		pdfDoc.Ln(6.0)
	}
	pdfDoc.Ln(3.0)
}

// ═══════════════════════════════════════════════════════════════════════════
// render_docx — Generate a minimal .docx from text
// ═══════════════════════════════════════════════════════════════════════════

// A valid .docx is a zip with three required entries:
//   [Content_Types].xml
//   _rels/.rels
//   word/document.xml
// We ship the minimum skeleton needed to make Word / LibreOffice happy.

const docxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const docxRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

func toolRenderDOCX(args map[string]any) ToolResult {
	content, _ := args["content"].(string)
	if content == "" {
		return ToolResult{ToolName: "render_docx", Success: false, Error: "content is required"}
	}
	title, _ := args["title"].(string)
	baseName, _ := args["filename"].(string)
	if baseName == "" {
		baseName = "document"
	}
	baseName = sanitizeOutputName(baseName)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return ToolResult{ToolName: "render_docx", Success: false, Error: fmt.Sprintf("mkdir: %v", err)}
	}

	// Build word/document.xml by turning each input line into a <w:p>.
	// Headings (#, ##, ###) become bold paragraphs at larger sizes.
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	body.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)

	if title != "" {
		body.WriteString(docxParagraph(title, 32, true))
	}
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			body.WriteString(`<w:p/>`)
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			size := map[int]int{1: 30, 2: 26, 3: 22}[len(m[1])]
			body.WriteString(docxParagraph(m[2], size, true))
			continue
		}
		if m := bulletRE.FindStringSubmatch(line); m != nil {
			body.WriteString(docxParagraph("\u2022 "+m[1], 22, false))
			continue
		}
		body.WriteString(docxParagraph(line, 22, false))
	}
	// sectPr is required at end of body for Word to be happy
	body.WriteString(`<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>`)
	body.WriteString(`</w:body></w:document>`)

	stamp := time.Now().UTC().Format("20060102-150405")
	outPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s.docx", stamp, baseName))

	if err := writeDocxZip(outPath, body.String()); err != nil {
		return ToolResult{ToolName: "render_docx", Success: false, Error: fmt.Sprintf("write docx: %v", err)}
	}
	return ToolResult{
		ToolName: "render_docx",
		Success:  true,
		Output:   fmt.Sprintf("DOCX created at %s", filepath.ToSlash(outPath)),
	}
}

// docxParagraph wraps text in the minimum OOXML needed for a visible paragraph.
// size is in half-points (22 = 11pt, 32 = 16pt). bold adds <w:b/>.
func docxParagraph(text string, size int, bold bool) string {
	var props strings.Builder
	if bold {
		props.WriteString(`<w:b/>`)
	}
	props.WriteString(fmt.Sprintf(`<w:sz w:val="%d"/>`, size))
	props.WriteString(fmt.Sprintf(`<w:szCs w:val="%d"/>`, size))
	escaped := xmlEscape(text)
	return fmt.Sprintf(
		`<w:p><w:r><w:rPr>%s</w:rPr><w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
		props.String(), escaped,
	)
}

func writeDocxZip(outPath, documentXML string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	entries := []struct{ name, body string }{
		{"[Content_Types].xml", docxContentTypes},
		{"_rels/.rels", docxRels},
		{"word/document.xml", documentXML},
	}
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			return err
		}
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════
// helpers
// ═══════════════════════════════════════════════════════════════════════════

func sanitizeOutputName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
		case r < 32 || r == 127:
			// skip control characters
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "document"
	}
	return out
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ═══════════════════════════════════════════════════════════════════════════
// extract_medical_entities — Stage 0-4 layered document pipeline
// ═══════════════════════════════════════════════════════════════════════════

// toolExtractMedicalEntities orchestrates the full Stage 0-4 pipeline:
//
//	Stage 0 — raw text extraction (auto_ingest / OCR)
//	Stage 1 — multi-hypothesis candidate generation (docpipeline)
//	Stage 2 — consistency verification (docpipeline)
//	Stage 3 — deterministic resolution (docpipeline)
//	Stage 4 — structured JSON output (this function)
//
// The model never touches raw text or decides which candidate wins.
// All decisions are deterministic — only ambiguous fields surface for review.
func (r *Registry) toolExtractMedicalEntities(args map[string]any) ToolResult {
	const toolName = "extract_medical_entities"

	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{ToolName: toolName, Success: false, Error: "path is required"}
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}

	includeAmbiguous := false
	if v, _ := args["include_ambiguous"].(string); strings.ToLower(v) == "true" {
		includeAmbiguous = true
	}

	// ── Stage 0: extract raw text ──────────────────────────────────────────
	rawText, stage0Method, _, err := extractRawText(path)
	if err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: fmt.Sprintf("stage0 text extraction: %v", err)}
	}
	if strings.TrimSpace(rawText) == "" {
		return ToolResult{ToolName: toolName, Success: false, Error: "stage0: no text extracted from document"}
	}

	// ── Stage 1-3: run docpipeline ─────────────────────────────────────────
	pr, vr := docpipeline.RunWithEngine(context.Background(), r.Engine, rawText, r.Engine != nil, "medical")

	// ── Stage 4: emit structured JSON ─────────────────────────────────────
	type output struct {
		Stage0Method  string                         `json:"stage0_method"`
		Status        docpipeline.VerificationStatus `json:"verification_status"`
		Conflicts     []string                       `json:"conflicts,omitempty"`
		Entities      []docpipeline.ResolvedEntity   `json:"entities"`
		AmbiguousKeys []string                       `json:"ambiguous_keys,omitempty"`
		FieldMap      map[string]string              `json:"field_map"`
		Summary       string                         `json:"summary"`
	}

	result := output{
		Stage0Method:  stage0Method,
		Status:        vr.Status,
		Conflicts:     vr.Conflicts,
		Entities:      pr.Entities,
		AmbiguousKeys: pr.AmbiguousKeys,
		FieldMap:      pr.ToFieldMap(includeAmbiguous),
		Summary:       pr.Summary,
	}

	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: fmt.Sprintf("marshal: %v", err)}
	}

	return ToolResult{
		ToolName: toolName,
		Success:  true,
		Output:   string(b),
	}
}

// extractRawText is Stage 0: dispatch to the correct extractor based on file
// extension and magic bytes, returning (text, method, error).
func extractRawText(path string) (string, string, []string, error) {
	ext := strings.ToLower(filepath.Ext(path))

	// Read file header for magic-byte detection
	f, err := os.Open(path)
	if err != nil {
		return "", "", nil, fmt.Errorf("open: %w", err)
	}
	header := make([]byte, 8)
	n, _ := f.Read(header)
	f.Close()
	header = header[:n]

	switch {
	case ext == ".pdf" || (len(header) >= 4 && string(header[:4]) == "%PDF"):
		// Try native PDF extraction first; fall through to OCR if empty
		result := toolPDFExtract(map[string]any{"path": path})
		if result.Success && strings.TrimSpace(result.Output) != "" &&
			!strings.HasPrefix(strings.TrimSpace(result.Output), "(no text found") {
			body, warns := splitExtractWarnings(result.Output)
			body, more := CleanExtractionBody(body)
			warns = append(warns, more...)
			return body, "pdf_extract", dedupeStrings(warns), nil
		}
		if !result.Success && strings.Contains(result.Error, "is locked") {
			// OCR can't open a locked file either; keep the plain message.
			return "", "pdf_extract", nil, errors.New(result.Error)
		}
		// Scanned PDF — attempt OCR
		ocrResult := toolOCRExtract(map[string]any{"path": path, "language": "eng"})
		if ocrResult.Success {
			body, warns := splitExtractWarnings(ocrResult.Output)
			body, more := CleanExtractionBody(body)
			warns = append(warns, more...)
			return body, "ocr_extract(pdf)", dedupeStrings(warns), nil
		}
		return "", "", nil, fmt.Errorf("PDF text extraction failed: %s", ocrResult.Error)

	case ext == ".docx":
		result := toolDOCXExtract(map[string]any{"path": path})
		if !result.Success {
			return "", "", nil, fmt.Errorf("docx_extract: %s", result.Error)
		}
		return result.Output, "docx_extract", nil, nil

	case ext == ".png" || ext == ".jpg" || ext == ".jpeg" ||
		ext == ".tiff" || ext == ".tif" || ext == ".bmp":
		if text, err := extractImageWithLMStudio(path); err == nil && text != "" {
			return text, "lm_studio_vision", nil, nil
		}
		result := toolImageExtract(map[string]any{"path": path, "language": "eng"})
		if !result.Success {
			return "", "", nil, fmt.Errorf("image_extract: %s", result.Error)
		}
		return result.Output, "image_extract", nil, nil

	case ext == ".txt" || ext == ".md" || ext == ".csv" || ext == ".json":
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", nil, fmt.Errorf("read text file: %w", err)
		}
		return string(data), "read_file", nil, nil

	default:
		// Generic text read as fallback
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", nil, fmt.Errorf("unknown file type and read failed: %w", err)
		}
		return string(data), "read_file(fallback)", nil, nil
	}
}

func (r *Registry) toolGenerateTool(args map[string]any) ToolResult {
	const toolName = "generate_tool"

	description := firstStringArg(args, "description")
	if description == "" {
		return ToolResult{ToolName: toolName, Success: false, Error: "description is required"}
	}
	suggestedName := firstStringArg(args, "tool_name", "name")

	if r.Engine == nil {
		return ToolResult{
			ToolName: toolName,
			Success:  false,
			Error:    "AIEngine is not configured. Please wire or enable a model runner backend to generate tools.",
		}
	}

	prompt := fmt.Sprintf(`You are an expert developer building a Terminus community tool definition.
Terminus community tools use the 7-box schema.
Every community tool is a JSON object with exactly the following fields:
{
  "name": "lowercase_snake_case_name",
  "description": "Clear explanation of what the tool does",
  "author": "community-ai",
  "version": "1.0",
  "tags": ["tag1", "tag2"],
  "parameters": {
    "param_name1": "Description of the parameter 1",
    "param_name2": "Description of the parameter 2"
  },
  "status_broadcast": "status-broadcast-tag",
  "entity_selector": "{{param_name1}}",
  "primary_operation": "shell:echo {{param_name1}} {{param_name2}}",
  "step_latency": 0,
  "remediation_logic": "fail",
  "audit_receipt": ""
}

The primary_operation must be prefixed with:
- "shell:" to run a shell command (e.g. "shell:python my_script.py --arg {{arg_name}}")
- "http:GET:" or "http:POST:" to make an HTTP request (e.g. "http:GET:https://api.example.com/status")
- "tool:" to call another tool in the registry (e.g. "tool:read_file:{\"path\":\"{{path}}\"}")
- "tool:builtin:" to run a built-in tool bypass (e.g. "tool:builtin:read_file:{\"path\":\"{{path}}\"}")
- "heal:" to run self-healing on a target file

Use {{param_name}} placeholders in entity_selector and primary_operation. Every parameter defined in "parameters" MUST be used as a placeholder in the command or selector.

User request: "%s"
Suggested tool name: "%s"

Generate ONLY the valid JSON object. No explanations, no markdown code block fences (like triple-backtick json). Just the raw JSON.`, description, suggestedName)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := r.Engine.Predict(ctx, prompt)
	if err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: fmt.Sprintf("AI engine failure: %v", err)}
	}

	resp = strings.TrimSpace(resp)
	// Strip markdown block markers if the model included them anyway.
	if strings.HasPrefix(resp, "```") {
		lines := strings.Split(resp, "\n")
		var cleanLines []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				continue
			}
			cleanLines = append(cleanLines, line)
		}
		resp = strings.Join(cleanLines, "\n")
	}
	resp = strings.TrimSpace(resp)

	// Validate JSON structure by unmarshaling
	var testDef CommunityToolDef
	if err := json.Unmarshal([]byte(resp), &testDef); err != nil {
		return ToolResult{
			ToolName: toolName,
			Success:  false,
			Error:    fmt.Sprintf("generated invalid JSON: %v\nOutput was:\n%s", err, resp),
		}
	}

	if testDef.Name == "" {
		if suggestedName != "" {
			testDef.Name = suggestedName
		} else {
			testDef.Name = "generated_tool_" + fmt.Sprintf("%d", time.Now().Unix())
		}
	}
	// Sanitize name
	testDef.Name = strings.ReplaceAll(strings.ToLower(testDef.Name), " ", "_")

	// Ensure folder exists and write file
	protoDir := "internal/protocols"
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: fmt.Sprintf("mkdir %s: %v", protoDir, err)}
	}

	filePath := filepath.Join(protoDir, testDef.Name+".json")
	prettyJSON, err := json.MarshalIndent(testDef, "", "  ")
	if err != nil {
		prettyJSON = []byte(resp)
	}

	if err := os.WriteFile(filePath, prettyJSON, 0644); err != nil {
		return ToolResult{ToolName: toolName, Success: false, Error: fmt.Sprintf("write file: %v", err)}
	}

	return ToolResult{
		ToolName: toolName,
		Success:  true,
		Output:   fmt.Sprintf("Successfully generated and saved tool '%s' to '%s'. It will be auto-ingested shortly.\n\nDefinition:\n%s", testDef.Name, filePath, string(prettyJSON)),
	}
}

func extractImageWithLMStudio(imgPath string) (string, error) {
	imgData, err := os.ReadFile(imgPath)
	if err != nil {
		return "", fmt.Errorf("read image file: %w", err)
	}

	var mimeType string
	ext := strings.ToLower(filepath.Ext(imgPath))
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".gif":
		mimeType = "image/gif"
	case ".bmp":
		mimeType = "image/bmp"
	default:
		mimeType = "image/jpeg"
	}

	base64Data := base64.StdEncoding.EncodeToString(imgData)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data)

	type textContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type imageURLInfo struct {
		URL string `json:"url"`
	}
	type imageContent struct {
		Type     string       `json:"type"`
		ImageURL imageURLInfo `json:"image_url"`
	}
	type message struct {
		Role    string `json:"role"`
		Content []any  `json:"content"`
	}
	type request struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
	}

	reqPayload := request{
		Model: "phi-4-reasoning-vision",
		Messages: []message{
			{
				Role: "user",
				Content: []any{
					textContent{
						Type: "text",
						Text: "Extract all clinical text, patient names, vitals, tables, handwriting, and checkboxes from this medical document image. Output ONLY clean Markdown containing the extracted text. Do not include introductory notes or chat commentary.",
					},
					imageContent{
						Type: "image_url",
						ImageURL: imageURLInfo{
							URL: dataURL,
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
	defer cancel()

	url := "http://localhost:1234/v1/chat/completions"
	if envBase := strings.TrimSpace(os.Getenv("LM_STUDIO_BASE_URL")); envBase != "" {
		url = strings.TrimRight(envBase, "/") + "/chat/completions"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// 90s server timeout, 95s client deadline
	client := &http.Client{Timeout: 95 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("LM Studio call failed (is LM Studio running on port 1234?): %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LM Studio returned HTTP status %d: %s", resp.StatusCode, string(respBytes))
	}

	var oaiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return "", fmt.Errorf("parse JSON response: %w", err)
	}

	if len(oaiResp.Choices) == 0 {
		return "", fmt.Errorf("empty choices from LM Studio")
	}

	return oaiResp.Choices[0].Message.Content, nil
}
