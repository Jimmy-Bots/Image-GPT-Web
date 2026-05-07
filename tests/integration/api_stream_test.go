package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestAnthropicMessagesReturnsJSON(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"message"`) || !strings.Contains(body, `"text":"hello"`) {
		t.Fatalf("unexpected anthropic body: %s", body)
	}
}

func TestAnthropicMessagesStreamsNamedEvents(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: message_start") || !strings.Contains(body, "event: content_block_delta") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("unexpected anthropic stream body: %s", body)
	}
}
