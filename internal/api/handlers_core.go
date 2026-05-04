package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

type accountCreateRequest struct {
	Tokens []string `json:"tokens"`
}

type accountDeleteRequest struct {
	Tokens []string `json:"tokens"`
}

type accountRefreshRequest struct {
	AccessTokens []string `json:"access_tokens"`
}

type accountUpdateRequest struct {
	AccessToken string  `json:"access_token"`
	Type        *string `json:"type"`
	Status      *string `json:"status"`
	Quota       *int    `json:"quota"`
}

type logsDeleteRequest struct {
	IDs []string `json:"ids"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB().PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unhealthy", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.cfg.AppVersion})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": s.cfg.AppVersion})
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	items, err := s.store.ListAccounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req accountCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	added := 0
	skipped := 0
	for _, token := range compactStrings(req.Tokens) {
		created, err := s.store.UpsertAccountToken(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
			return
		}
		if created {
			added++
		} else {
			skipped++
		}
	}
	items, _ := s.store.ListAccounts(r.Context())
	s.addLog(r, "account", "新增账号", map[string]any{"added": added, "skipped": skipped})
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped, "items": items})
}

func (s *Server) handleDeleteAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req accountDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	removed, err := s.store.DeleteAccounts(r.Context(), compactStrings(req.Tokens))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	items, _ := s.store.ListAccounts(r.Context())
	s.addLog(r, "account", "删除账号", map[string]any{"removed": removed})
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "items": items})
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req accountUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	item, err := s.store.UpdateAccount(
		r.Context(),
		strings.TrimSpace(req.AccessToken),
		storage.AccountUpdate{Type: req.Type, Status: req.Status, Quota: req.Quota},
	)
	if err != nil {
		writeError(w, storageStatus(err), "update_account_failed", err.Error())
		return
	}
	items, _ := s.store.ListAccounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "items": items})
}

func (s *Server) handleRefreshAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req accountRefreshRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	tokens := compactStrings(req.AccessTokens)
	items, _ := s.store.ListAccounts(r.Context())
	if len(tokens) == 0 {
		for _, item := range items {
			tokens = append(tokens, item.AccessToken)
		}
	}
	refreshed, errorsList := s.upstream.RefreshAccounts(r.Context(), tokens)
	items, _ = s.store.ListAccounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"refreshed": refreshed, "errors": errorsList, "items": items})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": settings})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var settings map[string]any
	if err := decodeJSON(r, &settings); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	saved, err := s.store.SaveSettings(r.Context(), settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": saved})
}

func (s *Server) handleStorageInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	status := "healthy"
	if err := s.store.DB().PingContext(r.Context()); err != nil {
		status = "unhealthy: " + err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"backend": map[string]any{
			"type":        "sqlite",
			"description": "SQLite WAL storage",
			"path":        s.cfg.DatabasePath,
		},
		"health": map[string]any{"status": status},
	})
}

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	items, err := s.store.ListLogs(r.Context(), strings.TrimSpace(r.URL.Query().Get("type")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDeleteLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req logsDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	removed, err := s.store.DeleteLogs(r.Context(), compactStrings(req.IDs))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) addLog(r *http.Request, logType string, summary string, detail map[string]any) {
	payload, _ := json.Marshal(detail)
	_ = s.store.AddLog(r.Context(), domain.SystemLog{
		ID:      randomLogID(),
		Time:    time.Now().UTC(),
		Type:    logType,
		Summary: summary,
		Detail:  payload,
	})
}

func compactStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
