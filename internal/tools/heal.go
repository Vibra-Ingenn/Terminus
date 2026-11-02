package tools

// heal.go — Universal self-healing document engine.
//
// HealDocument is the AI-in-the-middle layer: when a file is broken, missing
// data, or unreadable by normal means, this function reads everything it can,
// applies every applicable repair strategy in sequence, and returns a
// HealReport that describes:
//   - what issues were found
//   - what Terminus was able to fix automatically
//   - the recovered text (if any)
//   - specific user actions if full auto-repair was not possible
//
// The healing hierarchy for each file type:
//   PDF:   xref-offset repair → text-layer extraction → raw-byte salvage → OCR hint
//   DOCX:  zip+XML parser → raw XML text fallback
//   HL7:   full parse → segment-name identification → raw content
//   JSON/XML/Text: direct read (never fails if file is accessible)
//   Image: OCR hint (requires Tesseract)
//   Unknown: magic-byte report + guidance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HealReport is the structured outcome of a self-healing attempt.
type HealReport struct {
	FilePath      string   `json:"file_path"`
	FileType      string   `json:"file_type"`            // pdf, docx, hl7, text, image, unknown
	FileSizeBytes int64    `json:"file_size_bytes"`
	Issues        []string `json:"issues,omitempty"`     // problems detected
	AutoFixed     []string `json:"auto_fixed,omitempty"` // problems Terminus repaired automatically
	TextExtracted string   `json:"text_extracted,omitempty"` // recovered content
	OutputPath    string   `json:"output_path,omitempty"`    // path to repaired file if one was written
	Succeeded     bool     `json:"succeeded"`
	Strategy      string   `json:"strategy"` // which repair path was taken
	// UserActions lists the specific steps the user must take when auto-repair
	// was not possible or was only partial. Always check this field even on
	// Succeeded=true if you need a guaranteed-clean copy.
	UserActions []string `json:"user_actions,omitempty"`
	Summary     string   `json:"summary"`
	Error       string   `json:"error,omitempty"`
}

// HealDocument reads, diagnoses, and repairs any document.
// outDir is where repaired copies are written (defaults to workspace/outputs).
// This function never panics — it always returns a HealReport.
func HealDocument(path string, outDir string) (rep HealReport) {
	defer func() {
		if r := recover(); r != nil {
			rep.Issues = append(rep.Issues, fmt.Sprintf("internal panic: %v", r))
			rep.Succeeded = false
			rep.Summary = "heal_document crashed unexpectedly — see issues"
		}
	}()

	if outDir == "" {
		outDir = outputDir
	}

	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return HealReport{
			FilePath:  path,
			Succeeded: false,
			Strategy:  "pre-check",
			Issues:    []string{"file not found or inaccessible: " + err.Error()},
			UserActions: []string{
				"Verify the file path is correct",
				"Check that Terminus has read permission for this path",
				fmt.Sprintf("Expected file at: %s", path),
			},
			Summary: fmt.Sprintf("Cannot access file: %s", filepath.Base(path)),
			Error:   err.Error(),
		}
	}

	kind, _ := detectFileType(path)
	rep = HealReport{
		FilePath:      path,
		FileType:      kind,
		FileSizeBytes: info.Size(),
	}

	switch kind {
	case "pdf":
		healPDF(&rep, path, outDir)
	case "docx":
		healDOCX(&rep, path)
	case "hl7":
		healHL7(&rep, path)
	case "image":
		healImage(&rep, path)
	case "text":
		healText(&rep, path)
	default:
		healUnknown(&rep, path)
	}

	return rep
}

