package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gpt-image-web/internal/api"
	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/config"
	"gpt-image-web/internal/storage"
)

const testAdminAPIKey = "dev-key"

func newTestServer(t *testing.T, upstream api.Upstream) (*api.Server, func()) {
	t.Helper()
	server, _, cleanup := newTestServerWithStore(t, upstream)
	return server, cleanup
}

func newTestServerWithStore(t *testing.T, upstream api.Upstream) (*api.Server, *storage.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	webDir := filepath.Join(tempDir, "web")
	imagesDir := filepath.Join(dataDir, "images")
	backupsDir := filepath.Join(dataDir, "backups")
	for _, dir := range []string{dataDir, webDir, imagesDir, backupsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write test web index: %v", err)
	}

	dbPath := filepath.Join(dataDir, "app.db")
	store, err := storage.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := config.Config{
		Addr:                    "127.0.0.1:0",
		AppVersion:              "test",
		DataDir:                 dataDir,
		BackupsDir:              backupsDir,
		WebDir:                  webDir,
		ImagesDir:               imagesDir,
		DatabasePath:            dbPath,
		AdminEmail:              "admin@example.com",
		AdminPassword:           "password123",
		SessionSecret:           "secret",
		SessionTTLHours:         24,
		MaxRequestBodyBytes:     80 << 20,
		LoginRateLimitMax:       8,
		LoginRateLimitWindowSec: 300,
		ImageWorkerCount:        1,
		ImageQueueSize:          4,
		ImageAccountConcurrency: 1,
	}
	server, err := api.NewServer(cfg, store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("new server: %v", err)
	}
	server.SetUpstream(upstream)
	setAdminAPIKey(t, ctx, store, cfg.AdminEmail)
	cleanup := func() {
		server.Close()
		_ = store.Close()
	}
	return server, store, cleanup
}

func setAdminAPIKey(t *testing.T, ctx context.Context, store *storage.Store, email string) {
	t.Helper()
	admin, err := store.GetUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("load admin user: %v", err)
	}
	key, err := store.GetAPIKeyByUserID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("load admin api key: %v", err)
	}
	hash := auth.HashAPIKey(testAdminAPIKey)
	if _, err := store.UpdateAPIKey(ctx, key.ID, admin.ID, storage.APIKeyUpdate{KeyHash: &hash}); err != nil {
		t.Fatalf("set admin api key: %v", err)
	}
}
