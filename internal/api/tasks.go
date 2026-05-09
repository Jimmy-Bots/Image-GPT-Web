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
	taskPhaseQueued      = "queued"
	taskPhaseWaitingSlot = "waiting_slot"
	taskPhaseProcessing  = "processing"
	taskPhaseFinished    = "finished"
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
	OwnerID       string
	OwnerName     string
	OwnerRole     domain.Role
	OwnerAuthType string
	TaskID        string
	Mode          string
	Receipt       domain.UserQuotaReceipt
	Gen           ImageGenerationPayload
	Edit          ImageEditPayload
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
	_ = q.store.UpdateImageTask(ctx, job.OwnerID, job.TaskID, taskQueued, taskPhaseWaitingSlot, nil, "")
	q.addTaskEventContext(ctx, job, "queued", "任务进入等待队列", map[string]any{
		"status": taskQueued,
		"phase":  taskPhaseWaitingSlot,
	})
	var (
		result         map[string]any
		err            error
		lastAttemptCtx context.Context
	)
	for attempt := 1; attempt <= maxImageTaskAttempts; attempt++ {
		_ = q.store.UpdateImageTask(ctx, job.OwnerID, job.TaskID, taskRunning, taskPhaseProcessing, nil, "")
		q.addTaskEventContext(ctx, job, "processing", "开始处理图片任务", map[string]any{
			"status":  taskRunning,
			"phase":   taskPhaseProcessing,
			"attempt": attempt,
		})
		attemptCtx := withStructuredLog(ctx, q.taskEventLogWriter(job), "upstream", q.taskEventDetail(job, map[string]any{
			"log_kind": "image_request",
			"attempt":  attempt,
		}))
		lastAttemptCtx = attemptCtx
		if job.Mode == "edit" {
			result, err = q.upstream.EditImage(attemptCtx, job.Edit)
		} else {
			result, err = q.upstream.GenerateImage(attemptCtx, job.Gen)
		}
		if err == nil {
			break
		}
		attemptDetail := map[string]any{
			"status":    "attempt_failed",
			"phase":     taskPhaseWaitingSlot,
			"mode":      job.Mode,
			"attempt":   attempt,
			"error":     err.Error(),
			"queue_try": attempt,
		}
		appendStructuredLogAttempt(attemptCtx, attemptDetail)
		q.addTaskEventContext(ctx, job, "attempt_failed", "上游尝试失败", attemptDetail)
		if attempt < maxImageTaskAttempts {
			_ = q.store.UpdateImageTask(ctx, job.OwnerID, job.TaskID, taskQueued, taskPhaseWaitingSlot, nil, "")
			log.Printf("image_task retry id=%s owner=%s mode=%s attempt=%d err=%v", job.TaskID, job.OwnerID, job.Mode, attempt+1, err)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	if err != nil {
		attempts := logAttempts(lastAttemptCtx)
		if job.Receipt.Total > 0 {
			_, _ = q.store.RefundUserQuota(context.Background(), job.OwnerID, job.Receipt)
		}
		errorDetail := map[string]any{
			"status":         taskError,
			"phase":          taskPhaseFinished,
			"quota_used":     0,
			"quota_reserved": job.Receipt.Total,
			"quota_refund":   job.Receipt.Total,
			"error":          err.Error(),
			"attempts":       attempts,
		}
		log.Printf("image_task failed id=%s owner=%s mode=%s err=%v", job.TaskID, job.OwnerID, job.Mode, err)
		if updateErr := q.updateImageTaskFinal(context.Background(), job, taskError, jsonData([]any{}), err.Error()); updateErr != nil {
			errorDetail["task_update_error"] = updateErr.Error()
		}
		if status := q.imageTaskStatus(context.Background(), job); status != "" {
			errorDetail["task_status_before_event"] = status
		}
		q.addTaskEventContext(context.Background(), job, "error", "图片任务失败", errorDetail)
		return
	}
	prompt := job.Gen.Prompt
	if job.Mode == "edit" {
		prompt = job.Edit.Prompt
	}
	saved := persistImageResultItems(q.imagesDir, q.baseURL, result, prompt, job.OwnerID)
	shapeImageResponseForClient(result, "url")
	count := imageResultCount(result)
	if count > 0 {
		_, _ = q.store.AddUserQuotaUsage(context.Background(), job.OwnerID, count, time.Now())
	}
	if refund := job.Receipt.Total - count; refund > 0 {
		_, _ = q.store.RefundUserQuota(context.Background(), job.OwnerID, quotaRefundPortion(job.Receipt, refund))
	}
	data := result["data"]
	attempts := logAttempts(lastAttemptCtx)
	successDetail := map[string]any{
		"status":         taskSuccess,
		"phase":          taskPhaseFinished,
		"items":          count,
		"archived":       saved,
		"quota_reserved": job.Receipt.Total,
		"quota_used":     count,
		"quota_refund":   maxInt(0, job.Receipt.Total-count),
		"attempts":       attempts,
	}
	log.Printf("image_task success id=%s owner=%s mode=%s items=%d archived=%d base_url_configured=%t", job.TaskID, job.OwnerID, job.Mode, count, saved, q.baseURL != "")
	if updateErr := q.updateImageTaskFinal(context.Background(), job, taskSuccess, jsonData(data), ""); updateErr != nil {
		successDetail["task_update_error"] = updateErr.Error()
	}
	if status := q.imageTaskStatus(context.Background(), job); status != "" {
		successDetail["task_status_before_event"] = status
	}
	q.addTaskEventContext(context.Background(), job, "success", "图片任务成功", successDetail)
}

func (q *TaskQueue) updateImageTaskFinal(ctx context.Context, job taskJob, status string, data json.RawMessage, taskErr string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		lastErr = q.store.UpdateImageTask(ctx, job.OwnerID, job.TaskID, status, taskPhaseFinished, data, taskErr)
		if lastErr == nil {
			return nil
		}
		log.Printf("image_task final_update_failed id=%s owner=%s status=%s attempt=%d err=%v", job.TaskID, job.OwnerID, status, attempt, lastErr)
		time.Sleep(time.Duration(attempt*100) * time.Millisecond)
	}
	return lastErr
}

