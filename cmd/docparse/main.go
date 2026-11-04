// docparse — Terminus document reader (CLI). Wraps internal/tools for scripts and the local screen.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"terminus/internal/tools"
)

const usageText = `Terminus — reads receipts, invoices and documents, and pulls out the fields. No AI needed.

Commands:
  ui      Open the simple screen in your browser (same as serve, then opens the page).
  parse   Structured fields as JSON. PDF, DOCX, images, text.
  heal    Diagnose and repair broken files; writes a healed copy when possible.
  text    Plain extracted text on stdout (good for piping).
  batch   Process every file in a directory; writes one JSON file per input.
  check   Parse every PDF in a folder (regression test); reports clean / warnings / failed.
  serve   Local web screen at http://127.0.0.1:<port>/ plus POST /v1/parse, /v1/heal.

Examples:
  docparse ui
  docparse parse ./jobs/upload.pdf --raw
  docparse parse ./jobs/discharge.pdf --pack medical
  docparse heal ./broken.pdf --out-dir ./workspace/outputs
  docparse text invoice.docx > invoice.txt
  docparse batch --in ./workspace/uploads --out ./workspace/outputs/parsed
  (On Windows, write paths the Windows way: "C:\jobs\upload.pdf")
  docparse serve --port 8765
  docparse parse -q file.pdf          # quiet: JSON only on stdout

Options:
  --pack <name>      Optional field pack (e.g. "medical" for patient name, MRN, dates of care)

Environment:
  TERMINUS_WORKSPACE Workspace root (default: ./workspace relative to cwd). JANUS_WORKSPACE still works.
  TERMINUS_PACKS     Optional comma-separated packs for serve/parse (e.g. "medical")
  TESSDATA_PREFIX    Optional Tesseract data path for OCR on scans
                     (Linux example: /usr/share/tesseract-ocr/5/tessdata)
`

func main() {
	log.SetFlags(0)
	if quietLogs(os.Args) {
		log.SetOutput(io.Discard)
	}
	if len(os.Args) < 2 {
		// Double-clicked (no command): open the screen. "docparse help" lists the commands.
		fmt.Fprintln(os.Stderr, "Opening Terminus in your browser. Close this window to stop it. (Commands: docparse help)")
		os.Exit(runUI(nil))
	}

	cmd := strings.ToLower(os.Args[1])
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return
	case "parse":
		os.Exit(runParse(tidyArgs(os.Args[2:])))
	case "heal":
		os.Exit(runHeal(tidyArgs(os.Args[2:])))
	case "text":
		os.Exit(runText(tidyArgs(os.Args[2:])))
	case "batch":
		os.Exit(runBatch(tidyArgs(os.Args[2:])))
	case "serve":
		os.Exit(runServe(tidyArgs(os.Args[2:])))
	case "ui":
		os.Exit(runUI(tidyArgs(os.Args[2:])))
	case "check":
		os.Exit(runCheck(tidyArgs(os.Args[2:])))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usageText)
		os.Exit(2)
	}
}

