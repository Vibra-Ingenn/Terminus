package docpipeline

import (
	"strings"
	"testing"
)

func TestRepairOCRTextFixesLabels(t *testing.T) {
	cases := []struct{ in, want string }{
		{"TOTAL OVE $258.23", "TOTAL DUE $258.23"},
		{"Total 0ue: $12.00", "Total due: $12.00"},
		{"Amount DLE $40.00", "Amount DUE $40.00"},
		{"Bilt to: Harbor Cafe", "Bill to: Harbor Cafe"},
		{"invorwe inv-2042", "invoice inv-2042"},
		{"INVOlCE # 8812", "INVOICE # 8812"},
		{"suntotal $238.00", "subtotal $238.00"},
		{"Receipl no. 4471", "Receipt no. 4471"},
		{"Dste: 2026-09-03", "Date: 2026-09-03"},
		{"Custonner: Maple Bakery", "Customer: Maple Bakery"},
		{"Lids x4 $38. sO", "Lids x4 $38.50"},
		{"Paper cups $l54.0O", "Paper cups $154.00"},
	}
	for _, c := range cases {
		got, n := RepairOCRText(c.in)
		if got != c.want {
			t.Errorf("RepairOCRText(%q) = %q, want %q", c.in, got, c.want)
		}
		if n == 0 {
			t.Errorf("RepairOCRText(%q) reported no fixes", c.in)
		}
	}
}

// Ordinary words, plurals and names must come through untouched.
func TestRepairOCRTextLeavesRealWords(t *testing.T) {
	keep := []string{
		"Rate: $45.00 per hour",
		"Late fee $5.00",
		"Pay all invoices by Friday",
		"Monthly totals: $300.00",
		"Data plan 5 GB",
		"Sold out",
		"The total one is fine",
		"Harbor cate LLC",
		"Gate 12, Terminal B",
		"Built in 1998",
		"Bell Street Bakery",
		"Hold until 2026-10-01",
		"Sales tax 8.5%",
		"Thank you for your business",
	}
	for _, in := range keep {
		got, n := RepairOCRText(in)
		if got != in || n != 0 {
			t.Errorf("RepairOCRText(%q) = %q (%d fixes), want it unchanged", in, got, n)
		}
	}
}

// End to end: a badly read phone photo of an invoice now gives its fields.
func TestRoughPhotoInvoiceFields(t *testing.T) {
	ocr := strings.Join([]string{
		"ACME guppLy CO",
		"invorwe inv-2042",
		"Date 2026-09-03",
		"Bilt to: Harbor cate LLC",
		"Paper cups 4202 (case) x4 $38. sO $154 oo",
		"suntotal $238.00",
		"Tax 8.5% $20.23",
		"TOTAL OVE $258.23",
		"payment due 2026-10-03",
	}, "\n")

	before, _ := Run(ocr)
	if before.ToFieldMap(true)["total"] != "" {
		t.Fatalf("expected the unrepaired text to miss the total (test is stale)")
	}

	repaired, _ := RepairOCRText(ocr)
	pr, _ := Run(repaired)
	f := pr.ToFieldMap(true)
	want := map[string]string{
		"total":          "258.23",
		"invoice_number": "inv-2042",
		"customer":       "Harbor cate LLC",
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("field %s = %q, want %q (all fields: %v)", k, f[k], v, f)
		}
	}
	if !strings.Contains(f["date"], "2026-09-03") {
		t.Errorf("date = %q, want 2026-09-03", f["date"])
	}
	if !strings.Contains(f["due_date"], "2026-10-03") {
		t.Errorf("due_date = %q, want 2026-10-03", f["due_date"])
	}
}

// A bare date that only looks like a birth date must not raise an overlap
// warning against the labelled invoice date.
func TestNoOverlapWarningForWeakGuess(t *testing.T) {
	_, vr := Run("Greenleaf Produce\nInvoice 8812\nDate: 09/15/2026\nTotal due $134.00\n")
	for _, c := range vr.Conflicts {
		if strings.Contains(c, "span overlap") && strings.Contains(c, "(dob)") {
			t.Errorf("unexpected overlap warning: %s", c)
		}
	}
}

func TestRepairOCRTextTrimsSpecksAndSpacedCents(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Greenieal Produce :", "Greenieal Produce"},
		{"Total: 134.00 (no tax on produce) =", "Total: 134.00 (no tax on produce)"},
		{"= Lemons, 40 lb box", "Lemons, 40 lb box"},
		{"Amount due: $422 99 by 2026-10-20", "Amount due: $422.99 by 2026-10-20"},
		{"Tota’. 134.00 (no tax on produce}", "Total: 134.00 (no tax on produce}"},
		{"Date, 2026-09-03", "Date: 2026-09-03"},
	}
	for _, c := range cases {
		if got, _ := RepairOCRText(c.in); got != c.want {
			t.Errorf("RepairOCRText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, keep := range []string{"Invoice #", "Page 1 of 2", "a 10% fee", "Qty 4", "Bill to:", "CloudPOS Inc. 2026", "Thanks, 5 stars"} {
		if got, _ := RepairOCRText(keep); got != keep {
			t.Errorf("RepairOCRText(%q) = %q, want it unchanged", keep, got)
		}
	}
}

func TestCheckTotalArithmetic(t *testing.T) {
	receipt := "Pro plan $89.00\nExtra terminal $29.00\nSales tax $10.03\nTotal charged to card $%s\n"
	if w := CheckTotalArithmetic(strings.Replace(receipt, "%s", "128.03", 1), "128.03"); w != "" {
		t.Errorf("correct total flagged: %s", w)
	}
	w := CheckTotalArithmetic(strings.Replace(receipt, "%s", "328.03", 1), "328.03")
	if !strings.Contains(w, "128.03") {
		t.Errorf("misread total not caught, got %q", w)
	}
	invoice := "Paper cups x4 $38.50 $154.00\nLids x4 $21.00 $84.00\nSubtotal $238.00\nTax 8.5% $20.23\nTOTAL DUE $258.23\n"
	if w := CheckTotalArithmetic(invoice, "258.23"); w != "" {
		t.Errorf("correct invoice flagged: %s", w)
	}
	if w := CheckTotalArithmetic(strings.Replace(invoice, "$258.23", "$253.23", 1), "253.23"); !strings.Contains(w, "258.23") {
		t.Errorf("misread invoice total not caught, got %q", w)
	}
	// Nothing to check against: stay quiet.
	if w := CheckTotalArithmetic("Total: 134.00\n", "134.00"); w != "" {
		t.Errorf("unexpected warning: %s", w)
	}
}
