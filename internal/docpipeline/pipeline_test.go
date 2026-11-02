package docpipeline_test

import (
	"strings"
	"testing"

	"terminus/internal/docpipeline"
)

func TestExtractCandidates_SSN(t *testing.T) {
	text := "Patient SSN: 123-45-6789. DOB: 01/15/1980."
	r := docpipeline.ExtractCandidates(text)

	ssnFound := false
	for _, c := range r.Candidates {
		if c.Field == "ssn" && c.Value == "123-45-6789" {
			ssnFound = true
			if c.Confidence < 0.85 {
				t.Errorf("expected SSN confidence >= 0.85, got %.2f", c.Confidence)
			}
		}
	}
	if !ssnFound {
		t.Error("expected SSN candidate '123-45-6789' not found")
	}
}

func TestExtractCandidates_DOB(t *testing.T) {
	text := "Date of Birth: 03/22/1975"
	r := docpipeline.ExtractCandidates(text)

	dobFound := false
	for _, c := range r.Candidates {
		if c.Field == "dob" {
			dobFound = true
		}
	}
	if !dobFound {
		t.Error("expected DOB candidate not found")
	}
}

func TestExtractCandidates_PatientNameFullName(t *testing.T) {
	text := "Patient: John Smith\nDOB: 01/15/1980"
	r := docpipeline.ExtractCandidates(text, "medical")

	for _, c := range r.Candidates {
		if c.Field == "patient_name" && c.Value == "John Smith" {
			return
		}
	}
	t.Fatalf("expected full patient name John Smith, got candidates: %#v", r.Candidates)
}

func TestExtractCandidates_PhoneWithParentheses(t *testing.T) {
	text := "Phone: (555) 123-4567"
	r := docpipeline.ExtractCandidates(text)

	for _, c := range r.Candidates {
		if c.Field == "phone" && c.Value == "(555) 123-4567" {
			return
		}
	}
	t.Fatalf("expected phone with parentheses, got candidates: %#v", r.Candidates)
}

func TestExtractCandidates_Email(t *testing.T) {
	text := "Contact: john.smith@example.com for follow-up."
	r := docpipeline.ExtractCandidates(text)

	emailFound := false
	for _, c := range r.Candidates {
		if c.Field == "email" && c.Value == "john.smith@example.com" {
			emailFound = true
		}
	}
	if !emailFound {
		t.Error("expected email candidate not found")
	}
}

func TestExtractCandidates_NoFields(t *testing.T) {
	text := "This is a generic note with no structured data."
	r := docpipeline.ExtractCandidates(text)
	if len(r.Warnings) == 0 {
		t.Error("expected warning for text with no structured fields")
	}
}

func TestVerify_Valid(t *testing.T) {
	r := docpipeline.ExtractionResult{
		Candidates: []docpipeline.Candidate{
			{Field: "ssn", Value: "123-45-6789", Confidence: 0.90, Source: "ssn_canonical"},
			{Field: "phone", Value: "555-123-4567", Confidence: 0.82, Source: "phone"},
		},
	}
	vr := docpipeline.Verify(r)
	if vr.Status != docpipeline.StatusValid {
		t.Errorf("expected valid, got %s: %v", vr.Status, vr.Conflicts)
	}
	if len(vr.Approved) != 2 {
		t.Errorf("expected 2 approved candidates, got %d", len(vr.Approved))
	}
}

func TestVerify_Ambiguous(t *testing.T) {
	// Two distinct patient names = ambiguous
	r := docpipeline.ExtractionResult{
		Candidates: []docpipeline.Candidate{
			{Field: "patient_name", Value: "John Smith", Confidence: 0.75, Span: [2]int{0, 10}, Source: "labeled"},
			{Field: "patient_name", Value: "Jane Smith", Confidence: 0.75, Span: [2]int{50, 60}, Source: "labeled"},
		},
	}
	vr := docpipeline.Verify(r)
	if vr.Status != docpipeline.StatusAmbiguous {
		t.Errorf("expected ambiguous status, got %s", vr.Status)
	}
	if len(vr.Conflicts) == 0 {
		t.Error("expected at least one conflict")
	}
}

