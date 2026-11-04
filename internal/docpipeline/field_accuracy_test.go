package docpipeline_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terminus/internal/docpipeline"
)

// Optional local fixtures: set TERMINUS_INVOICE_FIXTURES to a folder of sample invoices.
var projectInvoicesDir = os.Getenv("TERMINUS_INVOICE_FIXTURES")

type invoiceExpect struct {
	file          string
	vendor        string
	customer      string
	invoiceNumber string
	date          string
	total         string
	wantFields    bool
}

func readInvoiceFixture(name string) (string, bool) {
	path := filepath.Join(projectInvoicesDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func runInvoiceText(t *testing.T, text string) map[string]string {
	pr, _ := docpipeline.Run(text)
	return pr.ToFieldMap(true)
}

func TestFieldAccuracy_ProjectInvoices(t *testing.T) {
	if _, err := os.Stat(projectInvoicesDir); projectInvoicesDir == "" || err != nil {
		t.Skip("project invoice fixtures not present on this machine")
	}

	cases := []invoiceExpect{
		{
			file:          "acme_supply_INV-2041.txt",
			vendor:        "ACME SUPPLY CO.",
			customer:      "Harbor Cafe LLC",
			invoiceNumber: "INV-2041",
			date:          "2026-09-03",
			total:         "258.23",
			wantFields:    true,
		},
		{
			file:          "brightline_electric_2026-09.txt",
			vendor:        "BRIGHTLINE ELECTRIC",
			customer:      "Harbor Cafe LLC",
			invoiceNumber: "BL-778120",
			date:          "2026-09-28",
			total:         "422.99",
			wantFields:    true,
		},
		{
			file:          "acme_supply_INV-2041_copy.txt",
			vendor:        "ACME SUPPLY CO.",
			customer:      "Harbor Cafe LLC",
			invoiceNumber: "INV-2041",
			date:          "2026-09-03",
			total:         "258.23",
			wantFields:    true,
		},
		{
			file:       "README_FOR_THE_PROJECT.txt",
			wantFields: false,
		},
		{
			file:          "cloudpos_subscription_oct.txt",
			vendor:        "CloudPOS Inc.",
			customer:      "Harbor Cafe LLC",
			invoiceNumber: "R-55102",
			date:          "2026-10-01",
			total:         "128.03",
			wantFields:    true,
		},
		{
			file:          "greenleaf_produce_8812.txt",
			vendor:        "Greenleaf Produce",
			customer:      "Harbor Cafe",
			invoiceNumber: "8812",
			date:          "2026-09-15",
			total:         "134.00",
			wantFields:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			text, ok := readInvoiceFixture(tc.file)
			if !ok {
				t.Fatalf("missing fixture %s", tc.file)
			}
			fields := runInvoiceText(t, text)
			if !tc.wantFields {
				if len(fields) > 0 {
					t.Fatalf("expected no fields, got %v", fields)
				}
				return
			}
			assertField(t, fields, "vendor", tc.vendor)
			assertField(t, fields, "customer", tc.customer)
			assertField(t, fields, "invoice_number", tc.invoiceNumber)
			assertField(t, fields, "date", tc.date)
			assertField(t, fields, "total", tc.total)
		})
	}
}

func TestFieldAccuracy_MedjanusOrdinaryInvoice(t *testing.T) {
	path := os.Getenv("TERMINUS_SAMPLE_INVOICE")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("medjanus ordinary invoice fixture not present")
	}
	fields := runInvoiceText(t, string(data))
	assertField(t, fields, "invoice_number", "1042")
	assertField(t, fields, "date", "2026-10-01")
	assertField(t, fields, "total", "135.00")
}

func assertField(t *testing.T, fields map[string]string, key, want string) {
	t.Helper()
	got := fields[key]
	if got != want {
		t.Errorf("field %q: got %q want %q (all fields: %v)", key, got, want, fields)
	}
}

