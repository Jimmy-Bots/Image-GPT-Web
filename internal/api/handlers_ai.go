package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gpt-image-web/internal/domain"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	result, err := s.upstream.ListModels(r.Context())
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}
	result = filterModelsByAllowed(result, s.allowedPublicModels(r.Context()))
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var req ImageGenerationPayload
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required")
		return
	}
	model, err := s.enforceImageRequestModel(r.Context(), identity, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_model", err.Error())
		return
	}
	req.Model = model
	req.Size = normalizeImageTaskSize(req.Size)
	if !s.checkContentPolicy(w, r, identity, "/v1/images/generations", req.Model, req.Prompt, req.Prompt) {
		return
	}
	if req.N == 0 {
		req.N = 1
	}
	taskID := randomLogID()
	user, receipt, err := s.reserveImageQuota(r.Context(), identity, req.N)
	if err != nil {
		s.logCall(r, identity, "/v1/images/generations", req.Model, "failed", "insufficient quota", map[string]any{
			"size":            req.Size,
			"n":               req.N,
			"requested_count": req.N,
			"quota_used":      0,
			"quota_reserved":  0,
			"available_quota": 0,
			"error_code":      "quota_exceeded",
		})
		writeError(w, http.StatusForbidden, "quota_exceeded", "insufficient quota")
		return
	}
	requestedFormat := normalizeImageResponseFormat(req.ResponseFormat)
	req.ResponseFormat = "b64_json"
	start := time.Now()
	log.Printf("image_generation start user=%s auth=%s model=%s n=%d size=%s requested_format=%s", identity.ID, identity.AuthType, req.Model, req.N, req.Size, requestedFormat)
	if s.cfg.DebugLogging() {
		log.Printf("image_generation debug prompt_chars=%d prompt_preview=%q", utf8.RuneCountInString(req.Prompt), truncateForLog(req.Prompt, 120))
	}
	baseLog := identityLogFields(identity)
	baseLog["endpoint"] = "/v1/images/generations"
	baseLog["model"] = req.Model
	baseLog["size"] = req.Size
	baseLog["n"] = req.N
	baseLog["requested_count"] = req.N
	baseLog["requested_format"] = requestedFormat
	baseLog["log_kind"] = "image_request"
	baseLog["task_id"] = taskID
	ctx := withStructuredLog(r.Context(), s.addLogContext, "call", baseLog)
	requestedCount := req.N
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:             taskID,
		OwnerID:        identity.ID,
		Status:         taskRunning,
		Phase:          taskPhaseProcessing,
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
		log.Printf("image_generation task_record_failed user=%s task=%s err=%v", identity.ID, taskID, err)
	} else {
		s.addImageTaskEventContext(r.Context(), identity, task, "submitted", "同步图片生成请求已提交", map[string]any{
			"endpoint":         "/v1/images/generations",
			"requested_format": requestedFormat,
			"quota_reserved":   receipt.Total,
		})
	}
	result, err := s.upstream.GenerateImage(ctx, req)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		s.refundImageQuota(r.Context(), identity, receipt)
		_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskError, taskPhaseFinished, jsonData([]any{}), err.Error())
		s.addImageTaskEventContext(r.Context(), identity, task, "error", "同步图片生成失败", map[string]any{
			"endpoint":        "/v1/images/generations",
			"status":          taskError,
			"phase":           taskPhaseFinished,
			"duration_ms":     duration,
			"quota_reserved":  receipt.Total,
			"quota_used":      0,
			"quota_refund":    receipt.Total,
			"available_quota": user.AvailableQuota,
			"error":           err.Error(),
			"attempts":        logAttempts(ctx),
		})
		log.Printf("image_generation failed user=%s model=%s duration_ms=%d err=%v", identity.ID, req.Model, duration, err)
		s.logCall(r, identity, "/v1/images/generations", req.Model, "failed", err.Error(), map[string]any{
			"task_id":          taskID,
			"duration_ms":      duration,
			"requested_format": requestedFormat,
			"size":             req.Size,
			"n":                req.N,
			"requested_count":  req.N,
			"quota_reserved":   receipt.Total,
			"quota_used":       0,
			"available_quota":  user.AvailableQuota,
			"attempts":         logAttempts(ctx),
		})
		s.writeUpstreamError(w, err)
		return
	}
	saved := s.persistImageResults(r, result, req.Prompt, identity.ID)
	shapeImageResponseForClient(result, requestedFormat)
	count := imageResultCount(result)
	finalUser := user
	if updatedUser, ok := s.addImageQuotaUsage(r.Context(), identity, count); ok {
		finalUser = updatedUser
	}
	if refund := receipt.Total - count; refund > 0 {
		if updatedUser, ok := s.refundImageQuotaWithResult(r.Context(), identity, quotaRefundPortion(receipt, refund)); ok {
			finalUser = updatedUser
		}
	}
	_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskSuccess, taskPhaseFinished, jsonData(result["data"]), "")
	s.addImageTaskEventContext(r.Context(), identity, task, "success", "同步图片生成成功", map[string]any{
		"endpoint":         "/v1/images/generations",
		"status":           taskSuccess,
		"phase":            taskPhaseFinished,
		"duration_ms":      duration,
		"requested_format": requestedFormat,
		"items":            count,
		"archived":         saved,
		"quota_reserved":   receipt.Total,
		"quota_used":       count,
		"quota_refund":     maxInt(0, receipt.Total-count),
		"available_quota":  finalUser.AvailableQuota,
		"attempts":         logAttempts(ctx),
	})
	log.Printf("image_generation success user=%s model=%s items=%d archived=%d duration_ms=%d", identity.ID, req.Model, count, saved, duration)
	s.logCall(r, identity, "/v1/images/generations", req.Model, "success", "", map[string]any{
		"task_id":          taskID,
		"duration_ms":      duration,
		"requested_format": requestedFormat,
		"size":             req.Size,
		"n":                req.N,
		"requested_count":  req.N,
		"items":            count,
		"archived":         saved,
		"quota_reserved":   receipt.Total,
		"quota_used":       count,
		"available_quota":  finalUser.AvailableQuota,
		"attempts":         logAttempts(ctx),
	})
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleImageEdits(w http.ResponseWriter, r *http.Request) {
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
	if !s.checkContentPolicy(w, r, identity, "/v1/images/edits", req.Model, req.Prompt, req.Prompt) {
		return
	}
	requestedFormat := normalizeImageResponseFormat(req.ResponseFormat)
	req.ResponseFormat = "b64_json"
	taskID := randomLogID()
	user, receipt, err := s.reserveImageQuota(r.Context(), identity, req.N)
	if err != nil {
		s.logCall(r, identity, "/v1/images/edits", req.Model, "failed", "insufficient quota", map[string]any{
			"size":            req.Size,
			"n":               req.N,
			"requested_count": req.N,
			"input_images":    len(req.Images),
			"quota_used":      0,
			"quota_reserved":  0,
			"available_quota": 0,
			"error_code":      "quota_exceeded",
		})
		writeError(w, http.StatusForbidden, "quota_exceeded", "insufficient quota")
		return
	}
	start := time.Now()
	log.Printf("image_edit start user=%s auth=%s model=%s n=%d size=%s images=%d requested_format=%s", identity.ID, identity.AuthType, req.Model, req.N, req.Size, len(req.Images), requestedFormat)
	if s.cfg.DebugLogging() {
		log.Printf("image_edit debug prompt_chars=%d prompt_preview=%q", utf8.RuneCountInString(req.Prompt), truncateForLog(req.Prompt, 120))
	}
	baseLog := identityLogFields(identity)
	baseLog["endpoint"] = "/v1/images/edits"
	baseLog["model"] = req.Model
	baseLog["size"] = req.Size
	baseLog["n"] = req.N
	baseLog["requested_count"] = req.N
	baseLog["input_images"] = len(req.Images)
	baseLog["requested_format"] = requestedFormat
	baseLog["log_kind"] = "image_request"
	baseLog["task_id"] = taskID
	ctx := withStructuredLog(r.Context(), s.addLogContext, "call", baseLog)
	requestedCount := req.N
	now := time.Now().UTC()
	task := domain.ImageTask{
		ID:             taskID,
		OwnerID:        identity.ID,
		Status:         taskRunning,
		Phase:          taskPhaseProcessing,
		Mode:           "edit",
		Model:          req.Model,
		Size:           req.Size,
		Prompt:         req.Prompt,
		RequestedCount: requestedCount,
		ReservedQuota:  jsonData(receipt),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateImageTask(r.Context(), task); err != nil {
		log.Printf("image_edit task_record_failed user=%s task=%s err=%v", identity.ID, taskID, err)
	} else {
		s.addImageTaskEventContext(r.Context(), identity, task, "submitted", "同步图片编辑请求已提交", map[string]any{
			"endpoint":         "/v1/images/edits",
			"input_images":     len(req.Images),
			"requested_format": requestedFormat,
			"quota_reserved":   receipt.Total,
		})
	}
	result, err := s.upstream.EditImage(ctx, req)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		s.refundImageQuota(r.Context(), identity, receipt)
		_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskError, taskPhaseFinished, jsonData([]any{}), err.Error())
		s.addImageTaskEventContext(r.Context(), identity, task, "error", "同步图片编辑失败", map[string]any{
			"endpoint":        "/v1/images/edits",
			"status":          taskError,
			"phase":           taskPhaseFinished,
			"duration_ms":     duration,
			"input_images":    len(req.Images),
			"quota_reserved":  receipt.Total,
			"quota_used":      0,
			"quota_refund":    receipt.Total,
			"available_quota": user.AvailableQuota,
			"error":           err.Error(),
			"attempts":        logAttempts(ctx),
		})
		log.Printf("image_edit failed user=%s model=%s duration_ms=%d err=%v", identity.ID, req.Model, duration, err)
		s.logCall(r, identity, "/v1/images/edits", req.Model, "failed", err.Error(), map[string]any{
			"task_id":          taskID,
			"duration_ms":      duration,
			"requested_format": requestedFormat,
			"size":             req.Size,
			"n":                req.N,
			"requested_count":  req.N,
			"input_images":     len(req.Images),
			"quota_reserved":   receipt.Total,
			"quota_used":       0,
			"available_quota":  user.AvailableQuota,
			"attempts":         logAttempts(ctx),
		})
		s.writeUpstreamError(w, err)
		return
	}
	saved := s.persistImageResults(r, result, req.Prompt, identity.ID)
	shapeImageResponseForClient(result, requestedFormat)
	count := imageResultCount(result)
	finalUser := user
	if updatedUser, ok := s.addImageQuotaUsage(r.Context(), identity, count); ok {
		finalUser = updatedUser
	}
	if refund := receipt.Total - count; refund > 0 {
		if updatedUser, ok := s.refundImageQuotaWithResult(r.Context(), identity, quotaRefundPortion(receipt, refund)); ok {
			finalUser = updatedUser
		}
	}
	_ = s.store.UpdateImageTask(r.Context(), identity.ID, taskID, taskSuccess, taskPhaseFinished, jsonData(result["data"]), "")
	s.addImageTaskEventContext(r.Context(), identity, task, "success", "同步图片编辑成功", map[string]any{
		"endpoint":         "/v1/images/edits",
		"status":           taskSuccess,
		"phase":            taskPhaseFinished,
		"duration_ms":      duration,
		"requested_format": requestedFormat,
		"input_images":     len(req.Images),
		"items":            count,
		"archived":         saved,
		"quota_reserved":   receipt.Total,
		"quota_used":       count,
		"quota_refund":     maxInt(0, receipt.Total-count),
		"available_quota":  finalUser.AvailableQuota,
		"attempts":         logAttempts(ctx),
	})
	log.Printf("image_edit success user=%s model=%s items=%d archived=%d duration_ms=%d", identity.ID, req.Model, count, saved, duration)
	s.logCall(r, identity, "/v1/images/edits", req.Model, "success", "", map[string]any{
		"task_id":          taskID,
		"duration_ms":      duration,
		"requested_format": requestedFormat,
		"size":             req.Size,
		"n":                req.N,
		"requested_count":  req.N,
		"input_images":     len(req.Images),
		"items":            count,
		"archived":         saved,
		"quota_reserved":   receipt.Total,
		"quota_used":       count,
		"available_quota":  finalUser.AvailableQuota,
		"attempts":         logAttempts(ctx),
	})
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	s.handleJSONOrStreamUpstream(w, r, "/v1/chat/completions", s.upstream.ChatCompletions, s.upstream.StreamChatCompletions, false)
}

