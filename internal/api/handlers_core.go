package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Tokens    []string `json:"tokens"`
	TokenRefs []string `json:"token_refs"`
}

type accountRefreshRequest struct {
	AccessTokens []string `json:"access_tokens"`
	TokenRefs    []string `json:"token_refs"`
}

type accountUpdateRequest struct {
	AccessToken string  `json:"access_token"`
	TokenRef    string  `json:"token_ref"`
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
	writeJSON(w, http.StatusOK, map[string]any{"items": publicAccounts(items)})
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
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped, "items": publicAccounts(items)})
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
	tokens, err := s.resolveAccountRefs(r.Context(), req.Tokens, req.TokenRefs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	removed, err := s.store.DeleteAccounts(r.Context(), tokens)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	items, _ := s.store.ListAccounts(r.Context())
	s.addLog(r, "account", "删除账号", map[string]any{"removed": removed})
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "items": publicAccounts(items)})
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
	token, err := s.resolveAccountRef(r.Context(), req.AccessToken, req.TokenRef)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	item, err := s.store.UpdateAccount(
		r.Context(),
		token,
		storage.AccountUpdate{Type: req.Type, Status: req.Status, Quota: req.Quota},
	)
	if err != nil {
		writeError(w, storageStatus(err), "update_account_failed", err.Error())
		return
	}
	items, _ := s.store.ListAccounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"item": publicAccount(item), "items": publicAccounts(items)})
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
	tokens, err := s.resolveAccountRefs(r.Context(), req.AccessTokens, req.TokenRefs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	items, _ := s.store.ListAccounts(r.Context())
	if len(tokens) == 0 {
		for _, item := range items {
			tokens = append(tokens, item.AccessToken)
		}
	}
	refreshed, errorsList := s.upstream.RefreshAccounts(r.Context(), tokens)
	items, _ = s.store.ListAccounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"refreshed": refreshed, "errors": publicRefreshErrors(errorsList), "items": publicAccounts(items)})
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
	ids := compactStrings(strings.Split(r.URL.Query().Get("ids"), ","))
	includeDetail := len(ids) > 0 || boolFromAny(r.URL.Query().Get("detail"))
	items, err := s.store.ListLogs(r.Context(), strings.TrimSpace(r.URL.Query().Get("type")), ids, includeDetail)
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

func (s *Server) resolveAccountRefs(ctx context.Context, tokens []string, refs []string) ([]string, error) {
	out := compactStrings(tokens)
	seen := make(map[string]struct{}, len(out))
	for _, token := range out {
		seen[token] = struct{}{}
	}
	for _, ref := range compactStrings(refs) {
		token, err := s.resolveAccountRef(ctx, "", ref)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[token]; ok {
			continue
		}
		out = append(out, token)
		seen[token] = struct{}{}
	}
	return out, nil
}

func (s *Server) resolveAccountRef(ctx context.Context, token string, ref string) (string, error) {
	token = strings.TrimSpace(token)
	if token != "" {
		return token, nil
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	items, err := s.store.ListAccounts(ctx)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if accountTokenRef(item.AccessToken) == ref {
			return item.AccessToken, nil
		}
	}
	return "", storage.ErrNotFound
}

func publicAccounts(items []domain.Account) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, publicAccount(item))
	}
	return out
}

func publicAccount(item domain.Account) map[string]any {
	return map[string]any{
		"token_ref":           accountTokenRef(item.AccessToken),
		"access_token_masked": maskToken(item.AccessToken),
		"type":                item.Type,
		"status":              item.Status,
		"quota":               item.Quota,
		"image_quota_unknown": item.ImageQuotaUnknown,
		"email":               item.Email,
		"user_id":             item.UserID,
		"limits_progress":     item.LimitsProgress,
		"default_model_slug":  item.DefaultModelSlug,
		"restore_at":          item.RestoreAt,
		"success":             item.Success,
		"fail":                item.Fail,
		"last_used_at":        item.LastUsedAt,
		"created_at":          item.CreatedAt,
		"updated_at":          item.UpdatedAt,
	}
}

func publicRefreshErrors(items []map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		next := make(map[string]string, len(item)+1)
		for key, value := range item {
			if key == "access_token" {
				next[key] = maskToken(value)
				next["token_ref"] = accountTokenRef(value)
				continue
			}
			next[key] = value
		}
		out = append(out, next)
	}
	return out
}

func accountTokenRef(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:24]
}
