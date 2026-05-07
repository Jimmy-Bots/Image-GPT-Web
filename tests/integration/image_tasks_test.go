package integration

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

func TestImageTaskEventsFollowLifecycle(t *testing.T) {
	upstream := &taskCaptureUpstream{requests: make(chan api.ImageGenerationPayload, 1)}
	server, cleanup := newTestServer(t, upstream)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"task-events","prompt":"event prompt","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create task failed: %d body=%s", rec.Code, rec.Body.String())
	}

	waitForTaskDetail(t, server, "task-events")

	eventReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks/task-events/events", nil)
	eventReq.Header.Set("Authorization", "Bearer dev-key")
	eventRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(eventRec, eventReq)
	if eventRec.Code != http.StatusOK {
		t.Fatalf("list task events failed: %d body=%s", eventRec.Code, eventRec.Body.String())
	}
	var payload struct {
		Items []struct {
			Type    string           `json:"type"`
			Summary string           `json:"summary"`
			Detail  *json.RawMessage `json:"detail,omitempty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(eventRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode task events: %v body=%s", err, eventRec.Body.String())
	}
	seen := map[string]bool{}
	for _, item := range payload.Items {
		seen[item.Type] = true
		if item.Detail == nil {
			t.Fatalf("event %q missing detail", item.Type)
		}
	}
	for _, eventType := range []string{"submitted", "queued", "processing", "success"} {
		if !seen[eventType] {
			t.Fatalf("missing task event %q: %#v", eventType, payload.Items)
		}
	}
	for _, summary := range []string{"图片任务已提交", "任务进入等待队列", "开始处理图片任务", "图片任务成功"} {
		assertLogQueryContains(t, server, map[string]string{
			"type":    "task",
			"task_id": "task-events",
			"query":   summary,
		}, summary)
	}
	assertLogQueryContains(t, server, map[string]string{
		"type":     "task",
		"task_id":  "task-events",
		"endpoint": "/api/image-tasks/generations",
		"status":   "success",
	}, "event prompt")

	statusBeforeSuccess := ""
	for _, item := range payload.Items {
		if item.Type != "success" || item.Detail == nil {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal(*item.Detail, &detail); err != nil {
			t.Fatalf("decode success event detail: %v", err)
		}
		if value, ok := detail["task_status_before_event"].(string); ok {
			statusBeforeSuccess = value
		}
	}
	if statusBeforeSuccess != "success" {
		t.Fatalf("success event should be written after task row reaches success, got %q", statusBeforeSuccess)
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

	includeDeletedReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks?deleted=only&mode=generate&model=gpt-image-2&date_from=2000-01-01&query=task-delete", nil)
	includeDeletedReq.Header.Set("Authorization", "Bearer dev-key")
	includeDeletedRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(includeDeletedRec, includeDeletedReq)
	if includeDeletedRec.Code != http.StatusOK {
		t.Fatalf("list including deleted failed: %d body=%s", includeDeletedRec.Code, includeDeletedRec.Body.String())
	}
	if !strings.Contains(includeDeletedRec.Body.String(), `"id":"task-delete"`) || !strings.Contains(includeDeletedRec.Body.String(), `"deleted_at"`) {
		t.Fatalf("soft-deleted task should be available for audit: %s", includeDeletedRec.Body.String())
	}

	eventReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks/task-delete/events", nil)
	eventReq.Header.Set("Authorization", "Bearer dev-key")
	eventRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(eventRec, eventReq)
	if eventRec.Code != http.StatusOK {
		t.Fatalf("deleted task events should be retained: %d body=%s", eventRec.Code, eventRec.Body.String())
	}
	if !strings.Contains(eventRec.Body.String(), `"type":"deleted"`) {
		t.Fatalf("deleted task events should include deletion marker: %s", eventRec.Body.String())
	}

	logReq := httptest.NewRequest(http.MethodGet, "/api/logs?type=task&detail=true", nil)
	logReq.Header.Set("Authorization", "Bearer dev-key")
	logRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(logRec, logReq)
	if logRec.Code != http.StatusOK {
		t.Fatalf("task audit log should be readable: %d body=%s", logRec.Code, logRec.Body.String())
	}
	if !strings.Contains(logRec.Body.String(), `"图片任务删除"`) {
		t.Fatalf("task deletion audit log missing: %s", logRec.Body.String())
	}
	assertLogQueryContains(t, server, map[string]string{
		"type":    "task",
		"task_id": "task-delete",
		"query":   "图片任务删除",
	}, "图片任务删除")
}

func TestAdminCanListUserWorkbenchTasks(t *testing.T) {
	upstream := &taskCaptureUpstream{requests: make(chan api.ImageGenerationPayload, 1)}
	server, cleanup := newTestServer(t, upstream)
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"task-user@example.com","name":"Task User","password":"password123","role":"user"}`))
	createReq.Header.Set("Authorization", "Bearer dev-key")
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create user failed: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"user-task-visible","prompt":"visible prompt","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer "+created.Key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create user task failed: %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-upstream.requests:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for user task")
	}

	adminListReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks?owner_id=all&query=user-task-visible", nil)
	adminListReq.Header.Set("Authorization", "Bearer dev-key")
	adminListRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminListRec, adminListReq)
	if adminListRec.Code != http.StatusOK {
		t.Fatalf("admin list tasks failed: %d body=%s", adminListRec.Code, adminListRec.Body.String())
	}
	if !strings.Contains(adminListRec.Body.String(), `"id":"user-task-visible"`) || !strings.Contains(adminListRec.Body.String(), `"owner_id":"`+created.Item.ID+`"`) {
		t.Fatalf("admin should see user task with owner id: %s", adminListRec.Body.String())
	}

	adminDetailReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids=user-task-visible&owner_id="+created.Item.ID, nil)
	adminDetailReq.Header.Set("Authorization", "Bearer dev-key")
	adminDetailRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminDetailRec, adminDetailReq)
	if adminDetailRec.Code != http.StatusOK || !strings.Contains(adminDetailRec.Body.String(), `"id":"user-task-visible"`) {
		t.Fatalf("admin detail should read user task: %d body=%s", adminDetailRec.Code, adminDetailRec.Body.String())
	}

	adminEventsReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks/user-task-visible/events?owner_id="+created.Item.ID, nil)
	adminEventsReq.Header.Set("Authorization", "Bearer dev-key")
	adminEventsRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminEventsRec, adminEventsReq)
	if adminEventsRec.Code != http.StatusOK || !strings.Contains(adminEventsRec.Body.String(), `"type":"submitted"`) {
		t.Fatalf("admin should read user task events: %d body=%s", adminEventsRec.Code, adminEventsRec.Body.String())
	}

	adminDeleteReq := httptest.NewRequest(http.MethodPost, "/api/image-tasks/delete", strings.NewReader(`{"items":[{"owner_id":"`+created.Item.ID+`","id":"user-task-visible"}]}`))
	adminDeleteReq.Header.Set("Authorization", "Bearer dev-key")
	adminDeleteReq.Header.Set("Content-Type", "application/json")
	adminDeleteRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminDeleteRec, adminDeleteReq)
	if adminDeleteRec.Code != http.StatusOK || !strings.Contains(adminDeleteRec.Body.String(), `"removed":1`) {
		t.Fatalf("admin should delete user task: %d body=%s", adminDeleteRec.Code, adminDeleteRec.Body.String())
	}

	adminDeletedReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids=user-task-visible&owner_id="+created.Item.ID+"&include_deleted=true", nil)
	adminDeletedReq.Header.Set("Authorization", "Bearer dev-key")
	adminDeletedRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminDeletedRec, adminDeletedReq)
	if adminDeletedRec.Code != http.StatusOK || !strings.Contains(adminDeletedRec.Body.String(), `"deleted_by"`) {
		t.Fatalf("admin-deleted task should remain auditable: %d body=%s", adminDeletedRec.Code, adminDeletedRec.Body.String())
	}
	adminDeletedEventsReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks/user-task-visible/events?owner_id="+created.Item.ID, nil)
	adminDeletedEventsReq.Header.Set("Authorization", "Bearer dev-key")
	adminDeletedEventsRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminDeletedEventsRec, adminDeletedEventsReq)
	if adminDeletedEventsRec.Code != http.StatusOK || !strings.Contains(adminDeletedEventsRec.Body.String(), `"type":"deleted"`) {
		t.Fatalf("admin deletion should be recorded on owner task events: %d body=%s", adminDeletedEventsRec.Code, adminDeletedEventsRec.Body.String())
	}

	userListReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks?owner_id=all&query=user-task-visible", nil)
	userListReq.Header.Set("Authorization", "Bearer "+created.Key)
	userListRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(userListRec, userListReq)
	if userListRec.Code != http.StatusOK || strings.Contains(userListRec.Body.String(), `"id":"user-task-visible"`) {
		t.Fatalf("deleted user task should be hidden from active user list: %d body=%s", userListRec.Code, userListRec.Body.String())
	}
}

