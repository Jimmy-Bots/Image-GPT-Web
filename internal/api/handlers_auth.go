package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false,
		Expires:  expiresAt,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerRequest struct {
	Email            string `json:"email"`
	Name             string `json:"name"`
	Password         string `json:"password"`
	VerificationCode string `json:"verification_code"`
}

type registerSendCodeRequest struct {
	Email string `json:"email"`
}

type userCreateRequest struct {
	Email              string      `json:"email"`
	Name               string      `json:"name"`
	Password           string      `json:"password"`
	Role               domain.Role `json:"role"`
	QuotaUnlimited     *bool       `json:"quota_unlimited"`
	PermanentQuota     *int        `json:"permanent_quota"`
	TemporaryQuota     *int        `json:"temporary_quota"`
	TemporaryQuotaDate *string     `json:"temporary_quota_date"`
}

type userCreateResponse struct {
	Item   domain.User    `json:"item"`
	APIKey map[string]any `json:"api_key"`
	Key    string         `json:"key"`
}

type userUpdateRequest struct {
	Email              *string      `json:"email"`
	Name               *string      `json:"name"`
	Password           *string      `json:"password"`
	Role               *domain.Role `json:"role"`
	Status             *string      `json:"status"`
	QuotaUnlimited     *bool        `json:"quota_unlimited"`
	PermanentQuota     *int         `json:"permanent_quota"`
	TemporaryQuota     *int         `json:"temporary_quota"`
	TemporaryQuotaDate *string      `json:"temporary_quota_date"`
	AddPermanentQuota  *int         `json:"add_permanent_quota"`
}

type userBatchRequest struct {
	IDs            []string `json:"ids"`
	Action         string   `json:"action"`
	Status         string   `json:"status"`
	TemporaryQuota *int     `json:"temporary_quota"`
	PermanentQuota *int     `json:"permanent_quota"`
}