func (q *TaskQueue) imageTaskStatus(ctx context.Context, job taskJob) string {
	items, err := q.store.ListImageTasks(ctx, job.OwnerID, []string{job.TaskID}, false, true)
	if err != nil || len(items) == 0 {
		return ""
	}
	return items[0].Status
}

func (q *TaskQueue) addTaskEventContext(ctx context.Context, job taskJob, eventType string, summary string, detail map[string]any) {
	payload := q.taskEventDetail(job, detail)
	payload["event_type"] = eventType
	payload["actor_id"] = "system"
	payload["actor_name"] = "Image task worker"
	raw, _ := json.Marshal(payload)
	at := time.Now().UTC()
	_ = q.store.AddTaskEvent(ctx, domain.TaskEvent{
		ID:      randomLogID(),
		OwnerID: job.OwnerID,
		TaskID:  job.TaskID,
		Time:    at,
		Type:    eventType,
		Summary: summary,
		Detail:  raw,
	})
	_ = q.store.AddLog(ctx, domain.SystemLog{
		ID:      randomLogID(),
		Time:    at,
		Type:    "task",
		Summary: summary,
		Detail:  raw,
	})
}

func (q *TaskQueue) taskEventLogWriter(job taskJob) structuredLogWriter {
	return func(ctx context.Context, logType string, summary string, detail map[string]any) {
		eventType := strings.TrimSpace(logType)
		if eventType == "" {
			eventType = "upstream"
		}
		q.addTaskEventContext(ctx, job, eventType, summary, detail)
	}
}

func (q *TaskQueue) taskEventDetail(job taskJob, detail map[string]any) map[string]any {
	payload := cloneLogDetail(detail)
	payload["task_id"] = job.TaskID
	payload["owner_id"] = job.OwnerID
	payload["subject_id"] = job.OwnerID
	payload["name"] = job.OwnerName
	payload["role"] = job.OwnerRole
	payload["auth_type"] = job.OwnerAuthType
	payload["mode"] = job.Mode
	payload["model"] = imageTaskModel(job)
	payload["size"] = imageTaskSize(job)
	payload["requested_count"] = imageTaskCount(job)
	payload["endpoint"] = imageTaskEndpoint(job.Mode)
	if prompt := imageTaskPrompt(job); prompt != "" {
		payload["prompt"] = prompt
	}
	return payload
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

func imageTaskPrompt(job taskJob) string {
	if job.Mode == "edit" {
		return job.Edit.Prompt
	}
	return job.Gen.Prompt
}

func imageTaskEndpoint(mode string) string {
	if mode == "edit" {
		return "/api/image-tasks/edits"
	}
	return "/api/image-tasks/generations"
}

func imageTaskCount(job taskJob) int {
	if job.Mode == "edit" {
		return job.Edit.N
	}
	return job.Gen.N
}

type imageTaskCreateRequest struct {
	ClientTaskID string `json:"client_task_id"`
	Prompt       string `json:"prompt"`
	Model        string `json:"model"`
	Size         string `json:"size"`
	N            int    `json:"n"`
}

type imageTaskDeleteRequest struct {
	IDs   []string             `json:"ids"`
	Items []imageTaskDeleteRef `json:"items"`
}

type imageTaskDeleteRef struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
}

