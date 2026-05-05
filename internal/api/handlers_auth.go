package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type userCreateRequest struct {
	Email    string      `json:"email"`
	Name     string      `json:"name"`
	Password string      `json:"password"`
	Role     domain.Role `json:"role"`
}

type userUpdateRequest struct {
	Email    *string      `json:"email"`
	Name     *string      `json:"name"`
	Password *string      `json:"password"`
	Role     *domain.Role `json:"role"`
	Status   *string      `json:"status"`
}

type keyCreateRequest struct {
	Name   string `json:"name"`
	UserID string `json:"user_id"`
}

type keyUpdateRequest struct {
	Name    *string `json:"name"`
	Enabled *bool   `json:"enabled"`
	Key     *string `json:"key"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if identity, ok := s.identityFromRequest(r); ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"version":    s.cfg.AppVersion,
			"role":       identity.Role,
			"subject_id": identity.ID,
			"name":       identity.Name,
		})
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
		return
	}
	limitKey := remoteAddr(r) + "|" + strings.ToLower(strings.TrimSpace(req.Email))
	if !s.limiter.Allow(limitKey, time.Now().UTC()) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	user, err := s.store.GetUserByEmail(r.Context(), req.Email)
	if err != nil || user.Status != domain.UserStatusActive || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	token, expiresAt, err := s.sessions.Sign(user.ID, user.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	_ = s.store.TouchUserLogin(r.Context(), user.ID, time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"version":    s.cfg.AppVersion,
		"role":       user.Role,
		"subject_id": user.ID,
		"name":       user.Name,
		"token":      token,
		"expires_at": expiresAt,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	if count > 0 && !s.cfg.AllowPublicRegistration {
		writeError(w, http.StatusForbidden, "registration_disabled", "public registration is disabled")
		return
	}
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	role := domain.RoleUser
	if count == 0 {
		role = domain.RoleAdmin
	}
	user, err := s.createUser(r.Context(), req.Email, req.Name, req.Password, role)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_user_failed", err.Error())
		return
	}
	token, expiresAt, err := s.sessions.Sign(user.ID, user.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "token": token, "expires_at": expiresAt})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	user, err := s.store.GetUserByID(r.Context(), identity.ID)
	if err != nil {
		if identity.AuthType == "legacy" {
			writeJSON(w, http.StatusOK, map[string]any{"identity": identity})
			return
		}
		writeError(w, storageStatus(err), "user_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identity": identity, "user": user})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req userCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Role == "" {
		req.Role = domain.RoleUser
	}
	user, err := s.createUser(r.Context(), req.Email, req.Name, req.Password, req.Role)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_user_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": user})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req userUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	update := storage.UserUpdate{Email: req.Email, Name: req.Name, Role: req.Role}
	if req.Password != nil {
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_password", err.Error())
			return
		}
		update.PasswordHash = &hash
	}
	if req.Status != nil {
		status := domain.UserStatus(strings.TrimSpace(*req.Status))
		update.Status = &status
	}
	user, err := s.store.UpdateUser(r.Context(), r.PathValue("user_id"), update)
	if err != nil {
		writeError(w, storageStatus(err), "update_user_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": user})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	status := domain.UserStatusDeleted
	user, err := s.store.UpdateUser(r.Context(), r.PathValue("user_id"), storage.UserUpdate{Status: &status})
	if err != nil {
		writeError(w, storageStatus(err), "delete_user_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": user})
}

func (s *Server) handleMyAPIKeys(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListAPIKeys(r.Context(), identity.ID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": publicKeys(items)})
}

func (s *Server) handleCreateMyAPIKey(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var req keyCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	item, raw, err := s.createAPIKey(r.Context(), identity.ID, identity.Role, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": publicKey(item), "key": raw})
}

func (s *Server) handleUpdateMyAPIKey(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	s.updateAPIKey(w, r, r.PathValue("key_id"), identity.ID)
}

func (s *Server) handleDeleteMyAPIKey(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAPIKey(r.Context(), r.PathValue("key_id"), identity.ID); err != nil {
		writeError(w, storageStatus(err), "delete_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLegacyListUserKeys(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	items, err := s.store.ListAPIKeys(r.Context(), "", string(domain.RoleUser))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": publicKeys(items)})
}

func (s *Server) handleLegacyCreateUserKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req keyCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		user, err := s.ensureDefaultAPIUser(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "default_user_failed", err.Error())
			return
		}
		userID = user.ID
	}
	item, raw, err := s.createAPIKey(r.Context(), userID, domain.RoleUser, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_key_failed", err.Error())
		return
	}
	items, _ := s.store.ListAPIKeys(r.Context(), "", string(domain.RoleUser))
	writeJSON(w, http.StatusCreated, map[string]any{"item": publicKey(item), "key": raw, "items": publicKeys(items)})
}

func (s *Server) handleLegacyUpdateUserKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	s.updateAPIKey(w, r, r.PathValue("key_id"), "")
}

func (s *Server) handleLegacyDeleteUserKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if err := s.store.DeleteAPIKey(r.Context(), r.PathValue("key_id"), ""); err != nil {
		writeError(w, storageStatus(err), "delete_key_failed", err.Error())
		return
	}
	items, _ := s.store.ListAPIKeys(r.Context(), "", string(domain.RoleUser))
	writeJSON(w, http.StatusOK, map[string]any{"items": publicKeys(items)})
}

func (s *Server) updateAPIKey(w http.ResponseWriter, r *http.Request, keyID string, userID string) {
	var req keyUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	update := storage.APIKeyUpdate{Name: req.Name, Enabled: req.Enabled}
	if req.Key != nil {
		hash := auth.HashAPIKey(*req.Key)
		update.KeyHash = &hash
	}
	item, err := s.store.UpdateAPIKey(r.Context(), keyID, userID, update)
	if err != nil {
		writeError(w, storageStatus(err), "update_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": publicKey(item)})
}

func (s *Server) createUser(ctx context.Context, email string, name string, password string, role domain.Role) (domain.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if email == "" {
		return domain.User{}, errors.New("email is required")
	}
	if name == "" {
		name = email
	}
	if role != domain.RoleAdmin && role != domain.RoleUser {
		return domain.User{}, errors.New("invalid role")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return domain.User{}, err
	}
	now := time.Now().UTC()
	user := domain.User{
		ID:           auth.RandomID(18),
		Email:        email,
		Name:         name,
		PasswordHash: hash,
		Role:         role,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.CreateUser(ctx, user); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Server) ensureDefaultAPIUser(ctx context.Context) (domain.User, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return domain.User{}, err
	}
	for _, user := range users {
		if user.Role == domain.RoleUser && user.Email == "api-user@local" {
			return user, nil
		}
	}
	return s.createUser(ctx, "api-user@local", "API User", auth.RandomID(18), domain.RoleUser)
}

func (s *Server) createAPIKey(ctx context.Context, userID string, role domain.Role, name string) (domain.APIKey, string, error) {
	if _, err := s.store.GetUserByID(ctx, userID); err != nil {
		return domain.APIKey{}, "", err
	}
	raw := auth.NewAPIKey()
	if strings.TrimSpace(name) == "" {
		name = "API Key"
	}
	item := domain.APIKey{
		ID:        auth.RandomID(12),
		UserID:    userID,
		Name:      strings.TrimSpace(name),
		Role:      role,
		KeyHash:   auth.HashAPIKey(raw),
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateAPIKey(ctx, item); err != nil {
		return domain.APIKey{}, "", err
	}
	return item, raw, nil
}

func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func publicKeys(items []domain.APIKey) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, publicKey(item))
	}
	return result
}

func publicKey(item domain.APIKey) map[string]any {
	return map[string]any{
		"id":           item.ID,
		"user_id":      item.UserID,
		"name":         item.Name,
		"role":         item.Role,
		"enabled":      item.Enabled,
		"created_at":   item.CreatedAt,
		"last_used_at": item.LastUsedAt,
	}
}
