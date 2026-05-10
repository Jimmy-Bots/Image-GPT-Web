package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/config"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

type syncCancelTestUpstream struct {
	started chan struct{}
	done    chan struct{}
}

func (u *syncCancelTestUpstream) ListModels(ctx context.Context) (map[string]any, error) {
	return map[string]any{"object": "list", "data": []map[string]any{}}, nil
}

func (u *syncCancelTestUpstream) GenerateImage(ctx context.Context, req ImageGenerationPayload) (map[string]any, error) {
	select {
	case u.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case u.done <- struct{}{}:
	default:
	}
	return nil, ctx.Err()
}

func (u *syncCancelTestUpstream) EditImage(ctx context.Context, req ImageEditPayload) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (u *syncCancelTestUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (u *syncCancelTestUpstream) StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (u *syncCancelTestUpstream) Responses(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (u *syncCancelTestUpstream) StreamResponses(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (u *syncCancelTestUpstream) AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (u *syncCancelTestUpstream) StreamAnthropicMessages(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (u *syncCancelTestUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	return 0, nil
}

func TestHandleLoginPostIgnoresStaleSessionCookie(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	webDir := filepath.Join(tempDir, "web")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	dbPath := filepath.Join(dataDir, "app.db")
	store, err := storage.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().UTC()
	currentUser := domain.User{
		ID:             "user-current",
		Email:          "current@example.com",
		Name:           "Current",
		PasswordHash:   hash,
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	staleUser := domain.User{
		ID:             "user-stale",
		Email:          "stale@example.com",
		Name:           "Stale",
		PasswordHash:   hash,
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	for _, user := range []domain.User{currentUser, staleUser} {
		if err := store.CreateUser(ctx, user); err != nil {
			t.Fatalf("create user %s: %v", user.ID, err)
		}
	}

	cfg := config.Config{
		DataDir:         dataDir,
		DatabasePath:    dbPath,
		WebDir:          webDir,
		ImagesDir:       filepath.Join(tempDir, "images"),
		SessionSecret:   "test-secret",
		SessionTTLHours: 24,
	}
	server, err := NewServer(cfg, store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	staleToken, _, err := server.sessions.Sign(staleUser.ID, staleUser.Role)
	if err != nil {
		t.Fatalf("sign stale token: %v", err)
	}

	body, err := json.Marshal(map[string]string{
		"email":    currentUser.Email,
		"password": "correct-password",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: staleToken})
	rec := httptest.NewRecorder()

	server.handleLogin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string      `json:"token"`
		User  domain.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.User.ID != currentUser.ID {
		t.Fatalf("logged in user=%q want=%q", resp.User.ID, currentUser.ID)
	}
	if resp.Token == "" {
		t.Fatalf("expected token in login response")
	}
}

func TestHandleChangeMyPassword(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	webDir := filepath.Join(tempDir, "web")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	dbPath := filepath.Join(dataDir, "app.db")
	store, err := storage.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	hash, err := auth.HashPassword("old-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().UTC()
	user := domain.User{
		ID:             "user-1",
		Email:          "user@example.com",
		Name:           "User",
		PasswordHash:   hash,
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	cfg := config.Config{
		DataDir:         dataDir,
		DatabasePath:    dbPath,
		WebDir:          webDir,
		ImagesDir:       filepath.Join(tempDir, "images"),
		SessionSecret:   "test-secret",
		SessionTTLHours: 24,
	}
	server, err := NewServer(cfg, store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	_, rawKey, err := server.ensureUserAPIKey(ctx, user, "Default API Key", true)
	if err != nil {
		t.Fatalf("ensure api key: %v", err)
	}
	body, err := json.Marshal(map[string]string{
		"current_password": "old-password",
		"new_password":     "new-password-123",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/me/password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	server.handleChangeMyPassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := store.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !auth.VerifyPassword(updated.PasswordHash, "new-password-123") {
		t.Fatalf("password was not updated")
	}
}

func TestHandleResetMyAPIKeyReturnsRawKey(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	webDir := filepath.Join(tempDir, "web")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	dbPath := filepath.Join(dataDir, "app.db")
	store, err := storage.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	hash, err := auth.HashPassword("password-123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().UTC()
	user := domain.User{
		ID:             "user-2",
		Email:          "user2@example.com",
		Name:           "User2",
		PasswordHash:   hash,
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	cfg := config.Config{
		DataDir:         dataDir,
		DatabasePath:    dbPath,
		WebDir:          webDir,
		ImagesDir:       filepath.Join(tempDir, "images"),
		SessionSecret:   "test-secret",
		SessionTTLHours: 24,
	}
	server, err := NewServer(cfg, store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	_, rawKey, err := server.ensureUserAPIKey(ctx, user, "Default API Key", true)
	if err != nil {
		t.Fatalf("ensure api key: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/me/api-key/reset", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	server.handleResetMyAPIKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(resp.Key) == "" {
		t.Fatalf("expected raw key")
	}
}

func TestHandleImageGenerationsMarksCancelledOnRequestAbort(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	webDir := filepath.Join(tempDir, "web")
	imagesDir := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	dbPath := filepath.Join(dataDir, "app.db")
	store, err := storage.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	hash, err := auth.HashPassword("password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().UTC()
	user := domain.User{
		ID:             "sync-cancel-user",
		Email:          "sync-cancel@example.com",
		Name:           "Sync Cancel",
		PasswordHash:   hash,
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: false,
		PermanentQuota: 5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	cfg := config.Config{
		DataDir:         dataDir,
		DatabasePath:    dbPath,
		WebDir:          webDir,
		ImagesDir:       imagesDir,
		SessionSecret:   "test-secret",
		SessionTTLHours: 24,
	}
	server, err := NewServer(cfg, store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	_, rawKey, err := server.ensureUserAPIKey(ctx, user, "Default API Key", true)
	if err != nil {
		t.Fatalf("ensure api key: %v", err)
	}

	upstream := &syncCancelTestUpstream{
		started: make(chan struct{}, 1),
		done:    make(chan struct{}, 1),
	}
	server.SetUpstream(upstream)

	body, err := json.Marshal(map[string]any{
		"prompt":          "cancel me",
		"model":           "gpt-image-2",
		"n":               1,
		"response_format": "url",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	reqCtx, cancel := context.WithCancel(ctx)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body)).WithContext(reqCtx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.handleImageGenerations(rec, req)
	}()

	select {
	case <-upstream.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream start")
	}
	cancel()
	select {
	case <-upstream.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream cancellation")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for handler to return")
	}

	items, err := store.ListImageTasks(ctx, user.ID, nil, true, true)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected sync task record")
	}
	task := items[0]
	if task.Status != taskCancelled {
		t.Fatalf("expected task status cancelled, got %s", task.Status)
	}
	events, err := store.ListTaskEvents(ctx, user.ID, task.ID, true)
	if err != nil {
		t.Fatalf("list task events: %v", err)
	}
	foundCancelled := false
	for _, event := range events {
		if event.Type == "cancelled" {
			foundCancelled = true
			break
		}
	}
	if !foundCancelled {
		t.Fatalf("expected cancelled event")
	}
	updated, err := store.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if updated.PermanentQuota != user.PermanentQuota {
		t.Fatalf("expected quota refunded to %d, got %d", user.PermanentQuota, updated.PermanentQuota)
	}
	if rec.Code != 200 && rec.Code != 0 {
		t.Fatalf("unexpected response status after cancel: %d", rec.Code)
	}
	if rec.Code != 0 && rec.Body.Len() > 0 {
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err == nil {
			if msg, _ := payload["error"].(map[string]any)["message"].(string); msg != "" && !errors.Is(reqCtx.Err(), context.Canceled) {
				t.Fatalf("unexpected response body after cancel: %s", rec.Body.String())
			}
		}
	}
}