// healPDF runs PDF-specific recovery in escalating order of invasiveness.
func healPDF(rep *HealReport, path, outDir string) {
	diag, data, err := diagnosePDFFile(path)
	if err != nil {
		rep.Issues = []string{"cannot read file: " + err.Error()}
		rep.UserActions = []string{"Check file permissions and disk integrity"}
		rep.Summary = "File read error"
		rep.Error = err.Error()
		return
	}
	rep.Issues = diag.Issues

	// Strategy 1: normal parser (may succeed for clean PDFs or after xref fix).
	openPath, cleanup, f, r, openErr := openPDFWithRepair(path)
	if openErr == nil {
		if cleanup != nil {
			defer cleanup()
		}
		defer f.Close()

		if openPath != path {
			rep.AutoFixed = append(rep.AutoFixed, "repaired startxref offset")
			// Write the fixed file as a persistent artefact.
			if merr := os.MkdirAll(outDir, 0755); merr == nil {
				fixed, changed := repairPDFStartXRefBytes(data)
				if changed {
					outPath := buildHealOutputPath(outDir, path, ".pdf")
					if werr := os.WriteFile(outPath, fixed, 0644); werr == nil {
						rep.OutputPath = outPath
						rep.AutoFixed = append(rep.AutoFixed, "wrote repaired PDF to "+filepath.Base(outPath))
					}
				}
			}
		}

		text, pages, _ := extractPDFText(r)
		if text != "" {
			rep.TextExtracted = text
			rep.Succeeded = true
			if openPath != path {
				rep.Strategy = "pdf_xref_repair"
			} else {
				rep.Strategy = "pdf_parser"
			}
			rep.Summary = fmt.Sprintf("Extracted %d pages of text", pages)
			if len(diag.Issues) > 0 && rep.OutputPath == "" {
				rep.UserActions = []string{
					"The PDF had structural issues but text was still readable",
					"Re-export from the source application for a clean, archival-quality copy",
				}
			}
			return
		}
		// Parser opened but no text layer — fall through to byte salvage.
	}

	// Strategy 2: raw-byte text salvage (works on structurally broken PDFs).
	salvaged := salvagePDFTextFromBytes(data)
	if salvaged != "" {
		rep.TextExtracted = salvaged
		rep.Succeeded = true
		rep.Strategy = "byte_salvage"
		rep.AutoFixed = append(rep.AutoFixed, "salvaged text from raw PDF byte stream")
		rep.Summary = fmt.Sprintf("Recovered %d chars via raw salvage — may be incomplete or out of order", len(salvaged))
		rep.UserActions = []string{
			"Salvaged text may be incomplete or out of reading order",
			"Re-export from the source application for a clean copy",
		}
		return
	}

	// Strategy 3: all parser paths failed.
	rep.Succeeded = false
	rep.Strategy = "all_repair_failed"
	rep.Summary = "Could not recover text from this PDF"

	var actions []string
	if len(diag.Issues) > 0 {
		actions = append(actions, "Structural issues: "+strings.Join(diag.Issues, "; "))
	}
	if diag.TextCandidates == 0 {
		actions = append(actions, "No text layer found — this may be a scanned image PDF")
		actions = append(actions, "Run OCR: use the tool 'ocr_extract' with this path (requires Tesseract on PATH)")
		actions = append(actions, "Install Tesseract: https://github.com/UB-Mannheim/tesseract/wiki (Windows) or 'apt install tesseract-ocr' (Linux)")
	} else {
		actions = append(actions, "Re-export the PDF from the source application (File → Export as PDF or File → Print to PDF)")
		actions = append(actions, "If generated by code, ensure byte offsets are computed on final raw bytes, not Unicode code points")
	}
	rep.UserActions = actions
}

// healDOCX attempts to recover a Word document.
func healDOCX(rep *HealReport, path string) {
	res := toolDOCXExtract(map[string]any{"path": path})
	if res.Success {
		rep.Succeeded = true
		rep.Strategy = "docx_parser"
		rep.TextExtracted = res.Output
		rep.Summary = "Successfully extracted Word document text"
		return
	}

	// Parser failed — record the issue and give actionable guidance.
	rep.Issues = []string{res.Error}
	rep.Succeeded = false
	rep.Strategy = "docx_parser_failed"
	rep.Summary = "Could not read Word document"
	rep.UserActions = []string{
		"The .docx file may be corrupted or password-protected",
		"Try opening in Microsoft Word or LibreOffice — if it opens, do File → Save As to create a clean copy",
		"If password-protected, remove the password before processing",
		"Last resort: open in Word, select all text (Ctrl+A), and paste into a plain .txt file",
	}
}

// healHL7 attempts to recover an HL7 message file.
func healHL7(rep *HealReport, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		rep.Issues = []string{"cannot read: " + err.Error()}
		rep.UserActions = []string{"Check file permissions and disk integrity"}
		rep.Summary = "File read error"
		rep.Error = err.Error()
		return
	}

	res := toolHL7Parse(map[string]any{"message": string(data)})
	if res.Success {
		rep.Succeeded = true
		rep.Strategy = "hl7_parser"
		rep.TextExtracted = res.Output
		rep.Summary = "Successfully parsed HL7 message"
		return
	}

	// Full parse failed — identify what segments are present.
	segs := extractHL7SegmentNames(string(data))
	rep.Issues = []string{res.Error}

	if len(segs) > 0 {
		rep.AutoFixed = []string{"identified segments: " + strings.Join(segs, ", ")}
		rep.TextExtracted = string(data) // raw content is still usable
		rep.Succeeded = true
		rep.Strategy = "hl7_partial"
		rep.Summary = fmt.Sprintf("Partial HL7 parse — segments found: %s", strings.Join(segs, ", "))
		rep.UserActions = []string{
			"The HL7 message may have non-standard delimiters or missing required fields",
			"Verify MSH segment is present and the field delimiter is | (pipe character)",
			"Check line endings — HL7 v2 uses carriage-return (\\r) as segment separator",
		}
		return
	}

	rep.Succeeded = false
	rep.Strategy = "hl7_unrecognized"
	rep.Summary = "Could not parse HL7 message — no valid segments found"
	rep.UserActions = []string{
		"Verify this is a valid HL7 v2.x message (starts with MSH|)",
		"Check for BOM or encoding issues — the file must be plain ASCII or UTF-8",
		"If received from an external system, ask them to resend with explicit HL7 v2.5 encoding",
	}
}