func runParse(args []string) int {
	fs := flag.NewFlagSet("parse", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var outFile string
	var includeRaw bool
	var autoHeal bool
	var packName string
	fs.StringVar(&outFile, "o", "", "write JSON to file (default stdout)")
	fs.BoolVar(&includeRaw, "raw", false, "include full raw_text in JSON")
	fs.BoolVar(&autoHeal, "auto-heal", false, "if parse status is not ok, run heal then parse again")
	fs.StringVar(&packName, "pack", "", "field pack to enable (e.g. medical)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := fs.Arg(0)
	if path == "" {
		fmt.Fprintln(os.Stderr, "parse: path argument required")
		return 2
	}

	body, ok := parseDocument(path, includeRaw, autoHeal, packName)
	if body == nil {
		return 1
	}
	if outFile != "" {
		if err := os.WriteFile(outFile, body, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "parse: write output: %v\n", err)
			return 1
		}
	} else {
		_, _ = os.Stdout.Write(body)
	}
	if !ok {
		return 1
	}
	return 0
}

func parseDocument(path string, includeRaw bool, autoHeal bool, packs ...string) ([]byte, bool) {
	reg := tools.NewRegistry()
	pack := ""
	if len(packs) > 0 && packs[0] != "" {
		pack = packs[0]
	} else {
		pack = os.Getenv("TERMINUS_PACKS")
	}
	res := reg.Dispatch(tools.ToolCall{
		Name: "document_parse",
		Arguments: map[string]any{
			"path":        path,
			"include_raw": boolString(includeRaw),
			"pack":        pack,
		},
	})
	if !res.Success {
		fmt.Fprintf(os.Stderr, "parse failed: %s\n", res.Error)
		return nil, false
	}

	if autoHeal && !parseStatusOK(res.Output) {
		healReport := tools.HealDocument(path, defaultOutputDir())
		if healReport.Succeeded && healReport.OutputPath != "" {
			res = reg.Dispatch(tools.ToolCall{
				Name: "document_parse",
				Arguments: map[string]any{
					"path":        healReport.OutputPath,
					"include_raw": boolString(includeRaw),
					"pack":        pack,
				},
			})
		}
	}

	if !res.Success {
		fmt.Fprintf(os.Stderr, "parse failed: %s\n", res.Error)
		return nil, false
	}
	return []byte(res.Output), parseStatusOK(res.Output)
}

func parseStatusOK(jsonOut string) bool {
	var stub struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &stub); err != nil {
		return false
	}
	return stub.Status == "ok"
}

func runHeal(args []string) int {
	fs := flag.NewFlagSet("heal", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outDir := defaultOutputDir()
	fs.StringVar(&outDir, "out-dir", outDir, "directory for repaired file copy")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := fs.Arg(0)
	if path == "" {
		fmt.Fprintln(os.Stderr, "heal: path argument required")
		return 2
	}

	report := tools.HealDocument(path, outDir)
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "heal: marshal: %v\n", err)
		return 1
	}
	_, _ = os.Stdout.Write(body)
	_, _ = os.Stdout.Write([]byte("\n"))
	if !report.Succeeded {
		return 1
	}
	return 0
}

