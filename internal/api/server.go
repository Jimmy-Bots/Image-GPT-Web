package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
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
}

func NewServer(cfg config.Config, store *storage.Store) (*Server, error) {
	pool := NewAccountPool(store, cfg.ImageAccountConcurrency)
	s := &Server{
		cfg:      cfg,
		store:    store,
		sessions: auth.NewSessionSigner(cfg.SessionSecret, time.Duration(cfg.SessionTTLHours)*time.Hour),
		pool:     pool,
	}
	s.upstream = NewChatGPTUpstream(store, pool, cfg.ProxyURL)
	if err := s.bootstrap(context.Background()); err != nil {
		return nil, err
	}
	s.tasks = NewTaskQueue(store, s.upstream, cfg.ImageWorkerCount, cfg.ImageQueueSize)
	return s, nil
}

func (s *Server) Close() {
	if s.tasks != nil {
		s.tasks.Close()
	}
}

func (s *Server) SetUpstream(upstream Upstream) {
	if upstream != nil {
		s.upstream = upstream
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
	mux.HandleFunc("PATCH /api/users/{user_id}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /api/users/{user_id}", s.handleDeleteUser)
	mux.HandleFunc("GET /api/auth/users", s.handleLegacyListUserKeys)
	mux.HandleFunc("POST /api/auth/users", s.handleLegacyCreateUserKey)
	mux.HandleFunc("POST /api/auth/users/{key_id}", s.handleLegacyUpdateUserKey)
	mux.HandleFunc("DELETE /api/auth/users/{key_id}", s.handleLegacyDeleteUserKey)
	mux.HandleFunc("GET /api/accounts", s.handleListAccounts)
	mux.HandleFunc("POST /api/accounts", s.handleCreateAccounts)
	mux.HandleFunc("DELETE /api/accounts", s.handleDeleteAccounts)
	mux.HandleFunc("POST /api/accounts/update", s.handleUpdateAccount)
	mux.HandleFunc("POST /api/accounts/refresh", s.handleRefreshAccounts)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("POST /api/settings", s.handleSaveSettings)
	mux.HandleFunc("GET /api/storage/info", s.handleStorageInfo)
	mux.HandleFunc("GET /api/logs", s.handleListLogs)
	mux.HandleFunc("POST /api/logs/delete", s.handleDeleteLogs)
	mux.HandleFunc("GET /api/image-tasks", s.handleListImageTasks)
	mux.HandleFunc("POST /api/image-tasks/generations", s.handleCreateGenerationTask)
	mux.HandleFunc("POST /api/image-tasks/edits", s.handleCreateEditTask)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/images/generations", s.handleImageGenerations)
	mux.HandleFunc("POST /v1/images/edits", s.handleImageEdits)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/messages", s.handleAnthropicMessages)
	return s.withRecover(s.withCORS(mux))
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
		ID:           auth.RandomID(18),
		Email:        s.cfg.AdminEmail,
		Name:         "Administrator",
		PasswordHash: passwordHash,
		Role:         domain.RoleAdmin,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.CreateUser(ctx, admin); err != nil {
		return err
	}
	if s.cfg.LegacyAdminKey != "" {
		item := domain.APIKey{
			ID:        auth.RandomID(12),
			UserID:    admin.ID,
			Name:      "Legacy admin key",
			Role:      domain.RoleAdmin,
			KeyHash:   auth.HashAPIKey(s.cfg.LegacyAdminKey),
			Enabled:   true,
			CreatedAt: now,
		}
		if err := s.store.CreateAPIKey(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) identityFromRequest(r *http.Request) (Identity, bool) {
	header := r.Header.Get("Authorization")
	if header == "" && r.Header.Get("x-api-key") != "" {
		header = "Bearer " + r.Header.Get("x-api-key")
	}
	raw, err := auth.ExtractBearer(header)
	if err != nil {
		return Identity{}, false
	}
	if s.cfg.LegacyAdminKey != "" && raw == s.cfg.LegacyAdminKey {
		return Identity{ID: "legacy-admin", Name: "Legacy admin", Role: domain.RoleAdmin, AuthType: "legacy"}, true
	}
	if claims, ok := s.sessions.Verify(raw); ok {
		user, err := s.store.GetUserByID(r.Context(), claims.SubjectID)
		if err != nil || user.Status != domain.UserStatusActive {
			return Identity{}, false
		}
		return Identity{ID: user.ID, Name: user.Name, Role: user.Role, AuthType: "session"}, true
	}
	key, err := s.store.FindAPIKeyByHash(r.Context(), auth.HashAPIKey(raw))
	if err != nil || !key.Enabled {
		return Identity{}, false
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.store.TouchAPIKey(ctx, key.ID, time.Now().UTC())
	}()
	return Identity{ID: key.UserID, KeyID: key.ID, Name: key.Name, Role: key.Role, AuthType: "api_key"}, true
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
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
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