func (s *Server) handleLegacyComplete(w http.ResponseWriter, r *http.Request) {
	s.handleJSONOrStreamUpstream(w, r, "/v1/complete", s.upstream.ChatCompletions, s.upstream.StreamChatCompletions, false)
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.handleJSONOrStreamUpstream(w, r, "/v1/responses", s.upstream.Responses, s.upstream.StreamResponses, true)
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	s.handleJSONOrStreamUpstream(w, r, "/v1/messages", s.upstream.AnthropicMessages, s.upstream.StreamAnthropicMessages, true)
}

func (s *Server) handleJSONUpstream(
	w http.ResponseWriter,
	r *http.Request,
	endpoint string,
	handler func(context.Context, map[string]any) (map[string]any, error),
) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var req map[string]any
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	model := "auto"
	if value, ok := req["model"].(string); ok && strings.TrimSpace(value) != "" {
		model = value
	}
	model, err := s.enforceGeneralRequestModel(r.Context(), model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_model", err.Error())
		return
	}
	req["model"] = model
	result, err := handler(r.Context(), req)
	if err != nil {
		s.logCall(r, identity, endpoint, model, "failed", err.Error())
		s.writeUpstreamError(w, err)
		return
	}
	s.logCall(r, identity, endpoint, model, "success", "")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleJSONOrStreamUpstream(
	w http.ResponseWriter,
	r *http.Request,
	endpoint string,
	jsonHandler func(context.Context, map[string]any) (map[string]any, error),
	streamHandler func(context.Context, map[string]any, func(map[string]any) error) error,
	namedEvents bool,
) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var req map[string]any
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	model := stringFromAny(req["model"], "auto")
	model, err := s.enforceGeneralRequestModel(r.Context(), model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_model", err.Error())
		return
	}
	req["model"] = model
	requestBodyText := requestText(req)
	if !s.checkContentPolicy(w, r, identity, endpoint, model, requestBodyText, requestBodyText) {
		return
	}
	if !boolFromAny(req["stream"]) {
		start := time.Now()
		result, err := jsonHandler(r.Context(), req)
		duration := time.Since(start).Milliseconds()
		if err != nil {
			s.logCall(r, identity, endpoint, model, "failed", err.Error(), map[string]any{"duration_ms": duration})
			s.writeUpstreamError(w, err)
			return
		}
		s.logCall(r, identity, endpoint, model, "success", "", map[string]any{"duration_ms": duration})
		writeJSON(w, http.StatusOK, result)
		return
	}
	start := time.Now()
	if err := s.streamJSONEvents(w, r, req, streamHandler, namedEvents); err != nil {
		s.logCall(r, identity, endpoint, model, "failed", err.Error(), map[string]any{"duration_ms": time.Since(start).Milliseconds(), "stream": true})
		return
	}
	s.logCall(r, identity, endpoint, model, "success", "", map[string]any{"duration_ms": time.Since(start).Milliseconds(), "stream": true})
}

