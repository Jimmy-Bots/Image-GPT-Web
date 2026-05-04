package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.N == 0 {
		req.N = 1
	}
	if req.ResponseFormat == "" {
		req.ResponseFormat = "b64_json"
	}
	result, err := s.upstream.GenerateImage(r.Context(), req)
	if err != nil {
		s.logCall(r, identity, "/v1/images/generations", req.Model, "failed", err.Error())
		s.writeUpstreamError(w, err)
		return
	}
	s.logCall(r, identity, "/v1/images/generations", req.Model, "success", "")
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
	result, err := s.upstream.EditImage(r.Context(), req)
	if err != nil {
		s.logCall(r, identity, "/v1/images/edits", req.Model, "failed", err.Error())
		s.writeUpstreamError(w, err)
		return
	}
	s.logCall(r, identity, "/v1/images/edits", req.Model, "success", "")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	s.handleJSONOrStreamUpstream(w, r, "/v1/chat/completions", s.upstream.ChatCompletions, s.upstream.StreamChatCompletions, false)
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.handleJSONOrStreamUpstream(w, r, "/v1/responses", s.upstream.Responses, s.upstream.StreamResponses, true)
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	s.handleJSONUpstream(w, r, "/v1/messages", s.upstream.AnthropicMessages)
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
	if !boolFromAny(req["stream"]) {
		result, err := jsonHandler(r.Context(), req)
		if err != nil {
			s.logCall(r, identity, endpoint, model, "failed", err.Error())
			s.writeUpstreamError(w, err)
			return
		}
		s.logCall(r, identity, endpoint, model, "success", "")
		writeJSON(w, http.StatusOK, result)
		return
	}
	if err := s.streamJSONEvents(w, r, req, streamHandler, namedEvents); err != nil {
		s.logCall(r, identity, endpoint, model, "failed", err.Error())
		return
	}
	s.logCall(r, identity, endpoint, model, "success", "")
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

func (s *Server) logCall(r *http.Request, identity Identity, endpoint string, model string, status string, callErr string) {
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
	payload, _ := json.Marshal(detail)
	_ = s.store.AddLog(r.Context(), domain.SystemLog{
		ID:      randomLogID(),
		Time:    time.Now().UTC(),
		Type:    "call",
		Summary: endpoint,
		Detail:  payload,
	})
}