func TestRun_DirtyMultiPatientFaxPreservesAmbiguity(t *testing.T) {
	text := `
*** FAX OCR COPY - skewed / partial duplicate ***
-- SECTION A --
Patient: John Smith
DOB: 01/15/1980
MRN: MRN-00123456
Phone: (555) 123-4567

--- duplicated referral separator / table drift ---
Pt Name: Jane Smith
DOB: 01/15/1980
MRN: MRN-00999999
Phone: 555-123-4567

handwritten note: chest pn x2h, rule out MI
`
	pr, vr := docpipeline.Run(text, "medical")

	if vr.Status != docpipeline.StatusAmbiguous {
		t.Fatalf("expected ambiguous status for multi-patient dirty fax, got %s conflicts=%v entities=%#v", vr.Status, vr.Conflicts, pr.Entities)
	}
	fields := pr.ToFieldMap(false)
	if _, ok := fields["patient_name"]; ok {
		t.Fatalf("ambiguous patient_name must not reach execution field map: %#v", fields)
	}
	if _, ok := fields["mrn"]; ok {
		t.Fatalf("ambiguous MRN must not reach execution field map: %#v", fields)
	}
	if len(pr.AmbiguousKeys) == 0 {
		t.Fatalf("expected ambiguous keys, got result: %#v", pr)
	}
}

func TestVerify_BelowThreshold(t *testing.T) {
	r := docpipeline.ExtractionResult{
		Candidates: []docpipeline.Candidate{
			{Field: "ssn", Value: "123456789", Confidence: 0.55, Source: "ssn_nodash"},
		},
	}
	vr := docpipeline.Verify(r)
	if vr.Status != docpipeline.StatusInvalid {
		t.Errorf("expected invalid (below threshold), got %s", vr.Status)
	}
}

func TestResolve_PicksBestCandidate(t *testing.T) {
	vr := docpipeline.VerificationResult{
		Status: docpipeline.StatusValid,
		Approved: []docpipeline.Candidate{
			{Field: "ssn", Value: "123-45-6789", Confidence: 0.90, Source: "ssn_canonical"},
		},
	}
	pr := docpipeline.Resolve(vr)
	if len(pr.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(pr.Entities))
	}
	if pr.Entities[0].Value != "123-45-6789" {
		t.Errorf("unexpected value: %s", pr.Entities[0].Value)
	}
	if pr.Entities[0].NeedsReview {
		t.Error("should not need review when single clean candidate")
	}
}

func TestResolve_FlagsAmbiguous(t *testing.T) {
	vr := docpipeline.VerificationResult{
		Status: docpipeline.StatusAmbiguous,
		Approved: []docpipeline.Candidate{
			{Field: "patient_name", Value: "John Smith", Confidence: 0.75, Source: "a"},
			{Field: "patient_name", Value: "Jane Smith", Confidence: 0.72, Source: "b"},
		},
		Conflicts: []string{"field patient_name has 2 distinct candidates"},
	}
	pr := docpipeline.Resolve(vr)
	if len(pr.AmbiguousKeys) == 0 {
		t.Error("expected ambiguous keys")
	}
	if !strings.Contains(pr.AmbiguousKeys[0], "patient_name") {
		t.Errorf("unexpected ambiguous key: %v", pr.AmbiguousKeys)
	}
	if len(pr.Entities) == 0 || !pr.Entities[0].NeedsReview {
		t.Error("entity should be flagged needs_review")
	}
}

func TestRun_FullPipeline(t *testing.T) {
	text := `
Patient: John Smith
SSN: 123-45-6789
DOB: 01/15/1980
Phone: (555) 123-4567
MRN: MRN-00123456
`
	pr, vr := docpipeline.Run(text, "medical")
	if vr.Status == docpipeline.StatusInvalid {
		t.Errorf("expected valid or ambiguous pipeline result, got invalid: %v", vr.Conflicts)
	}
	if len(pr.Entities) == 0 {
		t.Error("expected at least one resolved entity")
	}
	t.Logf("Pipeline result: %s", pr.Summary)
	for _, e := range pr.Entities {
		t.Logf("  %s = %q (%.0f%%) review=%v", e.Field, e.Value, e.Confidence*100, e.NeedsReview)
	}
}

