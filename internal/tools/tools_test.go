package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNewRegistryHasBuiltins(t *testing.T) {
	r := NewRegistry()
	defs := r.Definitions()
	if len(defs) == 0 {
		t.Fatal("NewRegistry should have built-in tools")
	}

	// Check that key builtins exist
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"read_file", "write_file", "list_dir", "calculate", "get_time"} {
		if !names[want] {
			t.Errorf("missing built-in tool %q", want)
		}
	}
}

func TestRegisterCustomTool(t *testing.T) {
	r := NewRegistry()
	before := len(r.Definitions())

	r.Register(ToolDef{
		Name:        "my_tool",
		Description: "test tool",
		Parameters:  map[string]string{"x": "number"},
	}, func(args map[string]any) ToolResult {
		return ToolResult{ToolName: "my_tool", Success: true, Output: "ok"}
	})

	after := len(r.Definitions())
	if after != before+1 {
		t.Errorf("expected %d tools after register, got %d", before+1, after)
	}
}

func TestDispatchKnownTool(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "get_time", Arguments: map[string]any{}})
	if !result.Success {
		t.Errorf("get_time dispatch failed: %s", result.Error)
	}
	if result.Output == "" {
		t.Error("get_time should return non-empty output")
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "nonexistent", Arguments: map[string]any{}})
	if result.Success {
		t.Error("unknown tool should return failure")
	}
	if !strings.Contains(result.Error, "unknown tool") {
		t.Errorf("error should mention unknown tool, got %q", result.Error)
	}
}

