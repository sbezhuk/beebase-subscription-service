package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	HealthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	expected := `{"status":"ok"}`
	if rec.Body.String() != expected && rec.Body.String() != expected+"\n" {
		t.Fatalf("expected body %q, got %q", expected, rec.Body.String())
	}
}