func quotaDayString(now time.Time) string {
	return now.Format("2006-01-02")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	clientIP, clientIPs := requestIPInfo(r)
	if identity, ok := s.sessionIdentityFromRequest(r); ok {
		s.addLog(r, "auth", "会话验证成功", map[string]any{
			"status":    "session_ok",
			"user_id":   identity.ID,
			"name":      identity.Name,
			"role":      identity.Role,
			"auth_type": identity.AuthType,
			"ip":        clientIP,
			"ips":       clientIPs,
		})
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
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || strings.TrimSpace(req.Password) == "" {
		s.addLog(r, "auth", "登录失败", map[string]any{
			"status": "bad_request",
			"email":  req.Email,
			"ip":     clientIP,
			"ips":    clientIPs,
			"reason": "missing_email_or_password",
		})
		writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
		return
	}
	limitKey := remoteAddr(r) + "|" + strings.ToLower(strings.TrimSpace(req.Email))
	if !s.limiter.Allow(limitKey, time.Now().UTC()) {
		s.addLog(r, "auth", "登录限流", map[string]any{
			"status": "rate_limited",
			"email":  req.Email,
			"ip":     clientIP,
			"ips":    clientIPs,
		})
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	user, err := s.store.GetUserByEmail(r.Context(), req.Email)
	if err != nil || user.Status != domain.UserStatusActive || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		status := "invalid_credentials"
		if err == nil && user.Status != domain.UserStatusActive {
			status = "user_not_active"
		}
		s.addLog(r, "auth", "登录失败", map[string]any{
			"status": status,
			"email":  req.Email,
			"ip":     clientIP,
			"ips":    clientIPs,
		})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	token, expiresAt, err := s.sessions.Sign(user.ID, user.Role)
	if err != nil {
		s.addLog(r, "auth", "登录失败", map[string]any{
			"status":  "session_error",
			"email":   req.Email,
			"user_id": user.ID,
			"role":    user.Role,
			"ip":      clientIP,
			"ips":     clientIPs,
			"error":   err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	_ = s.store.TouchUserLogin(r.Context(), user.ID, time.Now().UTC())
	s.addLog(r, "auth", "登录成功", map[string]any{
		"status":    "success",
		"email":     user.Email,
		"user_id":   user.ID,
		"name":      user.Name,
		"role":      user.Role,
		"auth_type": "session",
		"ip":        clientIP,
		"ips":       clientIPs,
	})
	s.setSessionCookie(w, token, expiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"version":    s.cfg.AppVersion,
		"role":       user.Role,
		"subject_id": user.ID,
		"name":       user.Name,
		"user":       user,
		"token":      token,
		"expires_at": expiresAt,
	})
}

func (s *Server) handleRegisterStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.publicRegisterStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	clientIP, clientIPs := requestIPInfo(r)
	count, err := s.store.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Email = normalizeRegistrationEmail(req.Email)
	req.Name = strings.TrimSpace(req.Name)
	req.Password = strings.TrimSpace(req.Password)
	req.VerificationCode = strings.TrimSpace(req.VerificationCode)
	if req.Email == "" || req.Name == "" || req.Password == "" || req.VerificationCode == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email, name, password and verification code are required")
		return
	}
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	if err := s.ensurePublicRegistrationAllowed(r.Context(), settings); err != nil {
		writeError(w, http.StatusForbidden, "registration_disabled", err.Error())
		return
	}
	if err := validateRegisterEmail(req.Email, settings); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.regCodes.Verify(req.Email, req.VerificationCode); err != nil {
		writeError(w, http.StatusBadRequest, "verification_failed", err.Error())
		return
	}
	role := domain.RoleUser
	if count == 0 {
		role = domain.RoleAdmin
	}
	user, err := s.createUser(r.Context(), req.Email, req.Name, req.Password, role, nil, nil, nil, nil, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_user_failed", err.Error())
		return
	}
	keyItem, _, err := s.ensureUserAPIKey(r.Context(), user, "Default API Key", false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create_key_failed", err.Error())
		return
	}
	user.APIKey = &keyItem
	token, expiresAt, err := s.sessions.Sign(user.ID, user.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", err.Error())
		return
	}
	s.addLog(r, "auth", "注册成功", map[string]any{
		"status":    "success",
		"email":     user.Email,
		"user_id":   user.ID,
		"name":      user.Name,
		"role":      user.Role,
		"auth_type": "session",
		"ip":        clientIP,
		"ips":       clientIPs,
	})
	s.setSessionCookie(w, token, expiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "token": token, "expires_at": expiresAt})
}

func (s *Server) handleRegisterSendCode(w http.ResponseWriter, r *http.Request) {
	clientIP, clientIPs := requestIPInfo(r)
	var req registerSendCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	email := normalizeRegistrationEmail(req.Email)
	if email == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email is required")
		return
	}
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	if err := s.ensurePublicRegistrationAllowed(r.Context(), settings); err != nil {
		writeError(w, http.StatusForbidden, "registration_disabled", err.Error())
		return
	}
	if err := validateRegisterEmail(email, settings); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, err := s.store.GetUserByEmail(r.Context(), email); err == nil {
		writeError(w, http.StatusConflict, "email_exists", "email is already registered")
		return
	} else if !errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	if remaining := s.regCodes.CooldownRemaining(email, s.registerCodeCooldown(email, settings)); remaining > 0 {
		writeError(w, http.StatusTooManyRequests, "register_code_cooldown", fmt.Sprintf("please wait %ds before requesting another code", int(remaining.Seconds())+1))
		return
	}
	cfg := smtpMailConfigFromSettings(settings)
	if err := validateSMTPMailConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, "smtp_config_invalid", err.Error())
		return
	}
	code := generateVerificationCode()
	s.regCodes.Put(email, code)
	subject := "GPT Image Web 注册验证码"
	body := strings.Join([]string{
		"你的注册验证码如下：",
		"",
		fmt.Sprintf("验证码：%s", code),
		"",
		"该验证码 10 分钟内有效，仅用于 GPT Image Web 注册。",
	}, "\n")
	if err := sendSMTPMail(r.Context(), cfg, email, subject, body); err != nil {
		s.regCodes.Delete(email)
		s.addLog(r, "auth", "注册验证码发送失败", map[string]any{
			"status": "failed",
			"email":  email,
			"ip":     clientIP,
			"ips":    clientIPs,
			"error":  err.Error(),
		})
		writeError(w, http.StatusBadRequest, "register_send_code_failed", err.Error())
		return
	}
	s.addLog(r, "auth", "注册验证码已发送", map[string]any{
		"status": "sent",
		"email":  email,
		"ip":     clientIP,
		"ips":    clientIPs,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	user, err := s.store.GetUserByID(r.Context(), identity.ID)
	if err != nil {
		writeError(w, storageStatus(err), "user_not_found", err.Error())
		return
	}
	settings, _ := s.store.GetSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"identity":     identity,
		"user":         user,
		"model_policy": modelPolicyForIdentity(r.Context(), identity, settings),
	})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	query := storage.UserListQuery{
		Page:     queryInt(r, "page", 1),
		PageSize: queryInt(r, "page_size", 25),
		Query:    strings.TrimSpace(r.URL.Query().Get("query")),
		Status:   strings.TrimSpace(r.URL.Query().Get("status")),
		Role:     strings.TrimSpace(r.URL.Query().Get("role")),
	}
	users, total, err := s.store.ListUsersWithAPIKeysPage(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users, "total": total, "page": query.Page, "page_size": query.PageSize})
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
	user, err := s.createUser(r.Context(), req.Email, req.Name, req.Password, req.Role, req.QuotaUnlimited, req.PermanentQuota, req.TemporaryQuota, req.TemporaryQuotaDate, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_user_failed", err.Error())
		return
	}
	keyItem, raw, err := s.ensureUserAPIKey(r.Context(), user, "Default API Key", false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create_key_failed", err.Error())
		return
	}
	user.APIKey = &keyItem
	writeJSON(w, http.StatusCreated, userCreateResponse{Item: user, APIKey: publicKey(keyItem), Key: raw})
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
	update := storage.UserUpdate{
		Email:              req.Email,
		Name:               req.Name,
		Role:               req.Role,
		QuotaUnlimited:     req.QuotaUnlimited,
		PermanentQuota:     req.PermanentQuota,
		TemporaryQuota:     req.TemporaryQuota,
		TemporaryQuotaDate: req.TemporaryQuotaDate,
		AddPermanentQuota:  req.AddPermanentQuota,
	}
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
	if key, err := s.syncUserAPIKey(r.Context(), user); err == nil {
		user.APIKey = &key
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": user})
}

