package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

const (
	taskQueued           = "queued"
	taskRunning          = "running"
	taskSuccess          = "success"
	taskError            = "error"
	defaultImageTaskSize = "auto"
	maxImageTaskAttempts = 2
)

type TaskQueue struct {
	imagesDir string
	baseURL   string
	store     *storage.Store
	upstream  Upstream
	jobs      chan taskJob
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type taskJob struct {
	OwnerID string
	TaskID  string
	Mode    string
	Receipt domain.UserQuotaReceipt
	Gen     ImageGenerationPayload
	Edit    ImageEditPayload
}

func NewTaskQueue(store *storage.Store, upstream Upstream, imagesDir string, baseURL string, workers int, queueSize int) *TaskQueue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &TaskQueue{
		imagesDir: imagesDir,
		baseURL:   baseURL,
		store:     store,
		upstream:  upstream,
		jobs:      make(chan taskJob, queueSize),
		cancel:    cancel,
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		log.Printf("image_task worker_start index=%d", i+1)
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
	for attempt := 1; attempt <= maxImageTaskAttempts; attempt++ {
		attemptCtx := withStructuredLog(ctx, q.addLogContext, "task", map[string]any{
			"task_id":   job.TaskID,
			"owner_id":  job.OwnerID,
			"mode":      job.Mode,
			"model":     imageTaskModel(job),
			"size":      imageTaskSize(job),
			"log_kind":  "image_attempt",
			"queue_try": attempt,
		})
		if job.Mode == "edit" {
			result, err = q.upstream.EditImage(attemptCtx, job.Edit)
		} else {
			result, err = q.upstream.GenerateImage(attemptCtx, job.Gen)
		}
		if err == nil {
			break
		}
		if attempt < maxImageTaskAttempts {
			log.Printf("image_task retry id=%s owner=%s mode=%s attempt=%d err=%v", job.TaskID, job.OwnerID, job.Mode, attempt+1, err)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	if err != nil {
		if job.Receipt.Total > 0 {
			_, _ = q.store.RefundUserQuota(context.Background(), job.OwnerID, job.Receipt)
		}
		log.Printf("image_task failed id=%s owner=%s mode=%s err=%v", job.TaskID, job.OwnerID, job.Mode, err)
		_ = q.store.UpdateImageTask(context.Background(), job.OwnerID, job.TaskID, taskError, jsonData([]any{}), err.Error())
		return
	}
	prompt := job.Gen.Prompt
	if job.Mode == "edit" {
		prompt = job.Edit.Prompt
	}
	saved := persistImageResultItems(q.imagesDir, q.baseURL, result, prompt)
	shapeImageResponseForClient(result, "url")
	count := imageResultCount(result)
	if refund := job.Receipt.Total - count; refund > 0 {
		_, _ = q.store.RefundUserQuota(context.Background(), job.OwnerID, quotaRefundPortion(job.Receipt, refund))
	}
	log.Printf("image_task success id=%s owner=%s mode=%s items=%d archived=%d base_url_configured=%t", job.TaskID, job.OwnerID, job.Mode, count, saved, q.baseURL != "")
	data := result["data"]
	_ = q.store.UpdateImageTask(context.Background(), job.OwnerID, job.TaskID, taskSuccess, jsonData(data), "")
}

func (q *TaskQueue) addLogContext(ctx context.Context, logType string, summary string, detail map[string]any) {
	payload, _ := json.Marshal(detail)
	_ = q.store.AddLog(ctx, domain.SystemLog{
		ID:      randomLogID(),
		Time:    time.Now().UTC(),
		Type:    logType,
		Summary: summary,
		Detail:  payload,
	})
}

func imageTaskModel(job taskJob) string {
	if job.Mode == "edit" {
		return job.Edit.Model
	}
	return job.Gen.Model
}

func imageTaskSize(job taskJob) string {
	if job.Mode == "edit" {
		return job.Edit.Size
	}
	return job.Gen.Size
}

type imageTaskCreateRequest struct {
	ClientTaskID string `json:"client_task_id"`
	Prompt       string `json:"prompt"`
	Model        string `json:"model"`
	Size         string `json:"size"`
	N            int    `json:"n"`
}

type imageTaskDeleteRequest struct {
	IDs []string `json:"ids"`
}

func (s *Server) handleListImageTasks(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	ids := compactStrings(strings.Split(r.URL.Query().Get("ids"), ","))
	includeData := len(ids) > 0 || boolFromAny(r.URL.Query().Get("detail"))
	if len(ids) > 0 {
		items, err := s.store.ListImageTasks(r.Context(), identity.ID, ids, includeData)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
			return
		}
		missing := missingTaskIDs(ids, items)
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "missing_ids": missing, "total": len(items), "page": 1, "page_size": len(items)})
		return
	}
	query := storage.ImageTaskPageQuery{
		Page:     queryInt(r, "page", 1),
		PageSize: queryInt(r, "page_size", 25),
		Query:    strings.TrimSpace(r.URL.Query().Get("query")),
		Status:   strings.TrimSpace(r.URL.Query().Get("status")),
	}
	items, total, err := s.store.ListImageTasksPage(r.Context(), identity.ID, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	missing := missingTaskIDs(ids, items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "missing_ids": missing, "total": total, "page": query.Page, "page_size": query.PageSize})
}