// healImage gives OCR guidance for image files (no auto-run to avoid surprise).
func healImage(rep *HealReport, path string) {
	_ = path
	rep.Succeeded = false
	rep.Strategy = "image_ocr_required"
	rep.Summary = "Image detected — OCR needed to extract text"
	rep.Issues = []string{"image files do not have an embedded text layer"}
	rep.UserActions = []string{
		"Use tool 'ocr_extract' with this path to run Tesseract OCR on the image",
		"Tesseract must be installed: https://github.com/UB-Mannheim/tesseract/wiki",
		"If the image is low-resolution or handwritten, OCR quality will be limited",
	}
}

// healText reads plain text files — always succeeds if the file is accessible.
func healText(rep *HealReport, path string) {
	data, err := readCapped(path, 128*1024)
	if err != nil {
		rep.Issues = []string{"read error: " + err.Error()}
		rep.UserActions = []string{"Check file permissions and disk integrity"}
		rep.Summary = "File read error"
		rep.Error = err.Error()
		return
	}
	rep.Succeeded = true
	rep.Strategy = "direct_read"
	rep.TextExtracted = strings.TrimSpace(string(data))
	rep.Summary = fmt.Sprintf("Read %d bytes of plain text", len(data))
}

// healUnknown handles files of unrecognized type with helpful diagnostics.
func healUnknown(rep *HealReport, path string) {
	data, readErr := os.ReadFile(path)
	head := data
	if len(head) > 32 {
		head = head[:32]
	}
	var hexParts []string
	for _, b := range head {
		hexParts = append(hexParts, fmt.Sprintf("%02x", b))
	}
	magic := strings.Join(hexParts, " ")

	ext := strings.ToLower(filepath.Ext(path))
	rep.Issues = []string{
		fmt.Sprintf("unrecognized file type (extension=%q magic=%s)", ext, magic),
	}

	// If the file is actually mostly readable text, salvage it anyway.
	if readErr == nil && isProbablyText(data) {
		rep.Succeeded = true
		rep.Strategy = "text_fallback"
		rep.AutoFixed = []string{"file classified as text despite unknown extension"}
		text := strings.TrimSpace(string(data))
		if len(text) > maxOutputChars {
			text = cutUTF8(text, maxOutputChars) + "\n... [truncated]"
		}
		rep.TextExtracted = text
		rep.Summary = fmt.Sprintf("Read as plain text (%d bytes)", len(data))
		return
	}

	rep.Succeeded = false
	rep.Strategy = "type_detection_failed"
	rep.Summary = "File type not recognized — cannot auto-heal"
	rep.UserActions = []string{
		"Verify the file extension matches its actual format",
		"Supported: PDF (.pdf), Word (.docx), HL7 (.hl7 or .txt), images (.png/.jpg/.tiff/.bmp), plain text (.txt/.csv/.json/.xml/.md)",
		fmt.Sprintf("Magic bytes: %s", magic),
		"If this is an image scan, try tool 'ocr_extract' to run OCR",
		"If this is an encrypted or signed file, decrypt it before processing",
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// extractHL7SegmentNames returns 3-letter segment IDs present in an HL7 message.
func extractHL7SegmentNames(msg string) []string {
	seen := make(map[string]bool)
	var segs []string
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) >= 3 {
			seg := line[:3]
			if isHL7SegmentID(seg) && !seen[seg] {
				seen[seg] = true
				segs = append(segs, seg)
			}
		}
	}
	return segs
}

func isHL7SegmentID(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func buildHealOutputPath(dir, inputPath, ext string) string {
	base := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	base = sanitizeOutputName(base + "-healed")
	return filepath.Join(dir, base+ext)
}

// ── tool registration ─────────────────────────────────────────────────────────

func toolHealDocument(args map[string]any) ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{ToolName: "heal_document", Success: false, Error: "path is required"}
	}
	outDir, _ := args["output_dir"].(string)
	report := HealDocument(path, outDir)
	out, _ := json.MarshalIndent(report, "", "  ")
	return ToolResult{
		ToolName: "heal_document",
		Success:  report.Succeeded,
		Output:   string(out),
		Error:    report.Error,
	}
}