func (s *Server) streamJSONEvents(
	w http.ResponseWriter,
	r *http.Request,
	req map[string]any,
	streamHandler func(context.Context, map[string]any, func(map[string]any) error) error,
	namedEvents bool,
) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming is not supported by this response writer")
		return fmt.Errorf("streaming is not supported")
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	err := streamHandler(r.Context(), req, func(event map[string]any) error {
		if namedEvents {
			if eventType, ok := event["type"].(string); ok && eventType != "" {
				if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
					return err
				}
			}
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if err != nil {
		writeStreamError(w, flusher, err, namedEvents)
		return err
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeStreamError(w http.ResponseWriter, flusher http.Flusher, err error, namedEvents bool) {
	event := map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error"}}
	if namedEvents {
		_, _ = fmt.Fprint(w, "event: error\n")
	}
	payload, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	flusher.Flush()
}

func truncateForLog(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max]) + "..."
}

func parseImageEditPayload(w http.ResponseWriter, r *http.Request) (ImageEditPayload, bool) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return ImageEditPayload{}, false
	}
	req := ImageEditPayload{
		Prompt:         strings.TrimSpace(r.FormValue("prompt")),
		Model:          strings.TrimSpace(r.FormValue("model")),
		Size:           strings.TrimSpace(r.FormValue("size")),
		ResponseFormat: strings.TrimSpace(r.FormValue("response_format")),
		N:              1,
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.ResponseFormat == "" {
		req.ResponseFormat = "b64_json"
	}
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("n"))); err == nil && n > 0 {
		req.N = n
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required")
		return ImageEditPayload{}, false
	}
	files := append(r.MultipartForm.File["image"], r.MultipartForm.File["image[]"]...)
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "image file is required")
		return ImageEditPayload{}, false
	}
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return ImageEditPayload{}, false
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return ImageEditPayload{}, false
		}
		req.Images = append(req.Images, UploadImage{
			Name:        header.Filename,
			ContentType: header.Header.Get("Content-Type"),
			Data:        data,
		})
	}
	return req, true
}

