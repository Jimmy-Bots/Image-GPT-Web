package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

const (
	taskQueued  = "queued"
	taskRunning = "running"
	taskSuccess = "success"
	taskError   = "error"
)

type TaskQueue struct {
	store    *storage.Store
	upstream Upstream
	jobs     chan taskJob
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

type taskJob struct {
	OwnerID string
	TaskID  string
	Mode    string
	Gen     ImageGenerationPayload
	Edit    ImageEditPayload
}

func NewTaskQueue(store *storage.Store, upstream Upstream, workers int, queueSize int) *TaskQueue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &TaskQueue{
		store:    store,
		upstream: upstream,
		jobs:     make(chan taskJob, queueSize),
		cancel:   cancel,
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
	return q
}

func (q *TaskQueue) Close() {
	q.cancel()
	q.wg.Wait()
}

func (q *TaskQueue) Submit(job taskJob) error {
	select {
	case q.jobs <- job:
		return nil
	default:
		return errors.New("image task queue is full")
	}
}

func (q *TaskQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-q.jobs:
			q.runJob(ctx, job)
		}
	}
}

func (q *TaskQueue) runJob(parent context.Context, job taskJob) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	_ = q.store.UpdateImageTask(ctx, job.OwnerID, job.TaskID, taskRunning, nil, "")
	var (
		result map[string]any
		err    error
	)
	if job.Mode == "edit" {
		result, err = q.upstream.EditImage(ctx, job.Edit)
	} else {
		result, err = q.upstream.GenerateImage(ctx, job.Gen)
	}
	if err != nil {
		_ = q.store.UpdateImageTask(context.Background(), job.OwnerID, job.TaskID, taskError, jsonData([]any{}), err.Error())
		return
	}
	data := result["data"]
	_ = q.store.UpdateImageTask(context.Background(), job.OwnerID, job.TaskID, taskSuccess, jsonData(data), "")
}

type imageTaskCreateRequest struct {
	ClientTaskID string `json:"client_task_id"`
	Prompt       string `json:"prompt"`
	Model        string `json:"model"`
	Size         string `json:"size"`
}

func (s *Server) handleListImageTasks(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	ids := compactStrings(strings.Split(r.URL.Query().Get("ids"), ","))
	items, err := s.store.ListImageTasks(r.Context(), identity.ID, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	missing := missingTaskIDs(ids, items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "missing_ids": missing})
}

func (s *Server) handleCreateGenerationTask(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var req imageTaskCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	taskID := strings.TrimSpace(req.ClientTaskID)
	if taskID == "" || strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "client_task_id and prompt are required")
		return
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:        taskID,
		OwnerID:   identity.ID,
		Status:    taskQueued,
		Mode:      "generate",
		Model:     req.Model,
		Size:      req.Size,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateImageTask(r.Context(), task); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			items, _ := s.store.ListImageTasks(r.Context(), identity.ID, []string{taskID})
			if len(items) > 0 {
				writeJSON(w, http.StatusOK, items[0])
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "task_create_failed", err.Error())
		return
	}
	err := s.tasks.Submit(taskJob{
		OwnerID: identity.ID,
		TaskID:  taskID,
		Mode:    "generate",
		Gen: ImageGenerationPayload{
			Prompt:         req.Prompt,
			Model:          req.Model,
			N:              1,
			Size:           req.Size,
			ResponseFormat: "url",
		},
	})
	if err != nil {
		_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskError, jsonData([]any{}), err.Error())
		writeError(w, http.StatusServiceUnavailable, "task_queue_full", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) handleCreateEditTask(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	req, ok := parseImageEditPayload(w, r)
	if !ok {
		return
	}
	taskID := strings.TrimSpace(r.FormValue("client_task_id"))
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "client_task_id is required")
		return
	}
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:        taskID,
		OwnerID:   identity.ID,
		Status:    taskQueued,
		Mode:      "edit",
		Model:     req.Model,
		Size:      req.Size,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateImageTask(r.Context(), task); err != nil {
		writeError(w, http.StatusInternalServerError, "task_create_failed", err.Error())
		return
	}
	if err := s.tasks.Submit(taskJob{OwnerID: identity.ID, TaskID: taskID, Mode: "edit", Edit: req}); err != nil {
		_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskError, jsonData([]any{}), err.Error())
		writeError(w, http.StatusServiceUnavailable, "task_queue_full", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func missingTaskIDs(requested []string, items []domain.ImageTask) []string {
	if len(requested) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		seen[item.ID] = struct{}{}
	}
	missing := make([]string, 0)
	for _, id := range requested {
		if _, ok := seen[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func randomLogID() string {
	return auth.RandomID(12)
}
