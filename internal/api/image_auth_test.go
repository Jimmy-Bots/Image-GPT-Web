package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gpt-image-web/internal/config"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

func TestHandleImageAssetEnforcesOwnerAccess(t *testing.T) {
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

	now := time.Now().UTC()
	owner := domain.User{
		ID:             "user-owner",
		Email:          "owner@example.com",
		Name:           "Owner",
		PasswordHash:   "hash",
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	other := domain.User{
		ID:             "user-other",
		Email:          "other@example.com",
		Name:           "Other",
		PasswordHash:   "hash",
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	admin := domain.User{
		ID:             "user-admin",
		Email:          "admin@example.com",
		Name:           "Admin",
		PasswordHash:   "hash",
		Role:           domain.RoleAdmin,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	for _, user := range []domain.User{owner, other, admin} {
		if err := store.CreateUser(ctx, user); err != nil {
			t.Fatalf("create user %s: %v", user.ID, err)
		}
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

	rel := filepath.ToSlash(filepath.Join("2026", "05", "07", "owned.png"))
	path := filepath.Join(imagesDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir asset dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	writeImageMeta(path, storedImageMeta{
		Prompt:        "test",
		RevisedPrompt: "",
		ArchivedAt:    now,
		OwnerID:       owner.ID,
	})

	ownerToken, _, err := server.sessions.Sign(owner.ID, owner.Role)
	if err != nil {
		t.Fatalf("owner token: %v", err)
	}
	otherToken, _, err := server.sessions.Sign(other.ID, other.Role)
	if err != nil {
		t.Fatalf("other token: %v", err)
	}
	adminToken, _, err := server.sessions.Sign(admin.ID, admin.Role)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}

		cases := []struct {
			name   string
			token  string
			status int
		}{
			{name: "owner allowed", token: ownerToken, status: http.StatusOK},
			{name: "other forbidden", token: otherToken, status: http.StatusForbidden},
			{name: "admin allowed", token: adminToken, status: http.StatusOK},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/images/"+rel, nil)
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.token})
				rec := httptest.NewRecorder()
				server.handleImageAsset(rec, req)
				if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestSetSessionCookieAndCookieAuth(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	webDir := filepath.Join(tempDir, "web")
	imagesDir := filepath.Join(tempDir, "images")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.MkdirAll(webDir, 0o755)
	_ = os.MkdirAll(imagesDir, 0o755)
	_ = os.WriteFile(filepath.Join(webDir, "index.html"), []byte("ok"), 0o644)

	dbPath := filepath.Join(dataDir, "app.db")
	store, err := storage.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	user := domain.User{
		ID:             "cookie-user",
		Email:          "cookie@example.com",
		Name:           "Cookie",
		PasswordHash:   "hash",
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
		ImagesDir:       imagesDir,
		SessionSecret:   "test-secret",
		SessionTTLHours: 24,
	}
	server, err := NewServer(cfg, store)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	token, expiresAt, err := server.sessions.Sign(user.ID, user.Role)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	rec := httptest.NewRecorder()
	server.setSessionCookie(rec, token, expiresAt)
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie to be set")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(cookies[0])
	identity, ok := server.sessionIdentityFromRequest(req)
	if !ok {
		t.Fatal("expected cookie-backed session identity")
	}
	if identity.ID != user.ID {
		t.Fatalf("identity=%s want=%s", identity.ID, user.ID)
	}
	if _, ok := server.identityFromRequest(req); ok {
		t.Fatal("expected cookie-backed session to not satisfy generic identity auth without bearer token")
	}
}
