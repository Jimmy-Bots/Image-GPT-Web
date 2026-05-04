package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr                    string
	AppVersion              string
	DataDir                 string
	DatabasePath            string
	DBMaxOpenConns          int
	ProxyURL                string
	LegacyAdminKey          string
	SessionSecret           string
	SessionTTLHours         int
	AdminEmail              string
	AdminPassword           string
	AllowPublicRegistration bool
	ImageWorkerCount        int
	ImageQueueSize          int
	ImageAccountConcurrency int
}

func Load() (Config, error) {
	dataDir := envString("CHATGPT2API_DATA_DIR", "./data")
	dbPath := envString("CHATGPT2API_DB_PATH", filepath.Join(dataDir, "app.db"))
	cfg := Config{
		Addr:                    envString("CHATGPT2API_ADDR", ":3000"),
		AppVersion:              envString("CHATGPT2API_VERSION", "0.1.0-go"),
		DataDir:                 dataDir,
		DatabasePath:            dbPath,
		DBMaxOpenConns:          envInt("CHATGPT2API_DB_MAX_OPEN_CONNS", 16, 1),
		ProxyURL:                envString("CHATGPT2API_PROXY_URL", ""),
		LegacyAdminKey:          strings.TrimSpace(os.Getenv("CHATGPT2API_AUTH_KEY")),
		SessionSecret:           strings.TrimSpace(os.Getenv("CHATGPT2API_SESSION_SECRET")),
		SessionTTLHours:         envInt("CHATGPT2API_SESSION_TTL_HOURS", 24*14, 1),
		AdminEmail:              envString("CHATGPT2API_ADMIN_EMAIL", "admin@example.com"),
		AdminPassword:           strings.TrimSpace(os.Getenv("CHATGPT2API_ADMIN_PASSWORD")),
		AllowPublicRegistration: envBool("CHATGPT2API_ALLOW_REGISTRATION", false),
		ImageWorkerCount:        envInt("CHATGPT2API_IMAGE_WORKERS", 4, 1),
		ImageQueueSize:          envInt("CHATGPT2API_IMAGE_QUEUE_SIZE", 128, 1),
		ImageAccountConcurrency: envInt("CHATGPT2API_IMAGE_ACCOUNT_CONCURRENCY", 3, 1),
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o755); err != nil {
		return Config{}, fmt.Errorf("create db dir: %w", err)
	}
	if cfg.SessionSecret == "" {
		if cfg.LegacyAdminKey != "" {
			cfg.SessionSecret = cfg.LegacyAdminKey
		} else {
			cfg.SessionSecret = randomSecret()
		}
	}
	return cfg, nil
}

func envString(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int, minimum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < minimum {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func randomSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "dev-session-secret"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