func (s *Server) handleBatchUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req userBatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ids := compactStrings(req.IDs)
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "ids are required")
		return
	}
	action := strings.TrimSpace(req.Action)
	if action == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "action is required")
		return
	}
	updated := 0
	results := make([]domain.User, 0, len(ids))
	switch action {
	case "enable", "disable", "delete":
		var status domain.UserStatus
		switch action {
		case "enable":
			status = domain.UserStatusActive
		case "disable":
			status = domain.UserStatusDisabled
		default:
			status = domain.UserStatusDeleted
		}
		for _, id := range ids {
			user, err := s.store.UpdateUser(r.Context(), id, storage.UserUpdate{Status: &status})
			if err != nil {
				writeError(w, storageStatus(err), "batch_user_failed", err.Error())
				return
			}
			if key, err := s.syncUserAPIKey(r.Context(), user); err == nil {
				user.APIKey = &key
			}
			results = append(results, user)
			updated++
		}
	case "grant_temporary_quota":
		if req.TemporaryQuota == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "temporary_quota is required")
			return
		}
		for _, id := range ids {
			user, err := s.store.UpdateUser(r.Context(), id, storage.UserUpdate{TemporaryQuota: req.TemporaryQuota})
			if err != nil {
				writeError(w, storageStatus(err), "batch_user_failed", err.Error())
				return
			}
			if key, err := s.syncUserAPIKey(r.Context(), user); err == nil {
				user.APIKey = &key
			}
			results = append(results, user)
			updated++
		}
	case "grant_permanent_quota":
		if req.PermanentQuota == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "permanent_quota is required")
			return
		}
		for _, id := range ids {
			user, err := s.store.UpdateUser(r.Context(), id, storage.UserUpdate{AddPermanentQuota: req.PermanentQuota})
			if err != nil {
				writeError(w, storageStatus(err), "batch_user_failed", err.Error())
				return
			}
			if key, err := s.syncUserAPIKey(r.Context(), user); err == nil {
				user.APIKey = &key
			}
			results = append(results, user)
			updated++
		}
	case "set_temporary_quota":
		if req.TemporaryQuota == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "temporary_quota is required")
			return
		}
		for _, id := range ids {
			user, err := s.store.UpdateUser(r.Context(), id, storage.UserUpdate{TemporaryQuota: req.TemporaryQuota})
			if err != nil {
				writeError(w, storageStatus(err), "batch_user_failed", err.Error())
				return
			}
			if key, err := s.syncUserAPIKey(r.Context(), user); err == nil {
				user.APIKey = &key
			}
			results = append(results, user)
			updated++
		}
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "unsupported action")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "items": results})
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
	if key, err := s.syncUserAPIKey(r.Context(), user); err == nil {
		user.APIKey = &key
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": user})
}

