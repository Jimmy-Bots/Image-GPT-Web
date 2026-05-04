package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-image-web/internal/api"
	"gpt-image-web/internal/config"
	"gpt-image-web/internal/storage"
)

func TestChatCompletionsStreamsSSE(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"object":"chat.completion.chunk"`) || !strings.Contains(body, `"content":"hel"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("unexpected chat stream body: %s", body)
	}
}

func TestResponsesStreamsNamedEvents(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"auto","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: response.created") || !strings.Contains(body, "event: response.output_text.delta") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("unexpected response stream body: %s", body)
	}
}

func newTestServer(t *testing.T, upstream api.Upstream) (*api.Server, func()) {
	t.Helper()
	store, err := storage.Open(context.Background(), t.TempDir()+"/app.db", 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := config.Config{
		Addr:                    "127.0.0.1:0",
		AppVersion:              "test",
		LegacyAdminKey:          "dev-key",
		AdminEmail:              "admin@example.com",
		AdminPassword:           "password123",
		SessionSecret:           "secret",
		SessionTTLHours:         24,
		ImageWorkerCount:        1,
		ImageQueueSize:          4,
		ImageAccountConcurrency: 1,
	}
	server, err := api.NewServer(cfg, store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server.SetUpstream(upstream)
	cleanup := func() {
		server.Close()
		_ = store.Close()
	}
	return server, cleanup
}

type fakeStreamUpstream struct{}

func (fakeStreamUpstream) ListModels(ctx context.Context) (map[string]any, error) {
	return map[string]any{"object": "list", "data": []any{}}, nil
}

func (fakeStreamUpstream) GenerateImage(ctx context.Context, req api.ImageGenerationPayload) (map[string]any, error) {
	return map[string]any{"data": []any{}}, nil
}

func (fakeStreamUpstream) EditImage(ctx context.Context, req api.ImageEditPayload) (map[string]any, error) {
	return map[string]any{"data": []any{}}, nil
}

func (fakeStreamUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (fakeStreamUpstream) StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	if err := onEvent(map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": int64(1),
		"model":   "auto",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": "hel"}, "finish_reason": nil}},
	}); err != nil {
		return err
	}
	return onEvent(map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": int64(1),
		"model":   "auto",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	})
}

func (fakeStreamUpstream) Responses(ctx context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (fakeStreamUpstream) StreamResponses(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	if err := onEvent(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_test"}}); err != nil {
		return err
	}
	return onEvent(map[string]any{"type": "response.output_text.delta", "delta": "hi"})
}

func (fakeStreamUpstream) AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, api.ErrUpstreamNotImplemented
}

func (fakeStreamUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	return 0, nil
}
