package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/config"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

type Server struct {
	cfg      config.Config
	store    *storage.Store
	sessions *auth.SessionSigner
	upstream Upstream
	pool     *AccountPool
	tasks    *TaskQueue
	limiter  *loginLimiter
	register *registerManager
	autoRef  *accountAutoRefresher
	backup   *backupManager
}

func NewServer(cfg config.Config, store *storage.Store) (*Server, error) {
	pool := NewAccountPool(store, cfg.ImageAccountConcurrency)
	s := &Server{
		cfg:      cfg,
		store:    store,
		sessions: auth.NewSessionSigner(cfg.SessionSecret, time.Duration(cfg.SessionTTLHours)*time.Hour),
		pool:     pool,
		limiter:  newLoginLimiter(cfg.LoginRateLimitMax, time.Duration(cfg.LoginRateLimitWindowSec)*time.Second),
	}
	s.upstream = NewChatGPTUpstream(store, pool, cfg.ProxyURL)
	if upstreamImpl, ok := s.upstream.(*ChatGPTUpstream); ok {
		upstreamImpl.SetLogWriter(s.addLogContext)
	}
	s.register = newRegisterManager(cfg, store, s.upstream)
	if err := s.bootstrap(context.Background()); err != nil {
		return nil, err
	}
	s.tasks = NewTaskQueue(store, s.upstream, cfg.ImagesDir, cfg.BaseURL, cfg.ImageWorkerCount, cfg.ImageQueueSize)
	s.autoRef = newAccountAutoRefresher(store, s.upstream)
	s.autoRef.SetLogWriter(s.addLogContext)
	s.autoRef.Start()
	s.backup = newBackupManager(cfg, store)
	s.backup.SetLogWriter(s.addLogContext)
	s.backup.Start()
	return s, nil
}

func (s *Server) Close() {
	if s.backup != nil {
		s.backup.Close()
	}
	if s.autoRef != nil {
		s.autoRef.Close()
	}
	if s.tasks != nil {
		s.tasks.Close()
	}
}