func TestParseToolCall(t *testing.T) {
	raw := `{"tool_call": {"name": "calculate", "arguments": {"expression": "2+2"}}}`
	tc, err := ParseToolCall(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Name != "calculate" {
		t.Errorf("name = %q, want calculate", tc.Name)
	}
	if tc.Arguments["expression"] != "2+2" {
		t.Errorf("expression = %v, want 2+2", tc.Arguments["expression"])
	}
}

func TestParseToolCallEmptyName(t *testing.T) {
	raw := `{"tool_call": {"name": "", "arguments": {}}}`
	_, err := ParseToolCall(raw)
	if err == nil {
		t.Error("should error on empty tool name")
	}
}

func TestParseToolCallInvalidJSON(t *testing.T) {
	_, err := ParseToolCall("not json")
	if err == nil {
		t.Error("should error on invalid JSON")
	}
}

func TestParseToolCallRepairsSemicolonSeparators(t *testing.T) {
	raw := `{"tool_call":{"name":"done";"arguments":{"summary":"ok"};};}`
	tc, err := ParseToolCall(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Name != "done" {
		t.Errorf("name = %q, want done", tc.Name)
	}
	if tc.Arguments["summary"] != "ok" {
		t.Errorf("summary = %v, want ok", tc.Arguments["summary"])
	}
}

func TestParseToolCallRepairsProseAndTrailingJunk(t *testing.T) {
	raw := "Sure.\n```json\n" +
		`{"tool_call":{"name":"calculate";"arguments":{"expression":"2+2",},},};` +
		"\n```\nextra"
	tc, err := ParseToolCall(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Name != "calculate" {
		t.Errorf("name = %q, want calculate", tc.Name)
	}
	if tc.Arguments["expression"] != "2+2" {
		t.Errorf("expression = %v, want 2+2", tc.Arguments["expression"])
	}
}

func TestParseToolCallClosesMissingBraces(t *testing.T) {
	raw := `{"tool_call":{"name":"done","arguments":{"summary":"hit context limit"}`
	tc, err := ParseToolCall(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Name != "done" {
		t.Errorf("name = %q, want done", tc.Name)
	}
}

func TestParseToolCallRepairsPeriodSeparators(t *testing.T) {
	raw := `{"tool_call":{"name":"chart_template","arguments":{"patient_name":"John Doe". "patient_dob":"1990-01-01"}}`
	tc, err := ParseToolCall(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Name != "chart_template" {
		t.Errorf("name = %q, want chart_template", tc.Name)
	}
	if tc.Arguments["patient_name"] != "John Doe" || tc.Arguments["patient_dob"] != "1990-01-01" {
		t.Errorf("args mismatch: %v", tc.Arguments)
	}
}

func TestParseToolCallClosesTruncatedString(t *testing.T) {
	raw := `{"tool_call":{"name":"chart_template","arguments":{"patient_name":"John Doe","patient_dob":"1990`
	tc, err := ParseToolCall(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Name != "chart_template" {
		t.Errorf("name = %q, want chart_template", tc.Name)
	}
	if tc.Arguments["patient_dob"] != "1990" {
		t.Errorf("patient_dob = %v, want 1990", tc.Arguments["patient_dob"])
	}
}

func TestCalculateTool(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{
		Name:      "calculate",
		Arguments: map[string]any{"expression": "3 + 4 * 2"},
	})
	if !result.Success {
		t.Errorf("calculate failed: %s", result.Error)
	}
}

func TestSystemPromptBlock(t *testing.T) {
	r := NewRegistry()
	block := r.SystemPromptBlock()
	if !strings.Contains(block, "tool_call") {
		t.Error("system prompt block should contain tool_call schema")
	}
	if !strings.Contains(block, "read_file") {
		t.Error("system prompt block should list read_file")
	}
}

// ── Medical tool tests ───────────────────────────────────────────────────

func TestRegistryHasMedicalTools(t *testing.T) {
	r := NewRegistry()
	names := make(map[string]bool)
	for _, d := range r.Definitions() {
		names[d.Name] = true
	}
	for _, want := range []string{
		"pdf_extract", "pdf_diagnose", "chart_template", "icd10_lookup", "anonymize", "fhir_export",
		"hl7_parse", "vitals_check", "medication_check", "encrypt_data", "audit_log",
	} {
		if !names[want] {
			t.Errorf("missing medical tool %q", want)
		}
	}
}

func TestChartTemplateSOAP(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "chart_template", Arguments: map[string]any{
		"type":            "soap",
		"patient_name":    "John Doe",
		"chief_complaint": "Chest pain",
		"subjective":      "Patient reports sharp chest pain for 2 hours",
		"assessment":      "Rule out MI",
	}})
	if !result.Success {
		t.Fatalf("chart_template failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "SOAP NOTE") {
		t.Error("output should contain SOAP NOTE header")
	}
	if !strings.Contains(result.Output, "John Doe") {
		t.Error("output should contain patient name")
	}
	if !strings.Contains(result.Output, "Chest pain") {
		t.Error("output should contain chief complaint")
	}
}

func TestLocalDocumentToPatientChartSmoke(t *testing.T) {
	r := NewRegistry()
	docPath := filepath.Join(t.TempDir(), "referral.txt")
	doc := `
Patient: John Smith
DOB: 01/15/1980
MRN: MRN-00123456
Phone: (555) 123-4567
Email: john.smith@example.com
Chief complaint: Chest pain for 2 hours.
Vitals: BP 150/95 HR 102 Temp 98.6
Assessment: Rule out MI.
Plan: ECG, troponin, aspirin if not contraindicated.
`
	if err := os.WriteFile(docPath, []byte(doc), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	extracted := r.Dispatch(ToolCall{Name: "extract_medical_entities", Arguments: map[string]any{
		"path": docPath,
	}})
	if !extracted.Success {
		t.Fatalf("extract_medical_entities failed: %s", extracted.Error)
	}

	var parsed struct {
		Stage0Method string            `json:"stage0_method"`
		Status       string            `json:"verification_status"`
		FieldMap     map[string]string `json:"field_map"`
		Summary      string            `json:"summary"`
	}
	if err := json.Unmarshal([]byte(extracted.Output), &parsed); err != nil {
		t.Fatalf("parse extraction output: %v\n%s", err, extracted.Output)
	}
	if parsed.Status == "invalid" {
		t.Fatalf("expected valid or ambiguous extraction, got invalid: %s", extracted.Output)
	}
	if parsed.FieldMap["patient_name"] != "John Smith" {
		t.Fatalf("expected patient name John Smith, got field map: %#v", parsed.FieldMap)
	}

	chart := r.Dispatch(ToolCall{Name: "chart_template", Arguments: map[string]any{
		"type":            "soap",
		"patient_name":    parsed.FieldMap["patient_name"],
		"chief_complaint": "Chest pain for 2 hours",
		"subjective":      "Referral note reports chest pain for 2 hours.",
		"objective":       fmt.Sprintf("DOB: %s\nMRN: %s\nPhone: %s", parsed.FieldMap["dob"], parsed.FieldMap["mrn"], parsed.FieldMap["phone"]),
		"assessment":      "Rule out MI.",
		"plan":            "ECG, troponin, aspirin if not contraindicated.",
	}})
	if !chart.Success {
		t.Fatalf("chart_template failed: %s", chart.Error)
	}
	for _, want := range []string{"SOAP NOTE", "John Smith", "Chest pain", "Rule out MI"} {
		if !strings.Contains(chart.Output, want) {
			t.Fatalf("chart output missing %q:\n%s", want, chart.Output)
		}
	}
	t.Logf("local extraction method=%s status=%s summary=%s", parsed.Stage0Method, parsed.Status, parsed.Summary)
	t.Logf("generated chart preview:\n%s", truncateTestOutput(chart.Output, 700))
}

func TestLocalDirtyFaxDoesNotProduceFalseCleanChart(t *testing.T) {
	r := NewRegistry()
	docPath := filepath.Join(t.TempDir(), "dirty-fax.txt")
	doc := `
*** FAX OCR COPY - skewed / partial duplicate ***
Patient: John Smith
DOB: 01/15/1980
MRN: MRN-00123456
Phone: (555) 123-4567

--- lower copied section starts here ---
Patient: Jane Smith
DOB: 01/15/1980
MRN: MRN-00999999
Phone: 555-123-4567

handwritten note over margin: chest pn x2h, rule out MI
`
	if err := os.WriteFile(docPath, []byte(doc), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	extracted := r.Dispatch(ToolCall{Name: "extract_medical_entities", Arguments: map[string]any{
		"path": docPath,
	}})
	if !extracted.Success {
		t.Fatalf("extract_medical_entities failed: %s", extracted.Error)
	}

	var parsed struct {
		Status        string            `json:"verification_status"`
		Conflicts     []string          `json:"conflicts"`
		FieldMap      map[string]string `json:"field_map"`
		AmbiguousKeys []string          `json:"ambiguous_keys"`
	}
	if err := json.Unmarshal([]byte(extracted.Output), &parsed); err != nil {
		t.Fatalf("parse extraction output: %v\n%s", err, extracted.Output)
	}
	if parsed.Status != "ambiguous" {
		t.Fatalf("expected ambiguous dirty fax, got %s output:\n%s", parsed.Status, extracted.Output)
	}
	if _, ok := parsed.FieldMap["patient_name"]; ok {
		t.Fatalf("ambiguous patient_name must be withheld from chart inputs: %#v", parsed.FieldMap)
	}
	if _, ok := parsed.FieldMap["mrn"]; ok {
		t.Fatalf("ambiguous MRN must be withheld from chart inputs: %#v", parsed.FieldMap)
	}
	if len(parsed.Conflicts) == 0 || len(parsed.AmbiguousKeys) == 0 {
		t.Fatalf("expected conflicts and ambiguous keys, output:\n%s", extracted.Output)
	}
}

func truncateTestOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestICD10LookupByKeyword(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "icd10_lookup", Arguments: map[string]any{
		"query": "chest pain",
	}})
	if !result.Success {
		t.Fatalf("icd10_lookup failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "R07.9") {
		t.Errorf("should find R07.9 for chest pain, got: %s", result.Output)
	}
}

func TestICD10LookupByCode(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "icd10_lookup", Arguments: map[string]any{
		"query": "I10",
	}})
	if !result.Success {
		t.Fatalf("icd10_lookup failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "hypertension") {
		t.Error("should find hypertension for I10")
	}
}

func TestAnonymizeSSN(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "anonymize", Arguments: map[string]any{
		"text": "Patient SSN is 123-45-6789 and phone is 555-123-4567",
	}})
	if !result.Success {
		t.Fatalf("anonymize failed: %s", result.Error)
	}
	if strings.Contains(result.Output, "123-45-6789") {
		t.Error("SSN should be redacted")
	}
	if !strings.Contains(result.Output, "[REDACTED-SSN]") {
		t.Error("should contain [REDACTED-SSN]")
	}
}

func TestFHIRExportBasic(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "fhir_export", Arguments: map[string]any{
		"patient_name": "Jane Smith",
		"diagnosis":    "Hypertension",
		"icd10_code":   "I10",
	}})
	if !result.Success {
		t.Fatalf("fhir_export failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "Bundle") {
		t.Error("output should contain FHIR Bundle")
	}
	if !strings.Contains(result.Output, "I10") {
		t.Error("output should contain ICD-10 code")
	}
}

func TestHL7Parse(t *testing.T) {
	msg := "MSH|^~\\&|SENDING|FAC|RECEIVING|FAC|20230101120000||ADT^A01|12345|P|2.3\nPID|||12345||Doe^John||19800101|M"
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "hl7_parse", Arguments: map[string]any{
		"message": msg,
	}})
	if !result.Success {
		t.Fatalf("hl7_parse failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "ADT^A01") {
		t.Error("should parse message type ADT^A01")
	}
	if !strings.Contains(result.Output, "Doe^John") {
		t.Error("should parse patient name")
	}
	if !strings.Contains(result.Output, "12345") {
		t.Error("should parse patient ID")
	}
}

