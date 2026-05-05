package tests

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSIsSameOriginByDefault(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected default allowed origin: %q", got)
	}
}

func TestSecurityHeadersAreApplied(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("missing strict CSP: %q", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("unexpected nosniff header: %q", got)
	}
}

func TestLoginRateLimit(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	for i := 0; i < 8; i++ {
		rec := loginWithWrongPassword(t, server.Routes())
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected unauthorized, got %d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	rec := loginWithWrongPassword(t, server.Routes())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected rate limited, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func loginWithWrongPassword(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"admin@example.com","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
