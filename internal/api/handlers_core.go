package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

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
	Password    *string `json:"password"`
	MaxConcurrency *int `json:"max_concurrency"`
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
	query := storage.AccountListQuery{
		Page:       queryInt(r, "page", 1),
		PageSize:   queryInt(r, "page_size", 25),
		Query:      strings.TrimSpace(r.URL.Query().Get("query")),
		Status:     strings.TrimSpace(r.URL.Query().Get("status")),
		Type:       strings.TrimSpace(r.URL.Query().Get("account_type")),
		ActiveOnly: boolFromAny(r.URL.Query().Get("active_only")),
	}
	items, total, summary, err := s.store.ListAccountsPage(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	poolStats := s.pool.Stats(r.Context())
	if query.ActiveOnly {
		filtered := make([]domain.Account, 0, len(items))
		for _, item := range items {
			if stat := poolStats.Accounts[item.AccessToken]; stat.ActiveRequests > 0 {
				filtered = append(filtered, item)
			}
		}
		items = filtered
		total = 0
		summary.Total = 0
		summary.Normal = 0
		summary.Success = 0
		summary.Fail = 0
		summary.QuotaTotal = 0
		summary.QuotaUnknown = false
		summary.QuotaUnlimited = false
		summary.ActiveRequests = 0
		summary.TotalConcurrency = 0
		allItems, err := s.store.ListAccounts(r.Context())
		if err == nil {
			filteredAll := make([]domain.Account, 0, len(allItems))
			for _, item := range allItems {
				if !accountMatchesQuery(item, query) {
					continue
				}
				if stat := poolStats.Accounts[item.AccessToken]; stat.ActiveRequests > 0 {
					item.ActiveRequests = stat.ActiveRequests
					item.AllowedConcurrency = stat.AllowedConcurrency
					filteredAll = append(filteredAll, item)
				}
			}
			total = len(filteredAll)
			summary.Total = len(filteredAll)
			for _, item := range filteredAll {
				if item.Status == "正常" {
					summary.Normal++
					if item.ImageQuotaUnknown {
						summary.QuotaUnknown = true
					} else {
						summary.QuotaTotal += item.Quota
					}
					if strings.EqualFold(item.Type, "pro") || strings.EqualFold(item.Type, "prolite") {
						summary.QuotaUnlimited = true
					}
				}
				summary.Success += item.Success
				summary.Fail += item.Fail
				summary.ActiveRequests += item.ActiveRequests
				summary.TotalConcurrency += item.AllowedConcurrency
			}
			page, pageSize := query.Page, query.PageSize
			if page < 1 {
				page = 1
			}
			if pageSize < 1 {
				pageSize = 25
			}
			start := (page - 1) * pageSize
			if start > len(filteredAll) {
				start = len(filteredAll)
			}
			end := start + pageSize
			if end > len(filteredAll) {
				end = len(filteredAll)
			}
			items = filteredAll[start:end]
		}
	} else {
		summary.ActiveRequests = poolStats.ActiveRequests
		summary.TotalConcurrency = poolStats.TotalConcurrency
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     publicAccounts(items, poolStats.Accounts),
		"total":     total,
		"page":      query.Page,
		"page_size": query.PageSize,
		"summary":   summary,
	})
}

func accountMatchesQuery(item domain.Account, query storage.AccountListQuery) bool {
	if query.Status != "" && item.Status != query.Status {
		return false
	}
	if query.Type != "" && item.Type != query.Type {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(query.Query))
	if text == "" {
		return true
	}
	return strings.Contains(strings.ToLower(item.Email), text) ||
		strings.Contains(strings.ToLower(item.Password), text) ||
		strings.Contains(strings.ToLower(item.Type), text) ||
		strings.Contains(strings.ToLower(item.Status), text) ||
		strings.Contains(strings.ToLower(item.DefaultModelSlug), text)
}

func (s *Server) handleGetAccountRefreshStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.autoRef == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": accountAutoRefreshState{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": s.autoRef.Status(r.Context())})
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
	s.addLog(r, "account", "删除账号", map[string]any{"removed": removed})
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
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
		storage.AccountUpdate{Type: req.Type, Status: req.Status, Quota: req.Quota, Password: req.Password, MaxConcurrency: req.MaxConcurrency},
	)
	if err != nil {
		writeError(w, storageStatus(err), "update_account_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": publicAccount(item, s.pool.Stats(r.Context()).Accounts)})
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
	if len(tokens) == 0 {
		items, _ := s.store.ListAccounts(r.Context())
		for _, item := range items {
			tokens = append(tokens, item.AccessToken)
		}
	}
	refreshed, errorsList := s.upstream.RefreshAccounts(r.Context(), tokens)
	writeJSON(w, http.StatusOK, map[string]any{"refreshed": refreshed, "errors": publicRefreshErrors(errorsList)})
}

func (s *Server) handleRefreshDueAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	intervalMinutes := intMapValue(settings, "refresh_account_interval_minute")
	if intervalMinutes < 1 {
		intervalMinutes = defaultAutoRefreshIntervalMinutes
	}
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	tokens := dueRefreshTokens(accounts, intervalMinutes, time.Now())
	refreshed, errorsList := s.upstream.RefreshAccounts(r.Context(), tokens)
	writeJSON(w, http.StatusOK, map[string]any{
		"selected":  len(tokens),
		"refreshed": refreshed,
		"errors":    publicRefreshErrors(errorsList),
	})
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
	if len(ids) > 0 {
		items, err := s.store.ListLogs(r.Context(), strings.TrimSpace(r.URL.Query().Get("type")), ids, includeDetail)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": len(items)})
		return
	}
	query := storage.LogListQuery{
		Page:          queryInt(r, "page", 1),
		PageSize:      queryInt(r, "page_size", 25),
		Query:         strings.TrimSpace(r.URL.Query().Get("query")),
		Type:          strings.TrimSpace(r.URL.Query().Get("type")),
		IncludeDetail: includeDetail,
	}
	items, total, err := s.store.ListLogsPage(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": query.Page, "page_size": query.PageSize})
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

func publicAccounts(items []domain.Account, stats map[string]AccountPoolAccountStats) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, publicAccount(item, stats))
	}
	return out
}

func publicAccount(item domain.Account, stats map[string]AccountPoolAccountStats) map[string]any {
	accountStats := stats[item.AccessToken]
	return map[string]any{
		"token_ref":           accountTokenRef(item.AccessToken),
		"access_token_masked": maskToken(item.AccessToken),
		"password":            item.Password,
		"type":                item.Type,
		"status":              item.Status,
		"quota":               item.Quota,
		"max_concurrency":     item.MaxConcurrency,
		"image_quota_unknown": item.ImageQuotaUnknown,
		"email":               item.Email,
		"user_id":             item.UserID,
		"limits_progress":     item.LimitsProgress,
		"default_model_slug":  item.DefaultModelSlug,
		"restore_at":          item.RestoreAt,
		"success":             item.Success,
		"fail":                item.Fail,
		"active_requests":     accountStats.ActiveRequests,
		"allowed_concurrency": accountStats.AllowedConcurrency,
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

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return fallback
	}
	return number
}