func (s *Server) writeUpstreamError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUpstreamNotImplemented) {
		writeError(w, http.StatusNotImplemented, "upstream_not_implemented", "this upstream route is not implemented yet")
		return
	}
	writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
}

func requestText(req map[string]any) string {
	return strings.Join(requestTextParts(req["prompt"], req["messages"], req["input"], req["instructions"], req["system"], req["tools"]), "\n")
}

func requestTextParts(values ...any) []string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(textFromAny(value))
		if text != "" {
			parts = append(parts, text)
		}
	}
	return parts
}

func textFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(textFromAny(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		parts := requestTextParts(typed["text"], typed["input_text"], typed["content"], typed["input"], typed["instructions"], typed["system"], typed["prompt"])
		if len(parts) == 0 {
			payload, _ := json.Marshal(typed)
			return string(payload)
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func (s *Server) logCall(r *http.Request, identity Identity, endpoint string, model string, status string, callErr string, extra ...map[string]any) {
	detail := map[string]any{
		"subject_id": identity.ID,
		"key_id":     identity.KeyID,
		"name":       identity.Name,
		"role":       identity.Role,
		"endpoint":   endpoint,
		"model":      model,
		"status":     status,
	}
	if callErr != "" {
		detail["error"] = callErr
	}
	for _, fields := range extra {
		for key, value := range fields {
			detail[key] = value
		}
	}
	payload, _ := json.Marshal(detail)
	_ = s.store.AddLog(r.Context(), domain.SystemLog{
		ID:      randomLogID(),
		Time:    time.Now().UTC(),
		Type:    "call",
		Summary: endpoint,
		Detail:  payload,
	})
}