func TestUserCannotDeleteOtherUserTasksWithOwnerID(t *testing.T) {
	upstream := &taskCaptureUpstream{requests: make(chan api.ImageGenerationPayload, 2)}
	server, cleanup := newTestServer(t, upstream)
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"isolation-user@example.com","name":"Isolation User","password":"password123","role":"user"}`))
	createReq.Header.Set("Authorization", "Bearer dev-key")
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create user failed: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created user: %v", err)
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"admin-owned-task","prompt":"admin prompt","model":"gpt-image-2"}`))
	adminReq.Header.Set("Authorization", "Bearer dev-key")
	adminReq.Header.Set("Content-Type", "application/json")
	adminRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusAccepted {
		t.Fatalf("create admin task failed: %d body=%s", adminRec.Code, adminRec.Body.String())
	}
	var adminTask struct {
		OwnerID string `json:"owner_id"`
	}
	if err := json.Unmarshal(adminRec.Body.Bytes(), &adminTask); err != nil {
		t.Fatalf("decode admin task: %v", err)
	}

	userDeleteReq := httptest.NewRequest(http.MethodPost, "/api/image-tasks/delete", strings.NewReader(`{"items":[{"owner_id":"`+adminTask.OwnerID+`","id":"admin-owned-task"}]}`))
	userDeleteReq.Header.Set("Authorization", "Bearer "+created.Key)
	userDeleteReq.Header.Set("Content-Type", "application/json")
	userDeleteRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(userDeleteRec, userDeleteReq)
	if userDeleteRec.Code != http.StatusOK || !strings.Contains(userDeleteRec.Body.String(), `"removed":0`) {
		t.Fatalf("user should not delete another owner task: %d body=%s", userDeleteRec.Code, userDeleteRec.Body.String())
	}

	adminListReq := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids=admin-owned-task&owner_id="+adminTask.OwnerID, nil)
	adminListReq.Header.Set("Authorization", "Bearer dev-key")
	adminListRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(adminListRec, adminListReq)
	if adminListRec.Code != http.StatusOK || !strings.Contains(adminListRec.Body.String(), `"id":"admin-owned-task"`) || strings.Contains(adminListRec.Body.String(), `"deleted_at"`) {
		t.Fatalf("admin task should remain active: %d body=%s", adminListRec.Code, adminListRec.Body.String())
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
