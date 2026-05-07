package integration

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-image-web/internal/api"
)

func TestImageGenerationPersistsLocalArchive(t *testing.T) {
	server, cleanup := newTestServer(t, imageArchiveUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"draw","model":"gpt-image-2","response_format":"b64_json"}`))
	req.Host = "example.test"
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("generation failed: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"url":"http://example.test/images/`) {
		t.Fatalf("response missing local image URL: %s", rec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/images", nil)
	listReq.Host = "example.test"
	listReq.Header.Set("Authorization", "Bearer dev-key")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list failed: %d body=%s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	if !strings.Contains(body, `"items":[`) || !strings.Contains(body, `/images/`) {
		t.Fatalf("unexpected image list: %s", body)
	}
	path := strings.Split(strings.Split(body, `"path":"`)[1], `"`)[0]

	deleteReq := httptest.NewRequest(http.MethodPost, "/api/images/delete", strings.NewReader(`{"paths":["`+path+`"]}`))
	deleteReq.Header.Set("Authorization", "Bearer dev-key")
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK || !strings.Contains(deleteRec.Body.String(), `"removed":1`) {
		t.Fatalf("delete failed: %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	assertLogContains(t, server, "image", "删除归档图片")
}

func TestImageGenerationURLResponseUsesLocalArchive(t *testing.T) {
	upstream := &formatCaptureUpstream{}
	server, cleanup := newTestServer(t, upstream)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"draw","model":"gpt-image-2","response_format":"url"}`))
	req.Host = "example.test"
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("generation failed: %d body=%s", rec.Code, rec.Body.String())
	}
	if upstream.format != "b64_json" {
		t.Fatalf("upstream format = %q, want b64_json", upstream.format)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"url":"http://example.test/images/`) {
		t.Fatalf("response missing local image URL: %s", body)
	}
	if strings.Contains(body, `"b64_json"`) {
		t.Fatalf("url response leaked b64_json: %s", body)
	}

	tasksReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks", nil)
	tasksReq.Header.Set("Authorization", "Bearer dev-key")
	tasksRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(tasksRec, tasksReq)
	if tasksRec.Code != http.StatusOK {
		t.Fatalf("list tasks failed: %d body=%s", tasksRec.Code, tasksRec.Body.String())
	}
	if !strings.Contains(tasksRec.Body.String(), `"mode":"generate"`) {
		t.Fatalf("sync image call should create an auditable task: %s", tasksRec.Body.String())
	}
	taskID := strings.Split(strings.Split(tasksRec.Body.String(), `"id":"`)[1], `"`)[0]
	eventReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks/"+taskID+"/events", nil)
	eventReq.Header.Set("Authorization", "Bearer dev-key")
	eventRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(eventRec, eventReq)
	if eventRec.Code != http.StatusOK {
		t.Fatalf("list sync task events failed: %d body=%s", eventRec.Code, eventRec.Body.String())
	}
	if !strings.Contains(eventRec.Body.String(), `"type":"submitted"`) || !strings.Contains(eventRec.Body.String(), `"type":"success"`) {
		t.Fatalf("sync image task events missing lifecycle markers: %s", eventRec.Body.String())
	}
}

type imageArchiveUpstream struct {
	fakeStreamUpstream
}

func (imageArchiveUpstream) GenerateImage(ctx context.Context, req api.ImageGenerationPayload) (map[string]any, error) {
	return map[string]any{
		"created": int64(1),
		"data": []map[string]any{{
			"b64_json":       base64.StdEncoding.EncodeToString([]byte("png-ish")),
			"revised_prompt": "draw",
		}},
	}, nil
}

var _ api.Upstream = imageArchiveUpstream{}

type formatCaptureUpstream struct {
	imageArchiveUpstream
	format string
}

func (u *formatCaptureUpstream) GenerateImage(ctx context.Context, req api.ImageGenerationPayload) (map[string]any, error) {
	u.format = req.ResponseFormat
	return u.imageArchiveUpstream.GenerateImage(ctx, req)
}