func (s *Server) handleResetUserAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	user, err := s.store.GetUserByID(r.Context(), r.PathValue("user_id"))
	if err != nil {
		writeError(w, storageStatus(err), "user_not_found", err.Error())
		return
	}
	item, raw, err := s.ensureUserAPIKey(r.Context(), user, "Default API Key", true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reset_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": publicKey(item), "key": raw})
}

func (s *Server) handleMyAPIKeys(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetAPIKeyByUserID(r.Context(), identity.ID)
	if err != nil {
		writeError(w, storageStatus(err), "key_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": publicKeys([]domain.APIKey{item})})
}

func (s *Server) handleCreateMyAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "key_creation_disabled", "API keys are created with users; reset the user's key instead")
}

func (s *Server) handleUpdateMyAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "key_update_disabled", "manage the user's API key from the admin user table")
}

func (s *Server) handleDeleteMyAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "key_deletion_disabled", "each user keeps one API key; disable or delete the user instead")
}

func (s *Server) createUser(ctx context.Context, email string, name string, password string, role domain.Role, quotaUnlimited *bool, permanentQuota *int, temporaryQuota *int, temporaryQuotaDate *string, dailyTemporaryQuota *int) (domain.User, error) {
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
	if role == domain.RoleAdmin {
		user.QuotaUnlimited = true
	} else {
		defaultTemporaryQuota := s.defaultNewUserTemporaryQuota(ctx)
		if quotaUnlimited != nil {
			user.QuotaUnlimited = *quotaUnlimited
		}
		if permanentQuota != nil {
			user.PermanentQuota = maxInt(0, *permanentQuota)
		}
		if temporaryQuota != nil {
			user.TemporaryQuota = maxInt(0, *temporaryQuota)
		} else if defaultTemporaryQuota > 0 {
			user.TemporaryQuota = defaultTemporaryQuota
		}
		if dailyTemporaryQuota != nil {
			user.DailyTemporaryQuota = maxInt(0, *dailyTemporaryQuota)
		} else if user.TemporaryQuota > 0 {
			user.DailyTemporaryQuota = user.TemporaryQuota
		}
		if user.TemporaryQuota > 0 {
			user.TemporaryQuotaDate = quotaDayString(time.Now())
		} else if temporaryQuotaDate != nil {
			user.TemporaryQuotaDate = strings.TrimSpace(*temporaryQuotaDate)
		}
	}
	if err := s.store.CreateUser(ctx, user); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Server) defaultNewUserTemporaryQuota(ctx context.Context) int {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return 0
	}
	return maxInt(0, intMapValue(settings, "default_new_user_temporary_quota"))
}

func normalizeRegistrationEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (s *Server) ensureUserAPIKey(ctx context.Context, user domain.User, name string, reset bool) (domain.APIKey, string, error) {
	if !reset {
		key, err := s.store.GetAPIKeyByUserID(ctx, user.ID)
		if err == nil {
			return key, "", nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return domain.APIKey{}, "", err
		}
	}
	raw := auth.NewAPIKey()
	if strings.TrimSpace(name) == "" {
		name = "Default API Key"
	}
	item := domain.APIKey{
		ID:        auth.RandomID(12),
		UserID:    user.ID,
		Name:      strings.TrimSpace(name),
		Role:      user.Role,
		KeyHash:   auth.HashAPIKey(raw),
		Enabled:   user.Status == domain.UserStatusActive,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.UpsertUserAPIKey(ctx, item); err != nil {
		return domain.APIKey{}, "", err
	}
	saved, err := s.store.GetAPIKeyByUserID(ctx, user.ID)
	if err != nil {
		return domain.APIKey{}, "", err
	}
	return saved, raw, nil
}

func (s *Server) syncUserAPIKey(ctx context.Context, user domain.User) (domain.APIKey, error) {
	key, err := s.store.GetAPIKeyByUserID(ctx, user.ID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) && user.Status != domain.UserStatusDeleted {
			key, _, err = s.ensureUserAPIKey(ctx, user, "Default API Key", false)
		}
		return key, err
	}
	enabled := user.Status == domain.UserStatusActive
	name := key.Name
	role := user.Role
	update := storage.APIKeyUpdate{Name: &name, Enabled: &enabled, Role: &role}
	item, err := s.store.UpdateAPIKey(ctx, key.ID, user.ID, update)
	if err != nil {
		return domain.APIKey{}, err
	}
	return item, nil
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