func (s *Server) SetUpstream(upstream Upstream) {
	if upstream != nil {
		s.upstream = upstream
		if s.tasks != nil {
			s.tasks.upstream = upstream
		}
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /auth/register", s.handleRegister)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/me/api-keys", s.handleMyAPIKeys)
	mux.HandleFunc("POST /api/me/api-keys", s.handleCreateMyAPIKey)
	mux.HandleFunc("PATCH /api/me/api-keys/{key_id}", s.handleUpdateMyAPIKey)
	mux.HandleFunc("DELETE /api/me/api-keys/{key_id}", s.handleDeleteMyAPIKey)
	mux.HandleFunc("GET /api/users", s.handleListUsers)
	mux.HandleFunc("POST /api/users", s.handleCreateUser)
	mux.HandleFunc("POST /api/users/batch", s.handleBatchUsers)
	mux.HandleFunc("PATCH /api/users/{user_id}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /api/users/{user_id}", s.handleDeleteUser)
	mux.HandleFunc("POST /api/users/{user_id}/api-key/reset", s.handleResetUserAPIKey)
	mux.HandleFunc("GET /api/accounts", s.handleListAccounts)
	mux.HandleFunc("GET /api/accounts/refresh-status", s.handleGetAccountRefreshStatus)
	mux.HandleFunc("DELETE /api/accounts", s.handleDeleteAccounts)
	mux.HandleFunc("POST /api/accounts/update", s.handleUpdateAccount)
	mux.HandleFunc("POST /api/accounts/refresh", s.handleRefreshAccounts)
	mux.HandleFunc("POST /api/accounts/refresh-due", s.handleRefreshDueAccounts)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("POST /api/settings", s.handleSaveSettings)
	mux.HandleFunc("POST /api/settings/mail/test", s.handleSendSMTPTest)
	mux.HandleFunc("GET /api/register/state", s.handleGetRegisterState)
	mux.HandleFunc("GET /api/register/logs", s.handleGetRegisterLogs)
	mux.HandleFunc("POST /api/register/config", s.handleSaveRegisterConfig)
	mux.HandleFunc("POST /api/register/start", s.handleStartRegister)
	mux.HandleFunc("POST /api/register/stop", s.handleStopRegister)
	mux.HandleFunc("POST /api/register/run-once", s.handleRunRegisterOnce)
	mux.HandleFunc("GET /api/backup/state", s.handleGetBackupState)
	mux.HandleFunc("POST /api/backup/run", s.handleRunBackup)
	mux.HandleFunc("GET /api/backup/items", s.handleListBackups)
	mux.HandleFunc("POST /api/backup/delete", s.handleDeleteBackup)
	mux.HandleFunc("GET /api/backup/download", s.handleDownloadBackup)
	mux.HandleFunc("GET /api/storage/info", s.handleStorageInfo)
	mux.HandleFunc("GET /api/logs", s.handleListLogs)
	mux.HandleFunc("POST /api/logs/delete", s.handleDeleteLogs)
	mux.HandleFunc("GET /api/images", s.handleListImages)
	mux.HandleFunc("POST /api/images/delete", s.handleDeleteImages)
	mux.HandleFunc("GET /api/image-tasks", s.handleListImageTasks)
	mux.HandleFunc("POST /api/image-tasks/delete", s.handleDeleteImageTasks)
	mux.HandleFunc("POST /api/image-tasks/generations", s.handleCreateGenerationTask)
	mux.HandleFunc("POST /api/image-tasks/edits", s.handleCreateEditTask)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/images/generations", s.handleImageGenerations)
	mux.HandleFunc("POST /v1/images/edits", s.handleImageEdits)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/complete", s.handleLegacyComplete)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("GET /", s.handleWebIndex)
	mux.HandleFunc("GET /assets/", s.handleWebAsset)
	mux.HandleFunc("GET /images/", s.handleImageAsset)
	return s.withAccessLog(s.withRecover(s.withRequestLimits(s.withSecurityHeaders(s.withCORS(mux)))))
}

func (s *Server) handleWebIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.WebDir, "index.html")
	http.ServeFile(w, r, path)
}

