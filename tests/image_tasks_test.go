package tests

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-image-web/internal/api"
)

func TestImageTaskGenerationDefaultsSize(t *testing.T) {
	upstream := &taskCaptureUpstream{requests: make(chan api.ImageGenerationPayload, 1)}
	server, cleanup := newTestServer(t, upstream)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"task-default-size","prompt":"draw","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create task failed: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"size":"1:1"`) {
		t.Fatalf("task response missing default size: %s", rec.Body.String())
	}

	select {
	case payload := <-upstream.requests:
		if payload.Size != "1:1" {
			t.Fatalf("upstream size = %q, want 1:1", payload.Size)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async image task")
	}
}

func TestImageTaskGenerationUsesContentPolicy(t *testing.T) {
	server, cleanup := newTestServer(t, fakeContentUpstream{})
	defer cleanup()
	saveSettings(t, server, map[string]any{"sensitive_words": []string{"blocked"}})

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"task-rejected","prompt":"blocked text","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected content rejection, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "content_rejected") {
		t.Fatalf("unexpected rejection body: %s", rec.Body.String())
	}
}

type taskCaptureUpstream struct {
	fakeStreamUpstream
	requests chan api.ImageGenerationPayload
}

func (u *taskCaptureUpstream) GenerateImage(ctx context.Context, req api.ImageGenerationPayload) (map[string]any, error) {
	u.requests <- req
	return map[string]any{
		"created": int64(1),
		"data": []map[string]any{{
			"b64_json":       base64.StdEncoding.EncodeToString([]byte("png-ish")),
			"revised_prompt": "draw",
		}},
	}, nil
}

var _ api.Upstream = (*taskCaptureUpstream)(nil)