func (s *Server) handleDeleteImageTasks(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var req imageTaskDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	removed, err := s.store.DeleteImageTasks(r.Context(), identity.ID, compactStrings(req.IDs))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
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
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Model = strings.TrimSpace(req.Model)
	req.Size = normalizeImageTaskSize(req.Size)
	if taskID == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "client_task_id and prompt are required")
		return
	}
	model, err := s.enforceImageRequestModel(r.Context(), identity, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_model", err.Error())
		return
	}
	req.Model = model
	if !s.checkContentPolicy(w, r, identity, "/api/image-tasks/generations", req.Model, req.Prompt) {
		return
	}
	requestedCount := clampImageTaskCountWithLimit(req.N, s.imageMaxCount(r.Context()))
	_, receipt, err := s.reserveImageQuota(r.Context(), identity, requestedCount)
	if err != nil {
		writeError(w, http.StatusForbidden, "quota_exceeded", "insufficient quota")
		return
	}
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:             taskID,
		OwnerID:        identity.ID,
		Status:         taskQueued,
		Mode:           "generate",
		Model:          req.Model,
		Size:           req.Size,
		Prompt:         req.Prompt,
		RequestedCount: requestedCount,
		ReservedQuota:  jsonData(receipt),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateImageTask(r.Context(), task); err != nil {
		s.refundImageQuota(r.Context(), identity, receipt)
		if strings.Contains(err.Error(), "UNIQUE") {
			items, _ := s.store.ListImageTasks(r.Context(), identity.ID, []string{taskID}, true)
			if len(items) > 0 {
				writeJSON(w, http.StatusOK, items[0])
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "task_create_failed", err.Error())
		return
	}
	err = s.tasks.Submit(taskJob{
		OwnerID: identity.ID,
		TaskID:  taskID,
		Mode:    "generate",
		Receipt: receipt,
		Gen: ImageGenerationPayload{
			Prompt:         req.Prompt,
			Model:          req.Model,
			N:              requestedCount,
			Size:           req.Size,
			ResponseFormat: "b64_json",
		},
	})
	if err != nil {
		s.refundImageQuota(r.Context(), identity, receipt)
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
	model, err := s.enforceImageRequestModel(r.Context(), identity, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_model", err.Error())
		return
	}
	req.Model = model
	req.Size = normalizeImageTaskSize(req.Size)
	req.N = clampImageTaskCountWithLimit(req.N, s.imageMaxCount(r.Context()))
	if !s.checkContentPolicy(w, r, identity, "/api/image-tasks/edits", req.Model, req.Prompt) {
		return
	}
	taskID := strings.TrimSpace(r.FormValue("client_task_id"))
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "client_task_id is required")
		return
	}
	_, receipt, err := s.reserveImageQuota(r.Context(), identity, req.N)
	if err != nil {
		writeError(w, http.StatusForbidden, "quota_exceeded", "insufficient quota")
		return
	}
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:             taskID,
		OwnerID:        identity.ID,
		Status:         taskQueued,
		Mode:           "edit",
		Model:          req.Model,
		Size:           req.Size,
		Prompt:         req.Prompt,
		RequestedCount: req.N,
		ReservedQuota:  jsonData(receipt),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateImageTask(r.Context(), task); err != nil {
		s.refundImageQuota(r.Context(), identity, receipt)
		writeError(w, http.StatusInternalServerError, "task_create_failed", err.Error())
		return
	}
	if err := s.tasks.Submit(taskJob{OwnerID: identity.ID, TaskID: taskID, Mode: "edit", Receipt: receipt, Edit: req}); err != nil {
		s.refundImageQuota(r.Context(), identity, receipt)
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

func clampImageTaskCountWithLimit(value int, limit int) int {
	if limit < 1 {
		limit = defaultImageMaxCount
	}
	if value < 1 {
		return 1
	}
	if value > limit {
		return limit
	}
	return value
}

func normalizeImageTaskSize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "auto") || strings.EqualFold(value, "default") || value == "默认" {
		return defaultImageTaskSize
	}
	return value
}
