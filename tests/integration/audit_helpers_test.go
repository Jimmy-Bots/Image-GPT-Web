package integration

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gpt-image-web/internal/api"
)

func assertLogContains(t *testing.T, server *api.Server, logType string, text string) {
	t.Helper()
	assertLogQueryContains(t, server, map[string]string{"type": logType}, text)
}

func assertLogQueryContains(t *testing.T, server *api.Server, filters map[string]string, text string) {
	t.Helper()
	values := url.Values{}
	for key, value := range filters {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	values.Set("detail", "true")
	req := httptest.NewRequest(http.MethodGet, "/api/logs?"+values.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+testAdminAPIKey)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list logs failed: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), text) {
		t.Fatalf("log missing %q with filters %#v: %s", text, filters, rec.Body.String())
	}
}
