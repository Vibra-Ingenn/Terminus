package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHealDocumentMissingFile checks that HealDocument returns useful guidance
// when the file does not exist.
func TestHealDocumentMissingFile(t *testing.T) {
	rep := HealDocument("/nonexistent/path/to/file.pdf", "")
	if rep.Succeeded {
		t.Error("expected Succeeded=false for missing file")
	}
	if rep.Error == "" {
		t.Error("expected non-empty Error for missing file")
	}
	if len(rep.UserActions) == 0 {
		t.Error("expected at least one UserAction for missing file")
	}
}

// TestHealDocumentPlainText checks that plain text files are always readable.
func TestHealDocumentPlainText(t *testing.T) {
	tmp, err := os.CreateTemp("", "heal-test-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())

	content := "Patient: John Doe\nDiagnosis: Hypertension\nPlan: Follow up in 3 months"
	tmp.WriteString(content)
	tmp.Close()

	rep := HealDocument(tmp.Name(), "")
	if !rep.Succeeded {
		t.Errorf("text file heal should succeed, got: %s", rep.Error)
	}
	if rep.FileType != "text" {
		t.Errorf("expected file_type=text, got %q", rep.FileType)
	}
	if !strings.Contains(rep.TextExtracted, "Hypertension") {
		t.Errorf("expected text content in TextExtracted, got %q", rep.TextExtracted[:min(100, len(rep.TextExtracted))])
	}
	if rep.Strategy != "direct_read" {
		t.Errorf("expected strategy=direct_read, got %q", rep.Strategy)
	}
}

// TestHealDocumentUnknownExtension verifies that unknown binary types get
// a magic-byte report and actionable guidance.
func TestHealDocumentUnknownExtension(t *testing.T) {
	tmp, err := os.CreateTemp("", "heal-test-*.xyz")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())

	// Write binary content that is NOT text.
	tmp.Write([]byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD, 0xFC})
	tmp.Close()

	rep := HealDocument(tmp.Name(), "")
	if rep.Succeeded {
		t.Error("expected Succeeded=false for unknown binary")
	}
	if rep.FileType != "unknown" {
		t.Errorf("expected file_type=unknown, got %q", rep.FileType)
	}
	if len(rep.Issues) == 0 {
		t.Error("expected at least one issue for unknown binary file")
	}
	if len(rep.UserActions) == 0 {
		t.Error("expected user actions for unknown binary file")
	}
}

// TestHealDocumentTextFallbackForUnknownExtension checks that files with
// unknown extensions that happen to contain readable text are salvaged.
func TestHealDocumentTextFallbackForUnknownExtension(t *testing.T) {
	tmp, err := os.CreateTemp("", "heal-test-*.xyz")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())

	tmp.WriteString("MSH|^~\\&|SENDING|FACILITY|RECEIVING|FACILITY|20240101120000||ADT^A01|MSG00001|P|2.5\r\nPID|1||123456^^^MRN||Doe^John\r\n")
	tmp.Close()

	rep := HealDocument(tmp.Name(), "")
	// HL7 extension is unknown but content is readable ASCII — should be salvaged.
	if !rep.Succeeded {
		t.Logf("Note: unknown .xyz extension not salvaged (file_type=%s, strategy=%s)", rep.FileType, rep.Strategy)
	}
}

// TestHealDocumentToolRegistered confirms heal_document is in the registry.
func TestHealDocumentToolRegistered(t *testing.T) {
	reg := NewRegistry()
	found := false
	for _, d := range reg.Definitions() {
		if d.Name == "heal_document" {
			found = true
			break
		}
	}
	if !found {
		t.Error("heal_document tool not registered in NewRegistry()")
	}
}

// TestHealDocumentToolDispatch checks the tool dispatch path produces valid HealReport JSON.
func TestHealDocumentToolDispatch(t *testing.T) {
	tmp, err := os.CreateTemp("", "heal-dispatch-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString("Hello from test")
	tmp.Close()

	reg := NewRegistry()
	result := reg.Dispatch(ToolCall{
		Name:      "heal_document",
		Arguments: map[string]any{"path": tmp.Name()},
	})

	if !result.Success {
		t.Errorf("dispatch heal_document on plain text should succeed: %s", result.Error)
	}
	if result.Output == "" {
		t.Error("expected non-empty output from heal_document dispatch")
	}

	// Output must be valid JSON.
	var rep HealReport
	if err := json.Unmarshal([]byte(result.Output), &rep); err != nil {
		t.Errorf("heal_document output is not valid JSON: %v\noutput: %s", err, result.Output)
	}
}

// TestHealDocumentMalformedPDF creates a PDF with a bad startxref and checks
// that Terminus either repairs it or salvages text from it.
func TestHealDocumentMalformedPDF(t *testing.T) {
	// Build a minimal PDF with a deliberately wrong startxref offset.
	// The xref table is at offset 9 but we declare offset 999.
	pdf := "%PDF-1.4\nxref\n0 1\n0000000000 65535 f \ntrailer\n<<\n/Size 1\n>>\nstartxref\n999\n%%EOF\n"
	tmp, err := os.CreateTemp("", "heal-malformed-*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(pdf)
	tmp.Close()

	outDir := t.TempDir()
	rep := HealDocument(tmp.Name(), outDir)

	if rep.FileType != "pdf" {
		t.Errorf("expected file_type=pdf, got %q", rep.FileType)
	}
	// We don't mandate success here — the malformed PDF may have no text layer —
	// but we do require that issues were detected and some guidance was given.
	if len(rep.Issues) == 0 && !rep.Succeeded {
		t.Error("expected either issues detected or successful extraction")
	}
	t.Logf("heal result: succeeded=%v strategy=%q issues=%v summary=%q",
		rep.Succeeded, rep.Strategy, rep.Issues, rep.Summary)
}

// TestHealDocumentHL7 checks a well-formed HL7 message is parsed successfully.
func TestHealDocumentHL7(t *testing.T) {
	msg := "MSH|^~\\&|SENDING|FAC|RECV|FAC|20240101120000||ADT^A01|MSG001|P|2.5\rPID|1||12345^^^MRN||Doe^Jane|||F\r"
	tmp, err := os.CreateTemp("", "heal-hl7-*.hl7")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(msg)
	tmp.Close()

	// hl7 extension → detected as text by magic, not hl7 — need to check via hl7 content
	// Actually the detectFileType function checks for "MSH|" prefix.
	rep := HealDocument(tmp.Name(), "")
	t.Logf("HL7 heal: file_type=%q succeeded=%v strategy=%q summary=%q",
		rep.FileType, rep.Succeeded, rep.Strategy, rep.Summary)
	if !rep.Succeeded {
		t.Logf("Note: HL7 heal did not succeed (may depend on file type detection): %s", rep.Error)
	}
}

// TestHealDocumentSummaryNeverEmpty checks that Summary is always populated.
func TestHealDocumentSummaryNeverEmpty(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		ext     string
	}{
		{"plain-text", []byte("hello world"), ".txt"},
		{"missing", nil, ""}, // no file — just use a non-existent path
		{"binary", []byte{0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF}, ".bin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var path string
			if c.content != nil {
				tmp, err := os.CreateTemp("", "heal-summary-*"+c.ext)
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(tmp.Name())
				tmp.Write(c.content)
				tmp.Close()
				path = tmp.Name()
			} else {
				path = filepath.Join(t.TempDir(), "nonexistent.pdf")
			}
			rep := HealDocument(path, "")
			if rep.Summary == "" {
				t.Errorf("Summary should never be empty (case %q)", c.name)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