func TestToFieldMap_ExcludesAmbiguous(t *testing.T) {
	pr := docpipeline.PipelineResult{
		Entities: []docpipeline.ResolvedEntity{
			{Field: "ssn", Value: "123-45-6789", Confidence: 0.90},
			{Field: "patient_name", Value: "John Smith", Confidence: 0.75, NeedsReview: true},
		},
		AmbiguousKeys: []string{"patient_name"},
	}

	m := pr.ToFieldMap(false)
	if _, ok := m["patient_name"]; ok {
		t.Error("ambiguous field should be excluded when includeAmbiguous=false")
	}
	if m["ssn"] != "123-45-6789" {
		t.Errorf("expected SSN in field map, got: %v", m)
	}
}

func TestExtractCandidates_ClinicalEntities(t *testing.T) {
	text := "Provider: Dr. Alice Vance\nNPI: 1234567890\nDOS: 06/15/2026"
	r := docpipeline.ExtractCandidates(text, "medical")

	foundNPI, foundProvider, foundDOS := false, false, false
	for _, c := range r.Candidates {
		if c.Field == "npi" && c.Value == "1234567890" {
			foundNPI = true
		}
		if c.Field == "provider_name" && strings.Contains(c.Value, "Alice Vance") {
			foundProvider = true
		}
		if c.Field == "dos" && c.Value == "06/15/2026" {
			foundDOS = true
		}
	}

	if !foundNPI {
		t.Error("expected NPI candidate '1234567890' not found")
	}
	if !foundProvider {
		t.Error("expected provider_name candidate containing 'Alice Vance' not found")
	}
	if !foundDOS {
		t.Error("expected DOS candidate '06/15/2026' not found")
	}
}

func TestExtractCandidates_GeneralPack(t *testing.T) {
	text := "Vendor: ACME Supply\nInvoice #: INV-9988\nDate: 2026-10-15\nTotal Due: $450.00\nBill to: John Smith"
	r := docpipeline.ExtractCandidates(text)

	foundVendor, foundInvoice, foundDate, foundTotal, foundCustomer := false, false, false, false, false
	for _, c := range r.Candidates {
		if c.Field == "vendor" && strings.Contains(c.Value, "ACME Supply") {
			foundVendor = true
		}
		if c.Field == "invoice_number" && c.Value == "INV-9988" {
			foundInvoice = true
		}
		if c.Field == "date" && c.Value == "2026-10-15" {
			foundDate = true
		}
		if c.Field == "total" && c.Value == "450.00" {
			foundTotal = true
		}
		if c.Field == "customer" && c.Value == "John Smith" {
			foundCustomer = true
		}
	}
	if !foundVendor {
		t.Error("expected vendor candidate not found")
	}
	if !foundInvoice {
		t.Error("expected invoice candidate not found")
	}
	if !foundDate {
		t.Error("expected date candidate not found")
	}
	if !foundTotal {
		t.Error("expected total candidate not found")
	}
	if !foundCustomer {
		t.Error("expected customer candidate not found")
	}
}

func TestExtractCandidates_MedicalPackOptional(t *testing.T) {
	text := "Patient: Jane Doe\nMRN: MRN-998877\nDOB: 01/01/1980\nInvoice #: INV-100"

	// Without medical pack
	noMed := docpipeline.ExtractCandidates(text)
	for _, c := range noMed.Candidates {
		if c.Field == "mrn" {
			t.Fatalf("MRN should NOT be extracted without medical pack, got: %v", c)
		}
		if c.Field == "patient_name" {
			t.Fatalf("patient_name should NOT be extracted without medical pack, got: %v", c)
		}
	}

	// With medical pack
	withMed := docpipeline.ExtractCandidates(text, "medical")
	foundMRN := false
	for _, c := range withMed.Candidates {
		if c.Field == "mrn" && c.Value == "MRN-998877" {
			foundMRN = true
		}
	}
	if !foundMRN {
		t.Fatal("MRN should be extracted with medical pack")
	}
}
