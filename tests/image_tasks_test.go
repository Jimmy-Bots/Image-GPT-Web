package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	if !strings.Contains(rec.Body.String(), `"size":"auto"`) {
		t.Fatalf("task response missing auto size: %s", rec.Body.String())
	}

	select {
	case payload := <-upstream.requests:
		if payload.Size != "auto" {
			t.Fatalf("upstream size = %q, want auto", payload.Size)
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

func TestImageTaskGenerationRetriesTransientFailure(t *testing.T) {
	upstream := &retryTaskUpstream{}
	server, cleanup := newTestServer(t, upstream)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"task-retry","prompt":"draw","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create task failed: %d body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for retry")
		default:
		}
		if upstream.calls.Load() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestImageTaskListUsesLightweightRowsAndDetailByID(t *testing.T) {
	upstream := &taskCaptureUpstream{requests: make(chan api.ImageGenerationPayload, 1)}
	server, cleanup := newTestServer(t, upstream)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"task-detail","prompt":"detail prompt","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create task failed: %d body=%s", rec.Code, rec.Body.String())
	}

	select {
	case <-upstream.requests:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async image task")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks", nil)
	listReq.Header.Set("Authorization", "Bearer dev-key")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list tasks failed: %d body=%s", listRec.Code, listRec.Body.String())
	}
	listBody := listRec.Body.String()
	if !strings.Contains(listBody, `"prompt":"detail prompt"`) {
		t.Fatalf("list response missing prompt: %s", listBody)
	}
	if strings.Contains(listBody, `"data"`) {
		t.Fatalf("list response should not include task data: %s", listBody)
	}

	detail := waitForTaskDetail(t, server, "task-detail")
	if !strings.Contains(detail, `"data"`) || !strings.Contains(detail, `"url":"/images/`) {
		t.Fatalf("detail response missing archived data: %s", detail)
	}
}

func TestImageTasksCanBeDeleted(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"task-delete","prompt":"delete prompt","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create task failed: %d body=%s", rec.Code, rec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodPost, "/api/image-tasks/delete", strings.NewReader(`{"ids":["task-delete"]}`))
	deleteReq.Header.Set("Authorization", "Bearer dev-key")
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK || !strings.Contains(deleteRec.Body.String(), `"removed":1`) {
		t.Fatalf("delete task failed: %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids=task-delete", nil)
	detailReq.Header.Set("Authorization", "Bearer dev-key")
	detailRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("list after delete failed: %d body=%s", detailRec.Code, detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), `"missing_ids":["task-delete"]`) {
		t.Fatalf("deleted task should be missing: %s", detailRec.Body.String())
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

type retryTaskUpstream struct {
	fakeStreamUpstream
	calls atomic.Int64
}

func (u *retryTaskUpstream) GenerateImage(ctx context.Context, req api.ImageGenerationPayload) (map[string]any, error) {
	if u.calls.Add(1) == 1 {
		return nil, errors.New("temporary upstream failure")
	}
	return map[string]any{
		"created": int64(1),
		"data": []map[string]any{{
			"b64_json":       base64.StdEncoding.EncodeToString([]byte("png-ish")),
			"revised_prompt": "draw",
		}},
	}, nil
}

var _ api.Upstream = (*retryTaskUpstream)(nil)

func waitForTaskDetail(t *testing.T, server *api.Server, id string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		req := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids="+id, nil)
		req.Header.Set("Authorization", "Bearer dev-key")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("detail failed: %d body=%s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Items []struct {
				Status string           `json:"status"`
				Data   *json.RawMessage `json:"data,omitempty"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode detail: %v body=%s", err, rec.Body.String())
		}
		if len(payload.Items) > 0 && payload.Items[0].Status == "success" && payload.Items[0].Data != nil {
			return rec.Body.String()
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for detail: %s", rec.Body.String())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
