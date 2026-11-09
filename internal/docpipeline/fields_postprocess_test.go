package docpipeline

import "testing"

func TestPostProcessFields_DropsReportVendor(t *testing.T) {
	m := map[string]string{
		"vendor": "DIAGNOSTIC IMAGING REPORT  .",
		"mrn":    "X1",
	}
	PostProcessFields(m)
	if _, ok := m["vendor"]; ok {
		t.Fatalf("vendor should be dropped for report, got %v", m)
	}
}

func TestPostProcessFields_KeepsBillVendor(t *testing.T) {
	m := map[string]string{
		"vendor":         "ACME SUPPLY CO.",
		"invoice_number": "INV-1",
		"total":          "10.00",
	}
	PostProcessFields(m)
	if m["vendor"] != "ACME SUPPLY CO." {
		t.Fatalf("vendor kept: %v", m)
	}
}

func TestNormalizeDateField(t *testing.T) {
	m := map[string]string{"date": "October 1, 2026"}
	PostProcessFields(m)
	if m["date"] != "2026-10-01" {
		t.Fatalf("date %q", m["date"])
	}
	if m["date_on_document"] != "October 1, 2026" {
		t.Fatalf("original %q", m["date_on_document"])
	}
}

func TestNormalizeDateStringImpossibleAndDayFirst(t *testing.T) {
	cases := []struct{ in, want string }{
		{"09/15/2026", "2026-09-15"},
		{"15/09/2026", "2026-09-15"}, // only day/month/year makes sense
		{"99/15/2026", "99/15/2026"}, // misread: kept, not rolled into 2034
		{"02/30/2026", "02/30/2026"}, // February 30th does not exist
	}
	for _, c := range cases {
		got, warn := normalizeDateString(c.in)
		if got != c.want {
			t.Errorf("normalizeDateString(%q) = %q, want %q", c.in, got, c.want)
		}
		if got == c.in && warn == "" {
			t.Errorf("normalizeDateString(%q): impossible date gave no warning", c.in)
		}
	}
}