func (s *Server) handleListImageTasks(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	ids := compactStrings(strings.Split(r.URL.Query().Get("ids"), ","))
	includeData := len(ids) > 0 || boolFromAny(r.URL.Query().Get("detail"))
	ownerID := taskOwnerScope(identity, r.URL.Query().Get("owner_id"))
	if len(ids) > 0 {
		items, err := s.store.ListImageTasks(r.Context(), ownerID, ids, includeData, boolFromAny(r.URL.Query().Get("include_deleted")))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
			return
		}
		missing := missingTaskIDs(ids, items)
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "missing_ids": missing, "total": len(items), "page": 1, "page_size": len(items)})
		return
	}
	query := storage.ImageTaskPageQuery{
		Page:           queryInt(r, "page", 1),
		PageSize:       queryInt(r, "page_size", 25),
		Query:          strings.TrimSpace(r.URL.Query().Get("query")),
		Status:         strings.TrimSpace(r.URL.Query().Get("status")),
		Mode:           strings.TrimSpace(r.URL.Query().Get("mode")),
		Model:          strings.TrimSpace(r.URL.Query().Get("model")),
		OwnerID:        taskOwnerFilter(identity, r.URL.Query().Get("owner_id")),
		Size:           strings.TrimSpace(r.URL.Query().Get("size")),
		DateFrom:       strings.TrimSpace(r.URL.Query().Get("date_from")),
		DateTo:         strings.TrimSpace(r.URL.Query().Get("date_to")),
		Deleted:        strings.TrimSpace(r.URL.Query().Get("deleted")),
		IncludeDeleted: boolFromAny(r.URL.Query().Get("include_deleted")),
	}
	items, total, err := s.store.ListImageTasksPage(r.Context(), taskOwnerScope(identity, r.URL.Query().Get("owner_id")), query)
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
	refs := normalizeDeleteTaskRefs(identity, req)
	if len(refs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"removed": 0})
		return
	}
	before := make([]domain.ImageTask, 0, len(refs))
	for _, ref := range refs {
		items, err := s.store.ListImageTasks(r.Context(), ref.OwnerID, []string{ref.ID}, false, false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
			return
		}
		before = append(before, items...)
	}
	removed, err := s.store.DeleteImageTasksByRef(r.Context(), refs, identity.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	deletedIDs := make([]string, 0, len(before))
	for _, task := range before {
		deletedIDs = append(deletedIDs, task.ID)
		s.addImageTaskEventContext(r.Context(), identity, task, "deleted", "图片任务已从任务列表删除", map[string]any{
			"deleted_by":    identity.ID,
			"deleted_count": removed,
		})
	}
	if len(deletedIDs) > 0 {
		ownerIDs := uniqueTaskOwnerIDs(before)
		detail := map[string]any{
			"task_ids":       deletedIDs,
			"requested_ids":  deleteTaskRefIDs(refs),
			"owner_ids":      ownerIDs,
			"removed":        removed,
			"retained_event": true,
		}
		if len(ownerIDs) == 1 {
			detail["owner_id"] = ownerIDs[0]
			detail["subject_id"] = ownerIDs[0]
		}
		if len(deletedIDs) == 1 {
			detail["task_id"] = deletedIDs[0]
		}
		s.addAuditLog(r, identity, "task", "图片任务删除", detail)
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) handleListImageTaskEvents(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	taskID := strings.TrimSpace(r.PathValue("task_id"))
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "task_id is required")
		return
	}
	ownerID := taskOwnerScope(identity, r.URL.Query().Get("owner_id"))
	items, err := s.store.ListTaskEvents(r.Context(), ownerID, taskID, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	if len(items) == 0 {
		tasks, err := s.store.ListImageTasks(r.Context(), ownerID, []string{taskID}, false, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
			return
		}
		if len(tasks) == 0 {
			writeError(w, http.StatusNotFound, "not_found", "task not found")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
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
	if !s.checkContentPolicy(w, r, identity, "/api/image-tasks/generations", req.Model, req.Prompt, req.Prompt) {
		return
	}
	requestedCount := clampImageTaskCountWithLimit(req.N, s.imageMaxCount(r.Context()))
	_, receipt, err := s.reserveImageQuota(r.Context(), identity, requestedCount)
	if err != nil {
		errorCode := "quota_exceeded"
		errorMessage := err.Error()
		if !strings.Contains(strings.ToLower(errorMessage), "quota exceeded") {
			errorCode = "reserve_quota_failed"
		}
		s.addLogContext(r.Context(), "task", "图片任务提交失败", map[string]any{
			"owner_id":        identity.ID,
			"name":            identity.Name,
			"role":            identity.Role,
			"auth_type":       identity.AuthType,
			"mode":            "generate",
			"model":           req.Model,
			"size":            req.Size,
			"requested_count": requestedCount,
			"status":          "failed",
			"quota_used":      0,
			"quota_reserved":  0,
			"error":           errorMessage,
			"error_code":      errorCode,
		})
		if errorCode == "quota_exceeded" {
			writeError(w, http.StatusForbidden, "quota_exceeded", "insufficient quota")
			return
		}
		writeError(w, http.StatusInternalServerError, "reserve_quota_failed", errorMessage)
		return
	}
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:             taskID,
		OwnerID:        identity.ID,
		Status:         taskQueued,
		Phase:          taskPhaseQueued,
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
			items, _ := s.store.ListImageTasks(r.Context(), identity.ID, []string{taskID}, true, false)
			if len(items) > 0 {
				writeJSON(w, http.StatusOK, items[0])
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "task_create_failed", err.Error())
		return
	}
	s.addImageTaskEventContext(r.Context(), identity, task, "submitted", "图片任务已提交", map[string]any{
		"quota_reserved": receipt.Total,
	})
	err = s.tasks.Submit(taskJob{
		OwnerID:       identity.ID,
		OwnerName:     identity.Name,
		OwnerRole:     identity.Role,
		OwnerAuthType: identity.AuthType,
		TaskID:        taskID,
		Mode:          "generate",
		Receipt:       receipt,
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
		_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskError, taskPhaseFinished, jsonData([]any{}), err.Error())
		task.Status = taskError
		task.Phase = taskPhaseFinished
		s.addImageTaskEventContext(r.Context(), identity, task, "queue_full", "任务队列已满，提交失败", map[string]any{
			"status":         "failed",
			"quota_used":     0,
			"quota_reserved": receipt.Total,
			"quota_refund":   receipt.Total,
			"error":          err.Error(),
			"error_code":     "task_queue_full",
		})
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
	req, ok := s.parseImageEditPayload(w, r, identity.ID)
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
	if !s.checkContentPolicy(w, r, identity, "/api/image-tasks/edits", req.Model, req.Prompt, req.Prompt) {
		return
	}
	taskID := strings.TrimSpace(r.FormValue("client_task_id"))
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "client_task_id is required")
		return
	}
	_, receipt, err := s.reserveImageQuota(r.Context(), identity, req.N)
	if err != nil {
		errorCode := "quota_exceeded"
		errorMessage := err.Error()
		if !strings.Contains(strings.ToLower(errorMessage), "quota exceeded") {
			errorCode = "reserve_quota_failed"
		}
		s.addLogContext(r.Context(), "task", "图片任务提交失败", map[string]any{
			"owner_id":        identity.ID,
			"name":            identity.Name,
			"role":            identity.Role,
			"auth_type":       identity.AuthType,
			"mode":            "edit",
			"model":           req.Model,
			"size":            req.Size,
			"requested_count": req.N,
			"status":          "failed",
			"quota_used":      0,
			"quota_reserved":  0,
			"error":           errorMessage,
			"error_code":      errorCode,
		})
		if errorCode == "quota_exceeded" {
			writeError(w, http.StatusForbidden, "quota_exceeded", "insufficient quota")
			return
		}
		writeError(w, http.StatusInternalServerError, "reserve_quota_failed", errorMessage)
		return
	}
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:             taskID,
		OwnerID:        identity.ID,
		Status:         taskQueued,
		Phase:          taskPhaseQueued,
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
	s.addImageTaskEventContext(r.Context(), identity, task, "submitted", "图片任务已提交", map[string]any{
		"quota_reserved": receipt.Total,
	})
	if err := s.tasks.Submit(taskJob{OwnerID: identity.ID, OwnerName: identity.Name, OwnerRole: identity.Role, OwnerAuthType: identity.AuthType, TaskID: taskID, Mode: "edit", Receipt: receipt, Edit: req}); err != nil {
		s.refundImageQuota(r.Context(), identity, receipt)
		_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskError, taskPhaseFinished, jsonData([]any{}), err.Error())
		task.Status = taskError
		task.Phase = taskPhaseFinished
		s.addImageTaskEventContext(r.Context(), identity, task, "queue_full", "任务队列已满，提交失败", map[string]any{
			"status":         "failed",
			"quota_used":     0,
			"quota_reserved": receipt.Total,
			"quota_refund":   receipt.Total,
			"error":          err.Error(),
			"error_code":     "task_queue_full",
		})
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

func taskOwnerScope(identity Identity, requested string) string {
	if identity.Role != domain.RoleAdmin {
		return identity.ID
	}
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "all" {
		return ""
	}
	return requested
}

func taskOwnerFilter(identity Identity, requested string) string {
	if identity.Role != domain.RoleAdmin {
		return ""
	}
	requested = strings.TrimSpace(requested)
	if requested == "all" {
		return ""
	}
	return requested
}

func normalizeDeleteTaskRefs(identity Identity, req imageTaskDeleteRequest) []storage.ImageTaskRef {
	seen := map[string]struct{}{}
	refs := make([]storage.ImageTaskRef, 0, len(req.Items)+len(req.IDs))
	add := func(ownerID string, id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if identity.Role != domain.RoleAdmin {
			ownerID = identity.ID
		} else {
			ownerID = strings.TrimSpace(ownerID)
			if ownerID == "" || ownerID == "all" {
				ownerID = identity.ID
			}
		}
		key := ownerID + "\x00" + id
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, storage.ImageTaskRef{OwnerID: ownerID, ID: id})
	}
	for _, item := range req.Items {
		add(item.OwnerID, item.ID)
	}
	for _, id := range req.IDs {
		add("", id)
	}
	return refs
}

