package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-image-web/internal/api"
)

func TestSensitiveWordsRejectUpstreamCalls(t *testing.T) {
	server, cleanup := newTestServer(t, fakeContentUpstream{})
	defer cleanup()
	saveSettings(t, server, map[string]any{"sensitive_words": []string{"blocked"}})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"blocked text"}]}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected sensitive word rejection, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "content_rejected") {
		t.Fatalf("unexpected rejection body: %s", rec.Body.String())
	}
}

func TestAIReviewAllowAndReject(t *testing.T) {
	reviewText := "ALLOW"
	reviewServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected review path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer review-key" {
			t.Fatalf("missing review auth: %q", r.Header.Get("Authorization"))
		}
		writeTestJSON(w, map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": reviewText}}}})
	}))
	defer reviewServer.Close()

	server, cleanup := newTestServer(t, fakeContentUpstream{})
	defer cleanup()
	saveSettings(t, server, map[string]any{"ai_review": map[string]any{"enabled": true, "base_url": reviewServer.URL, "api_key": "review-key", "model": "review-model"}})

	rec := callComplete(t, server, "hello")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected allow, got %d body=%s", rec.Code, rec.Body.String())
	}

	reviewText = "REJECT"
	rec = callComplete(t, server, "hello again")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected reject, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func callComplete(t *testing.T, server *api.Server, prompt string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"model": "auto", "prompt": prompt})
	req := httptest.NewRequest(http.MethodPost, "/v1/complete", strings.NewReader(string(payload)))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	return rec
}

func saveSettings(t *testing.T, server *api.Server, settings map[string]any) {
	t.Helper()
	payload, _ := json.Marshal(settings)
	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(string(payload)))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save settings failed: %d body=%s", rec.Code, rec.Body.String())
	}
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type fakeContentUpstream struct {
	fakeStreamUpstream
}

func (fakeContentUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func (fakeContentUpstream) StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return onEvent(map[string]any{"ok": true})
}

var _ api.Upstream = fakeContentUpstream{}
