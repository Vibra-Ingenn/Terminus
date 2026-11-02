package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The test bills as PDFs (scripts/gen_bill_test_pdfs.py): plain, with no space characters (how
// accounting programs write them), with all text inside a form, and locked without a password.
// Every way must give the same fields.
func TestBillPDFsGiveTheSameFields(t *testing.T) {
	dir := filepath.Join("testdata", "bills")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("run scripts/gen_bill_test_pdfs.py to create the bill fixtures")
	}
	want := map[string]map[string]string{
		"acme_supply_INV-2041":        {"invoice_number": "INV-2041", "total": "258.23", "date": "2026-09-03"},
		"brightline_electric_2026-09": {"invoice_number": "BL-778120", "total": "422.99", "date": "2026-09-28"},
		"cloudpos_subscription_oct":   {"invoice_number": "R-55102", "total": "128.03", "date": "2026-10-01"},
		"greenleaf_produce_8812":      {"invoice_number": "8812", "total": "134.00", "date": "2026-09-15"},
	}
	r := NewRegistry()
	for bill, fields := range want {
		for _, way := range []string{"plain", "nospace", "form", "locked"} {
			path := filepath.Join(dir, bill+"_"+way+".pdf")
			res := r.Dispatch(ToolCall{Name: "document_parse", Arguments: map[string]any{"path": path}})
			if !res.Success {
				t.Errorf("%s_%s: %s", bill, way, res.Error)
				continue
			}
			var out documentParseOutput
			if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
				t.Fatalf("%s_%s: %v", bill, way, err)
			}
			for k, v := range fields {
				if out.Fields[k] != v {
					t.Errorf("%s_%s: %s = %q, want %q", bill, way, k, out.Fields[k], v)
				}
			}
		}
	}
}