func deleteTaskRefIDs(refs []storage.ImageTaskRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.OwnerID) != "" {
			out = append(out, strings.TrimSpace(ref.OwnerID)+":"+strings.TrimSpace(ref.ID))
			continue
		}
		out = append(out, strings.TrimSpace(ref.ID))
	}
	return out
}

func uniqueTaskOwnerIDs(tasks []domain.ImageTask) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, task := range tasks {
		ownerID := strings.TrimSpace(task.OwnerID)
		if ownerID == "" {
			continue
		}
		if _, ok := seen[ownerID]; ok {
			continue
		}
		seen[ownerID] = struct{}{}
		out = append(out, ownerID)
	}
	return out
}

func (s *Server) addImageTaskEventContext(ctx context.Context, identity Identity, task domain.ImageTask, eventType string, summary string, detail map[string]any) {
	ownerID := strings.TrimSpace(task.OwnerID)
	if ownerID == "" {
		ownerID = identity.ID
	}
	payload := map[string]any{}
	payload["task_id"] = task.ID
	payload["owner_id"] = ownerID
	payload["subject_id"] = ownerID
	payload["name"] = identity.Name
	payload["role"] = identity.Role
	payload["auth_type"] = identity.AuthType
	payload["actor_id"] = identity.ID
	payload["actor_name"] = identity.Name
	payload["actor_role"] = identity.Role
	payload["actor_auth_type"] = identity.AuthType
	payload["mode"] = task.Mode
	payload["model"] = task.Model
	payload["size"] = task.Size
	payload["requested_count"] = task.RequestedCount
	payload["endpoint"] = imageTaskEndpoint(task.Mode)
	payload["event_type"] = eventType
	if task.Prompt != "" {
		payload["prompt"] = task.Prompt
	}
	if task.Status != "" {
		payload["status"] = task.Status
	}
	if task.Phase != "" {
		payload["phase"] = task.Phase
	}
	for key, value := range detail {
		payload[key] = value
	}
	raw, _ := json.Marshal(payload)
	at := time.Now().UTC()
	_ = s.store.AddTaskEvent(ctx, domain.TaskEvent{
		ID:      randomLogID(),
		OwnerID: ownerID,
		TaskID:  task.ID,
		Time:    at,
		Type:    eventType,
		Summary: summary,
		Detail:  raw,
	})
	_ = s.store.AddLog(ctx, domain.SystemLog{
		ID:      randomLogID(),
		Time:    at,
		Type:    "task",
		Summary: summary,
		Detail:  raw,
	})
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
