package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-image-web/internal/api"
)

func assertLogContains(t *testing.T, server *api.Server, logType string, text string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?type="+logType+"&detail=true", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminAPIKey)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list %s logs failed: %d body=%s", logType, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), text) {
		t.Fatalf("%s log missing %q: %s", logType, text, rec.Body.String())
	}
}
