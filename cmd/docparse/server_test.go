package main

import (
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

func TestHealth_TerminusService(t *testing.T) {
	mux := configureHTTPServer(serveConfig{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "terminus") {
		t.Fatalf("unexpected health body: %s", rec.Body.String())
	}
}