func TestExtractCandidates_PatientNameStopsAtMRN(t *testing.T) {
	text := "Patient: REDACTED   MRN: REDACTED   DOB: REDACTED   Sex: F"
	r := docpipeline.ExtractCandidates(text, "medical")
	for _, c := range r.Candidates {
		if c.Field == "patient_name" {
			if strings.Contains(c.Value, "MRN") {
				t.Fatalf("patient_name should not include MRN label, got %q", c.Value)
			}
			if strings.TrimSpace(c.Value) != "REDACTED" {
				t.Fatalf("patient_name want REDACTED, got %q", c.Value)
			}
			return
		}
	}
	t.Fatal("patient_name candidate not found")
}

func TestExtractCandidates_DischargeSummaryFields(t *testing.T) {
	text := "Patient: REDACTED   MRN: REDACTED   DOB: REDACTED   Sex: F\n" +
		"Admitted: 03/11/26    Discharged: 03/27/26    LOS: 7 days\n" +
		"Attending: Dr. N. Larsen, DO    Service: Pulm/CCM"
	r := docpipeline.ExtractCandidates(text, "medical")
	want := map[string]string{
		"patient_name": "REDACTED",
		"admitted":     "03/11/26",
		"discharged":   "03/27/26",
		"attending":    "Dr. N. Larsen, DO",
	}
	got := make(map[string]string)
	for _, c := range r.Candidates {
		if _, ok := want[c.Field]; ok && got[c.Field] == "" {
			got[c.Field] = strings.TrimSpace(c.Value)
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %s: got %q want %q", k, got[k], v)
		}
	}
}

func TestFieldAccuracy_DueDateIsNotTheDate(t *testing.T) {
	text := "ACME SUPPLY CO.\nInvoice #: INV-2041\nBill To: Harbor Cafe LLC\nDate: 2026-09-03\nDue Date: 2026-10-03\nTotal Due: $258.23"
	fields := runInvoiceText(t, text)
	if fields["date"] != "2026-09-03" {
		t.Fatalf("date = %q, want 2026-09-03 (the due date was taken as the date)", fields["date"])
	}
	if fields["due_date"] != "2026-10-03" {
		t.Fatalf("due_date = %q, want 2026-10-03", fields["due_date"])
	}
}
// Many invoices print each label on one line and its value on the next, under a title line.
func TestFieldAccuracy_LabelAboveValue(t *testing.T) {
	text := "Harbor Light Printing Co.\n\n\n14 Wharf Road, Portland, ME 04101\n\n\nINVOICE\n\n\nInvoice #\n\nHLP-20418\n\nDate:\n\nOctober 3, 2026\n\nDue Date:\n\nNovember 2, 2026\n\n\nBill To:\n\n\nMaple Street Bakery\n\n220 Maple Street\n\nPortland, ME 04102\n\n\nDescription\n\nQty\n\nAmount\n\nSubtotal\n\n$410.00\n\nTax (7%)\n\n$28.70\n\n\nTotal Due\n\n$438.70\n"
	fields := runInvoiceText(t, text)
	want := map[string]string{
		"vendor":         "Harbor Light Printing Co.",
		"customer":       "Maple Street Bakery",
		"invoice_number": "HLP-20418",
		"date":           "2026-10-03",
		"due_date":       "2026-11-02",
		"total":          "438.70",
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %q, want %q", k, fields[k], v)
		}
	}
}

func TestFieldAccuracy_ShortMonthAndSlashDueDate(t *testing.T) {
	fields := runInvoiceText(t, "Blue Kettle Catering\nInvoice No.\n1187\nDate:\nOct 28, 2026\nPayment Due:\n11/27/2026\nBalance Due\n$463.31\n")
	if fields["date"] != "2026-10-28" || fields["due_date"] != "2026-11-27" || fields["invoice_number"] != "1187" {
		t.Fatalf("got date=%q due_date=%q invoice_number=%q", fields["date"], fields["due_date"], fields["invoice_number"])
	}
}