func runText(args []string) int {
	fs := flag.NewFlagSet("text", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := fs.Arg(0)
	if path == "" {
		fmt.Fprintln(os.Stderr, "text: path argument required")
		return 2
	}

	reg := tools.NewRegistry()
	res := reg.Dispatch(tools.ToolCall{
		Name: "document_parse",
		Arguments: map[string]any{
			"path":        path,
			"include_raw": "true",
		},
	})
	if !res.Success {
		fmt.Fprintf(os.Stderr, "text: %s\n", res.Error)
		return 1
	}

	var parsed struct {
		RawText string `json:"raw_text"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "text: invalid JSON from parser: %v\n", err)
		return 1
	}
	text := strings.TrimSpace(parsed.RawText)
	// Drop the "[7 pages extracted]" line the reader puts first; a client's
	// text file should start with the document.
	if strings.HasPrefix(text, "[") {
		if end := strings.Index(text, "]"); end > 0 && strings.HasSuffix(text[:end], "pages extracted") {
			text = strings.TrimSpace(text[end+1:])
		}
	}
	if text == "" {
		fmt.Fprintf(os.Stderr, "text: no text extracted (status=%s)\n", parsed.Status)
		return 1
	}
	// Pages are separated by a form feed, as pdftotext does, not by
	// Terminus's internal page marker.
	text = strings.ReplaceAll(text, tools.PageBreakSentinel, "\n\f\n")
	_, _ = os.Stdout.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		_, _ = os.Stdout.WriteString("\n")
	}
	return 0
}

func runBatch(args []string) int {
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	inDir := ""
	outDir := ""
	var includeRaw bool
	var autoHeal bool
	var packName string
	fs.StringVar(&inDir, "in", "", "input directory of documents")
	fs.StringVar(&outDir, "out", "", "output directory for JSON results (required)")
	fs.BoolVar(&includeRaw, "raw", false, "include raw_text in each JSON file")
	fs.BoolVar(&autoHeal, "auto-heal", true, "heal then re-parse when initial parse is not ok")
	fs.StringVar(&packName, "pack", "", "field pack to enable (e.g. medical)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if inDir == "" || outDir == "" {
		fmt.Fprintln(os.Stderr, "batch: --in and --out are required")
		return 2
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "batch: mkdir out: %v\n", err)
		return 1
	}

	entries, err := os.ReadDir(inDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "batch: read input dir: %v\n", err)
		return 1
	}

	var failed int
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		inPath := filepath.Join(inDir, name)
		body, ok := parseDocument(inPath, includeRaw, autoHeal, packName)
		// Keep the ending in the output name (scan.pdf.json, scan.png.json) so
		// two inputs that differ only by type do not overwrite each other.
		outPath := filepath.Join(outDir, name+".json")
		if body == nil {
			failed++
			_ = os.WriteFile(outPath, []byte(`{"status":"failed","path":`+jsonString(inPath)+`}`), 0644)
			continue
		}
		if err := os.WriteFile(outPath, body, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "batch: write %s: %v\n", outPath, err)
			failed++
			continue
		}
		if !ok {
			failed++
		}
	}

	summary := map[string]any{
		"input_dir":  inDir,
		"output_dir": outDir,
		"failed":     failed,
		"total":      len(entries),
	}
	b, _ := json.Marshal(summary)
	fmt.Println(string(b))
	if failed > 0 {
		return 1
	}
	return 0
}

func readPathFromRequest(r *http.Request) (path string, includeRaw bool, err error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			return "", false, fmt.Errorf("multipart: %w", err)
		}
		path = strings.TrimSpace(r.FormValue("path"))
		includeRaw = strings.EqualFold(r.FormValue("include_raw"), "true")
		file, hdr, ferr := r.FormFile("file")
		if ferr == nil && hdr != nil {
			defer file.Close()
			dir := filepath.Join(defaultOutputDir(), "api-uploads")
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", false, err
			}
			dest := filepath.Join(dir, sanitizeFilename(hdr.Filename))
			if err := saveUpload(file, dest); err != nil {
				return "", false, err
			}
			return dest, includeRaw, nil
		}
		if path == "" {
			return "", false, fmt.Errorf("provide form field path or file upload")
		}
		return path, includeRaw, nil
	}

	var body struct {
		Path       string `json:"path"`
		IncludeRaw bool   `json:"include_raw"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("json body: %w", err)
	}
	if strings.TrimSpace(body.Path) == "" {
		return "", false, fmt.Errorf("path is required")
	}
	return body.Path, body.IncludeRaw, nil
}

func writeToolResult(w http.ResponseWriter, res tools.ToolResult) {
	w.Header().Set("Content-Type", "application/json")
	if !res.Success {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": res.Error,
		})
		return
	}
	_, _ = w.Write([]byte(res.Output))
}

func saveUpload(src io.Reader, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, src)
	return err
}

func defaultOutputDir() string {
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, "workspace", "outputs")
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := fs.Arg(0)
	if dir == "" {
		fmt.Fprintln(os.Stderr, "check: directory path required")
		return 2
	}
	sum, err := tools.CheckPDFDirectory(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check: %v\n", err)
		return 1
	}
	fmt.Printf("checked %d PDFs: %d clean, %d with warnings, %d failed\n", sum.Total, sum.Clean, sum.WithWarnings, sum.Failed)
	for _, f := range sum.Failures {
		fmt.Println("  " + f)
	}
	if sum.Failed > 0 {
		return 1
	}
	return 0
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// tidyArgs lets options be written before or after the file name, the way the
// help text shows them, and removes -q / --quiet (handled once, in main).
// Go's own option reader stops at the first plain word, so "heal file.pdf
// --out-dir x" used to ignore the folder.
func tidyArgs(args []string) []string {
	takesValue := map[string]bool{"o": true, "out-dir": true, "in": true, "out": true, "port": true, "format": true, "pack": true}
	var opts, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-q" || a == "--quiet" {
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			opts = append(opts, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && takesValue[name] && i+1 < len(args) {
				i++
				opts = append(opts, args[i])
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(opts, rest...)
}

func quietLogs(argv []string) bool {
	for _, a := range argv {
		if a == "--quiet" || a == "-q" {
			return true
		}
	}
	return false
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		default:
			return r
		}
	}, name)
	if name == "" {
		return "upload.bin"
	}
	return name
}
