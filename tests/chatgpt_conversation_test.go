package tests

import (
	"context"
	"io"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"

	"gpt-image-web/internal/upstream/chatgpt"
)

func TestChatGPTCollectTextFromAssistantMessageSSE(t *testing.T) {
	input := `data: {"message":{"author":{"role":"assistant"},"content":{"parts":["hello"]}}}

data: [DONE]

`
	got := collectTextFromSSE(t, input)
	if got != "hello" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestChatGPTCollectTextFromPatchAppendSSE(t *testing.T) {
	input := `data: {"p":"/message/content/parts/0","o":"append","v":"hel"}
data: {"p":"/message/content/parts/0","o":"append","v":"lo"}
data: [DONE]
`
	got := collectTextFromSSE(t, input)
	if got != "hello" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func collectTextFromSSE(t *testing.T, sse string) string {
	t.Helper()
	client := chatgpt.NewClient("token", chatgpt.WithHTTPClient(conversationDoer{t: t, sse: sse}))
	text, err := client.CollectText(context.Background(), []chatgpt.Message{{Role: "user", Content: "hi"}}, "auto")
	if err != nil {
		t.Fatalf("CollectText returned error: %v", err)
	}
	return text
}

type conversationDoer struct {
	t   *testing.T
	sse string
}

func (d conversationDoer) Do(req *fhttp.Request) (*fhttp.Response, error) {
	if req.URL.Path != "/" && req.Header.Get("Authorization") != "Bearer token" {
		d.t.Fatalf("missing authorization header for %s: %q", req.URL.Path, req.Header.Get("Authorization"))
	}
	switch req.URL.Path {
	case "/":
		return response(200, `<html data-build="fallback"><script src="/_next/static/chunks/c/abc/_build.js"></script></html>`), nil
	case "/backend-api/sentinel/chat-requirements":
		body, err := io.ReadAll(req.Body)
		if err != nil {
			d.t.Fatalf("read requirements body: %v", err)
		}
		if !strings.Contains(string(body), `"p"`) {
			d.t.Fatalf("missing proof-of-work payload: %s", string(body))
		}
		return response(200, `{"token":"requirements-token"}`), nil
	case "/backend-api/conversation":
		if req.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token") != "requirements-token" {
			d.t.Fatalf("missing requirements token: %q", req.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token"))
		}
		return response(200, d.sse), nil
	default:
		d.t.Fatalf("unexpected request path: %s", req.URL.Path)
		return response(404, `{}`), nil
	}
}

func response(status int, body string) *fhttp.Response {
	return &fhttp.Response{
		StatusCode: status,
		Header:     make(fhttp.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
