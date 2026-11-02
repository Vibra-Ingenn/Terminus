package docpipeline

import "testing"

func TestClassifyDocument_BankStatement(t *testing.T) {
	raw := "BANK OF CASCADIA STATEMENT\nOpening Balance: $12,450.67\nClosing Balance: $21,871.26"
	m := ClassifyDocument(raw, map[string]string{}, false)
	if m["document_type"] != "bank_statement" {
		t.Fatalf("type %q", m["document_type"])
	}
	if m["opening_balance"] != "12,450.67" || m["closing_balance"] != "21,871.26" {
		t.Fatalf("balances %v", m)
	}
}

func TestPostProcessMedicalDates(t *testing.T) {
	m := map[string]string{"admitted": "03/11/26", "discharged": "03/27/26"}
	PostProcessFields(m)
	if m["admitted"] != "2026-03-11" || m["admitted_on_document"] != "03/11/26" {
		t.Fatalf("admitted %v", m)
	}
	if m["discharged"] != "2026-03-27" {
		t.Fatalf("discharged %v", m)
	}
}