func (s *Server) handleWebAsset(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/assets/")
	path, ok := safeJoin(filepath.Join(s.cfg.WebDir, "assets"), rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleImageAsset(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/images/")
	path, ok := safeJoin(s.cfg.ImagesDir, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func safeJoin(root string, rel string) (string, bool) {
	clean := filepath.Clean("/" + rel)
	if rel == "" || clean == "/" || strings.Contains(clean, "..") {
		return "", false
	}
	return filepath.Join(root, strings.TrimPrefix(clean, "/")), true
}

func (s *Server) bootstrap(ctx context.Context) error {
	count, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	password := strings.TrimSpace(s.cfg.AdminPassword)
	if password == "" {
		password = "change-me-admin-password"
		log.Printf("bootstrap admin password was not configured; using development fallback")
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	admin := domain.User{
		ID:             auth.RandomID(18),
		Email:          s.cfg.AdminEmail,
		Name:           "Administrator",
		PasswordHash:   passwordHash,
		Role:           domain.RoleAdmin,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateUser(ctx, admin); err != nil {
		return err
	}
	if _, _, err := s.ensureUserAPIKey(ctx, admin, "Default API Key", false); err != nil {
		return err
	}
	return nil
}

func (s *Server) sessionIdentityFromRequest(r *http.Request) (Identity, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		if raw := strings.TrimSpace(r.URL.Query().Get("access_token")); raw != "" {
			header = "Bearer " + raw
		}
	}
	raw, err := auth.ExtractBearer(header)
	if err != nil {
		return Identity{}, false
	}
	claims, ok := s.sessions.Verify(raw)
	if !ok {
		return Identity{}, false
	}
	user, err := s.store.GetUserByID(r.Context(), claims.SubjectID)
	if err != nil || user.Status != domain.UserStatusActive {
		return Identity{}, false
	}
	return Identity{ID: user.ID, Name: user.Name, Role: user.Role, AuthType: "session"}, true
}

func (s *Server) identityFromRequest(r *http.Request) (Identity, bool) {
	header := r.Header.Get("Authorization")
	if header == "" && r.Header.Get("x-api-key") != "" {
		header = "Bearer " + r.Header.Get("x-api-key")
	}
	if header == "" {
		if raw := strings.TrimSpace(r.URL.Query().Get("access_token")); raw != "" {
			header = "Bearer " + raw
		}
	}
	raw, err := auth.ExtractBearer(header)
	if err != nil {
		return Identity{}, false
	}
	if s.cfg.LegacyAdminKey != "" && raw == s.cfg.LegacyAdminKey {
		return Identity{ID: "legacy-admin", Name: "Legacy admin", Role: domain.RoleAdmin, AuthType: "legacy"}, true
	}
	if identity, ok := s.sessionIdentityFromRequest(r); ok {
		return identity, true
	}
	key, err := s.store.FindAPIKeyByHash(r.Context(), auth.HashAPIKey(raw))
	if err != nil || !key.Enabled {
		return Identity{}, false
	}
	user, err := s.store.GetUserByID(r.Context(), key.UserID)
	if err != nil || user.Status != domain.UserStatusActive {
		return Identity{}, false
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.store.TouchAPIKey(ctx, key.ID, time.Now().UTC())
	}()
	return Identity{ID: user.ID, KeyID: key.ID, Name: user.Name, Role: user.Role, AuthType: "api_key"}, true
}

func (s *Server) requireIdentity(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	identity, ok := s.identityFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing bearer token")
		return Identity{}, false
	}
	return identity, true
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return Identity{}, false
	}
	if identity.Role != domain.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin role required")
		return Identity{}, false
	}
	return identity, true
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := s.allowedOrigin(r.Header.Get("Origin")); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedOrigin(origin string) string {
	if origin == "" {
		return ""
	}
	for _, allowed := range s.cfg.CORSAllowedOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return allowed
		}
	}
	return ""
}

func (s *Server) withRequestLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.MaxRequestBodyBytes > 0 && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

type accessLogWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *accessLogWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *accessLogWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *accessLogWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &accessLogWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if r.URL.Path == "/healthz" && !s.cfg.DebugLogging() {
			return
		}
		log.Printf(
			"request method=%s path=%s status=%d duration_ms=%d bytes=%d remote=%s",
			r.Method,
			safeLogPath(r),
			status,
			time.Since(start).Milliseconds(),
			recorder.bytes,
			clientIP(r),
		)
	})
}

func safeLogPath(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !r.URL.Query().Has("ids") {
		return path
	}
	return path + "?ids=" + strconv.Itoa(len(strings.Split(r.URL.Query().Get("ids"), ",")))
}

func clientIP(r *http.Request) string {
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return strings.TrimSpace(strings.Split(value, ",")[0])
		}
	}
	return r.RemoteAddr
}

type loginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	entries map[string]loginLimitEntry
}

type loginLimitEntry struct {
	Count      int
	WindowEnds time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	if max < 1 {
		max = 1
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &loginLimiter{max: max, window: window, entries: map[string]loginLimitEntry{}}
}

func (l *loginLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry.WindowEnds.IsZero() || now.After(entry.WindowEnds) {
		l.entries[key] = loginLimitEntry{Count: 1, WindowEnds: now.Add(l.window)}
		return true
	}
	if entry.Count >= l.max {
		return false
	}
	entry.Count++
	l.entries[key] = entry
	return true
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic: %v", recovered)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type Identity struct {
	ID       string      `json:"id"`
	KeyID    string      `json:"key_id,omitempty"`
	Name     string      `json:"name"`
	Role     domain.Role `json:"role"`
	AuthType string      `json:"auth_type"`
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func storageStatus(err error) int {
	if errors.Is(err, storage.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