func TestVitalsCheckNormal(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "vitals_check", Arguments: map[string]any{
		"heart_rate":    float64(72),
		"systolic_bp":   float64(120),
		"diastolic_bp":  float64(80),
		"temperature":   float64(98.6),
		"o2_saturation": float64(98),
	}})
	if !result.Success {
		t.Fatalf("vitals_check failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "NORMAL") {
		t.Error("normal vitals should show NORMAL")
	}
	if strings.Contains(result.Output, "CRITICAL") {
		t.Error("normal vitals should NOT show CRITICAL")
	}
}

func TestVitalsCheckCritical(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "vitals_check", Arguments: map[string]any{
		"heart_rate":    float64(180),
		"o2_saturation": float64(85),
	}})
	if !result.Success {
		t.Fatalf("vitals_check failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "CRITICAL") {
		t.Error("extreme vitals should show CRITICAL")
	}
}

func TestMedicationCheckInteraction(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "medication_check", Arguments: map[string]any{
		"medications": "warfarin, aspirin, metformin",
	}})
	if !result.Success {
		t.Fatalf("medication_check failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "bleeding") {
		t.Error("warfarin + aspirin should warn about bleeding")
	}
}

func TestMedicationCheckNoInteraction(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "medication_check", Arguments: map[string]any{
		"medications": "acetaminophen, vitamin d",
	}})
	if !result.Success {
		t.Fatalf("medication_check failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "no known interactions") {
		t.Error("acetaminophen + vitamin d should have no interactions")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Generate a test key (32 bytes = 64 hex chars)
	testKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	original := "Patient John Doe, SSN 123-45-6789"

	r := NewRegistry()

	// Encrypt
	encResult := r.Dispatch(ToolCall{Name: "encrypt_data", Arguments: map[string]any{
		"action": "encrypt",
		"text":   original,
		"key":    testKey,
	}})
	if !encResult.Success {
		t.Fatalf("encrypt failed: %s", encResult.Error)
	}

	// Extract base64 from output (last line)
	lines := strings.Split(encResult.Output, "\n")
	ciphertext := lines[len(lines)-1]

	// Decrypt
	decResult := r.Dispatch(ToolCall{Name: "encrypt_data", Arguments: map[string]any{
		"action": "decrypt",
		"text":   ciphertext,
		"key":    testKey,
	}})
	if !decResult.Success {
		t.Fatalf("decrypt failed: %s", decResult.Error)
	}
	if decResult.Output != original {
		t.Errorf("round-trip failed: got %q, want %q", decResult.Output, original)
	}
}

func TestAuditLog(t *testing.T) {
	r := NewRegistry()
	result := r.Dispatch(ToolCall{Name: "audit_log", Arguments: map[string]any{
		"action":  "test_access",
		"user":    "dr_smith",
		"patient": "John Doe",
		"details": "unit test",
	}})
	if !result.Success {
		t.Fatalf("audit_log failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "audit entry written") {
		t.Error("should confirm entry written")
	}
	if !strings.Contains(result.Output, "dr_smith") {
		t.Error("should contain user name")
	}
}

// ── Universal tool tests ─────────────────────────────────────────────────

func TestRegistryHasUniversalTools(t *testing.T) {
	r := NewRegistry()
	want := []string{"document_parse", "auto_ingest", "ocr_extract", "docx_extract", "image_extract", "render_pdf", "render_docx"}
	names := make(map[string]bool)
	for _, d := range r.Definitions() {
		names[d.Name] = true
	}
	for _, n := range want {
		if !names[n] {
			t.Errorf("registry missing universal tool %q", n)
		}
	}
}

func TestDocumentParseOnText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	content := "Patient: Jane Smith\nDOB: 01/01/1980\nMRN: MRN-12345\nChief complaint: Headache"
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "document_parse", Arguments: map[string]any{"path": p, "pack": "medical"}})
	if !res.Success {
		t.Fatalf("document_parse failed: %s", res.Error)
	}
	var parsed struct {
		Status string            `json:"status"`
		Method string            `json:"method"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatalf("parse document_parse output: %v\n%s", err, res.Output)
	}
	if parsed.Status != "ok" {
		t.Fatalf("status = %q, want ok: %s", parsed.Status, res.Output)
	}
	if parsed.Fields["patient_name"] != "Jane Smith" {
		t.Fatalf("patient_name not extracted: %#v", parsed.Fields)
	}

	// Verify that without medical pack, medical fields are not returned
	noMedRes := r.Dispatch(ToolCall{Name: "document_parse", Arguments: map[string]any{"path": p}})
	if !noMedRes.Success {
		t.Fatalf("document_parse without pack failed: %s", noMedRes.Error)
	}
	var noMedParsed struct {
		Fields map[string]string `json:"fields"`
	}
	_ = json.Unmarshal([]byte(noMedRes.Output), &noMedParsed)
	if _, ok := noMedParsed.Fields["patient_name"]; ok {
		t.Fatalf("patient_name should not be extracted without medical pack: %#v", noMedParsed.Fields)
	}
	if _, ok := noMedParsed.Fields["mrn"]; ok {
		t.Fatalf("mrn should not be extracted without medical pack: %#v", noMedParsed.Fields)
	}
}

func TestLegacyDocumentToolsRerouteToDocumentParse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(p, []byte("Patient: Jane Smith\nMRN: MRN-12345"), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	for _, toolName := range []string{"auto_ingest", "pdf_extract", "pdf_diagnose", "heal_document"} {
		res := r.Dispatch(ToolCall{Name: toolName, Arguments: map[string]any{"path": p}})
		if !res.Success {
			t.Fatalf("%s should reroute through document_parse: %s", toolName, res.Error)
		}
		if res.ToolName != "document_parse" {
			t.Fatalf("%s result tool = %q, want document_parse", toolName, res.ToolName)
		}
		if !strings.Contains(res.Output, `"status": "ok"`) {
			t.Fatalf("%s did not return document_parse JSON: %s", toolName, res.Output)
		}
	}
}

func TestAutoIngestReroutesToDocumentParseOnText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(p, []byte("Patient presents with chest pain and shortness of breath."), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "auto_ingest", Arguments: map[string]any{"path": p}})
	if !res.Success {
		t.Fatalf("auto_ingest failed: %s", res.Error)
	}
	if res.ToolName != "document_parse" {
		t.Fatalf("auto_ingest result tool = %q, want document_parse", res.ToolName)
	}
	if !strings.Contains(res.Output, `"status": "ok"`) {
		t.Errorf("expected document_parse ok status, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "chest pain") {
		t.Errorf("content missing: %s", res.Output)
	}
}

func TestAutoIngestReroutesToDocumentParseOnHL7(t *testing.T) {
	dir := t.TempDir()
	msg := "MSH|^~\\&|X|Y|Z|W|20230101||ADT^A01|1|P|2.3\nPID|||123||Doe^Jane"
	p := filepath.Join(dir, "msg.hl7")
	if err := os.WriteFile(p, []byte(msg), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "auto_ingest", Arguments: map[string]any{"path": p}})
	if !res.Success {
		t.Fatalf("auto_ingest failed: %s", res.Error)
	}
	if res.ToolName != "document_parse" {
		t.Fatalf("auto_ingest result tool = %q, want document_parse", res.ToolName)
	}
	if !strings.Contains(res.Output, `"status": "ok"`) || !strings.Contains(res.Output, "ADT^A01") {
		t.Errorf("expected document_parse HL7 output, got: %s", res.Output)
	}
}

func TestAutoIngestMissingFile(t *testing.T) {
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "auto_ingest", Arguments: map[string]any{"path": "/nonexistent/file.txt"}})
	if res.Success {
		t.Error("should fail on missing file")
	}
}

func TestOCRExtractWithoutTesseract(t *testing.T) {
	// We can only verify the friendly error path when tesseract is absent.
	// If tesseract IS installed on the test machine, we just verify the tool
	// rejects a missing path. Either way, the tool must not panic.
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "ocr_extract", Arguments: map[string]any{"path": "/nonexistent/image.png"}})
	if res.Success {
		t.Error("should fail on missing file")
	}
}

func TestChartTemplateRepairsPathOnlyCall(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	content := "Patient: Jane Smith\nDOB: 01/01/1980\nMRN: MRN-12345\nAssessment: Headache"
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "chart_template", Arguments: map[string]any{"path": p}})
	if !res.Success {
		t.Fatalf("chart_template path repair failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Jane Smith") {
		t.Fatalf("expected repaired chart to contain patient name, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Document parse status") {
		t.Fatalf("expected parse report in chart notes, got: %s", res.Output)
	}
}

func TestRenderPDFBasic(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("workspace/outputs") })
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "render_pdf", Arguments: map[string]any{
		"title":    "Test Report",
		"content":  "# Heading\n\nThis is a test paragraph.\n\n- bullet one\n- bullet two",
		"filename": "unit-test",
	}})
	if !res.Success {
		t.Fatalf("render_pdf failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "workspace/outputs/") {
		t.Errorf("expected output path, got: %s", res.Output)
	}
	// Verify the file exists and has the PDF magic bytes.
	entries, _ := os.ReadDir("workspace/outputs")
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-unit-test.pdf") {
			found = true
			data, _ := os.ReadFile(filepath.Join("workspace/outputs", e.Name()))
			if !strings.HasPrefix(string(data), "%PDF-") {
				t.Errorf("file is not a valid PDF")
			}
			break
		}
	}
	if !found {
		t.Error("PDF file not found in workspace/outputs")
	}
}

func TestRenderPDFRepairsPathOnlyCall(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("workspace/outputs") })
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(p, []byte("Patient: Jane Smith\nPlan: Follow up."), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "render_pdf", Arguments: map[string]any{"path": p}})
	if !res.Success {
		t.Fatalf("render_pdf path repair failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "workspace/outputs/") {
		t.Fatalf("expected rendered PDF path, got: %s", res.Output)
	}
}

func TestPDFExtractRepairsMalformedStartXRef(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("workspace/outputs") })
	r := NewRegistry()
	render := r.Dispatch(ToolCall{Name: "render_pdf", Arguments: map[string]any{
		"title":    "Patient Intake",
		"content":  "Patient Name: Jane Smith\nChief Complaint: Headache",
		"filename": "broken-xref",
	}})
	if !render.Success {
		t.Fatalf("render_pdf failed: %s", render.Error)
	}
	path := strings.TrimSpace(strings.TrimPrefix(render.Output, "PDF created at "))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	re := regexp.MustCompile(`startxref\s+(\d+)`)
	matches := re.FindSubmatchIndex(data)
	if matches == nil {
		t.Fatal("startxref not found in rendered PDF")
	}
	declared := string(data[matches[2]:matches[3]])
	data = append([]byte(nil), data...)
	copy(data[matches[2]:matches[3]], []byte(fmt.Sprintf("%0*d", len(declared), 0)))

	brokenPath := filepath.Join(filepath.Dir(path), "broken-startxref.pdf")
	if err := os.WriteFile(brokenPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	res := r.Dispatch(ToolCall{Name: "pdf_extract", Arguments: map[string]any{"path": brokenPath}})
	if !res.Success {
		t.Fatalf("pdf_extract should repair malformed startxref: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Jane Smith") {
		t.Fatalf("expected extracted text after repair, got: %s", res.Output)
	}
}

func TestPDFExtractSalvagesMalformedPDFText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "salvage.pdf")
	data := []byte("%PDF-1.4\n1 0 obj\n<< /Length 44 >>\nstream\nBT\n/F1 12 Tf\n72 720 Td\n(Patient Name: Jane Smith) Tj\nET\nendstream\nendobj\n%%EOF\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	res := toolPDFExtract(map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("pdf_extract should salvage malformed PDF text: %s", res.Error)
	}
	if !strings.Contains(res.Output, "this PDF was damaged") || !strings.Contains(res.Output, "Jane Smith") {
		t.Fatalf("expected salvaged text, got: %s", res.Output)
	}
}

func TestPDFDiagnoseReportsProblems(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnose.pdf")
	data := []byte("%PDF-1.4\n1 0 obj\n<< /Length 44 >>\nstream\nBT\n/F1 12 Tf\n72 720 Td\n(Patient Name: Jane Smith) Tj\nET\nendstream\nendobj\n%%EOF\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	res := toolPDFDiagnose(map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("pdf_diagnose failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "missing trailer block") || !strings.Contains(res.Output, "recoverable text fragments") {
		t.Fatalf("expected diagnostic details, got: %s", res.Output)
	}
}

func TestRenderDOCXBasic(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("workspace/outputs") })
	r := NewRegistry()
	res := r.Dispatch(ToolCall{Name: "render_docx", Arguments: map[string]any{
		"title":    "Test Letter",
		"content":  "Dear Dr. Smith,\n\nThank you for the referral.\n\nSincerely,\nDr. Jones",
		"filename": "unit-docx",
	}})
	if !res.Success {
		t.Fatalf("render_docx failed: %s", res.Error)
	}
	// Verify the output is a valid ZIP (docx is a zip).
	entries, _ := os.ReadDir("workspace/outputs")
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-unit-docx.docx") {
			found = true
			data, _ := os.ReadFile(filepath.Join("workspace/outputs", e.Name()))
			if len(data) < 4 || data[0] != 0x50 || data[1] != 0x4B {
				t.Errorf("file is not a valid ZIP/DOCX")
			}
			break
		}
	}
	if !found {
		t.Error("DOCX file not found in workspace/outputs")
	}
}

func TestDOCXRoundTrip(t *testing.T) {
	// render_docx → docx_extract should recover the content.
	t.Cleanup(func() { os.RemoveAll("workspace/outputs") })
	r := NewRegistry()
	content := "Patient had a good response to treatment.\nFollow up in 2 weeks."
	res := r.Dispatch(ToolCall{Name: "render_docx", Arguments: map[string]any{
		"content":  content,
		"filename": "roundtrip",
	}})
	if !res.Success {
		t.Fatalf("render_docx failed: %s", res.Error)
	}
	// Pull the path out of the success message.
	path := strings.TrimSpace(strings.TrimPrefix(res.Output, "DOCX created at "))
	res2 := r.Dispatch(ToolCall{Name: "docx_extract", Arguments: map[string]any{"path": path}})
	if !res2.Success {
		t.Fatalf("docx_extract failed: %s", res2.Error)
	}
	if !strings.Contains(res2.Output, "good response to treatment") {
		t.Errorf("round-trip lost content: %s", res2.Output)
	}
}

func TestRenderPDFMarkdownEnhancements(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("workspace/outputs") })
	r := NewRegistry()
	content := `# Document Title

This is **bold text** and this is __more bold__.

---

| Patient Name | DOB | Diagnosis |
|---|---|---|
| John Doe | 1990-01-01 | Diabetes |
| Jane Smith | 1985-05-12 | Hypertension |

---

- Bullet point with **bold** text.`
	res := r.Dispatch(ToolCall{Name: "render_pdf", Arguments: map[string]any{
		"content":  content,
		"filename": "markdown-test",
	}})
	if !res.Success {
		t.Fatalf("render_pdf with markdown failed: %s", res.Error)
	}
}

func TestDocumentParseOnRealTestPDFs(t *testing.T) {
	pdfDir := filepath.Join("..", "..", "docs", "pdf_data_to_test_Janus")
	entries, err := os.ReadDir(pdfDir)
	if err != nil {
		t.Skipf("skipping real PDF tests; directory not found: %v", err)
	}
	r := NewRegistry()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pdf") {
			continue
		}
		pdfPath := filepath.Join(pdfDir, e.Name())
		res := r.Dispatch(ToolCall{Name: "document_parse", Arguments: map[string]any{"path": pdfPath}})
		if !res.Success {
			t.Logf("PDF test %s failed or needs OCR: %s", e.Name(), res.Error)
			continue
		}
		t.Logf("PDF test %s result: success=true len=%d", e.Name(), len(res.Output))
	}
}

