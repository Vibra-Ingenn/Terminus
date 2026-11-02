package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUI_IndexPage(t *testing.T) {
	mux := configureHTTPServer(serveConfig{port: 8765})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Terminus") {
		t.Fatal("expected Terminus branding on home page")
	}
	if !strings.Contains(body, "Drop receipts") {
		t.Fatal("expected drop zone copy")
	}
}

func TestToolCard_ServedAndValid(t *testing.T) {
	mux := configureHTTPServer(serveConfig{})
	req := httptest.NewRequest(http.MethodGet, "/tool.json", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var card struct {
		Spec    string `json:"spec"`
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Version string `json:"version"`
		Actions []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("tool.json is not JSON: %v", err)
	}
	if card.Spec != "tool-card/0.1" || card.ID != toolID || card.Kind != "service" || card.Version != toolVersion {
		t.Fatalf("unexpected card header: %+v", card)
	}
	if len(card.Actions) == 0 || card.Actions[0].Path != "/v1/parse" {
		t.Fatalf("expected the parse action first: %+v", card.Actions)
	}
}

func TestHealth_TerminusService(t *testing.T) {
	mux := configureHTTPServer(serveConfig{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "terminus") {
		t.Fatalf("unexpected health body: %s", rec.Body.String())
	}
}
