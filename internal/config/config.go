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
	Addr                            string
	AppVersion                      string
	RootDir                         string
	DataDir                         string
	BackupsDir                      string
	WebDir                          string
	ImagesDir                       string
	DatabasePath                    string
	DBMaxOpenConns                  int
	ProxyURL                        string
	BaseURL                         string
	LogLevel                        string
	CORSAllowedOrigins              []string
	MaxRequestBodyBytes             int64
	LoginRateLimitMax               int
	LoginRateLimitWindowSec         int
	SessionSecret                   string
	SessionTTLHours                 int
	AdminEmail                      string
	AdminPassword                   string
	AllowPublicRegistration         bool
	ImageWorkerCount                int
	ImageQueueSize                  int
	ImageAccountConcurrency         int
	RegisterInbucketAPIBase         string
	RegisterInbucketDomains         []string
	RegisterInbucketRandomSubdomain bool
	RegisterProxyURL                string
	RegisterMode                    string
	RegisterTotal                   int
	RegisterThreads                 int
	RegisterTargetQuota             int
	RegisterTargetAvailable         int
	RegisterCheckIntervalSeconds    int
}

func Load() (Config, error) {
	rootDir, err := os.Getwd()
	if err != nil || strings.TrimSpace(rootDir) == "" {
		rootDir = "."
	}
	dataDir := envString("CHATGPT2API_DATA_DIR", "./data")
	dbPath := envString("CHATGPT2API_DB_PATH", filepath.Join(dataDir, "app.db"))
	appVersion := strings.TrimSpace(os.Getenv("CHATGPT2API_VERSION"))
	if appVersion == "" {
		appVersion = readVersionFile("VERSION", "0.1.0-go")
	}
	cfg := Config{
		Addr:                            envString("CHATGPT2API_ADDR", ":3000"),
		AppVersion:                      appVersion,
		RootDir:                         rootDir,
		DataDir:                         dataDir,
		BackupsDir:                      envString("CHATGPT2API_BACKUPS_DIR", filepath.Join(dataDir, "backups")),
		WebDir:                          envString("CHATGPT2API_WEB_DIR", "./web/dist"),
		ImagesDir:                       envString("CHATGPT2API_IMAGES_DIR", filepath.Join(dataDir, "images")),
		DatabasePath:                    dbPath,
		DBMaxOpenConns:                  envInt("CHATGPT2API_DB_MAX_OPEN_CONNS", 16, 1),
		ProxyURL:                        envString("CHATGPT2API_PROXY_URL", ""),
		BaseURL:                         strings.TrimRight(envString("CHATGPT2API_BASE_URL", ""), "/"),
		LogLevel:                        normalizeLogLevel(envString("CHATGPT2API_LOG_LEVEL", "info")),
		CORSAllowedOrigins:              envList("CHATGPT2API_CORS_ALLOWED_ORIGINS"),
		MaxRequestBodyBytes:             int64(envInt("CHATGPT2API_MAX_REQUEST_BODY_MB", 80, 1)) << 20,
		LoginRateLimitMax:               envInt("CHATGPT2API_LOGIN_RATE_LIMIT_MAX", 8, 1),
		LoginRateLimitWindowSec:         envInt("CHATGPT2API_LOGIN_RATE_LIMIT_WINDOW_SECONDS", 300, 1),
			SessionSecret:                   strings.TrimSpace(os.Getenv("CHATGPT2API_SESSION_SECRET")),
		SessionTTLHours:                 envInt("CHATGPT2API_SESSION_TTL_HOURS", 24*14, 1),
		AdminEmail:                      envString("CHATGPT2API_ADMIN_EMAIL", "admin@example.com"),
		AdminPassword:                   strings.TrimSpace(os.Getenv("CHATGPT2API_ADMIN_PASSWORD")),
		AllowPublicRegistration:         envBool("CHATGPT2API_ALLOW_REGISTRATION", false),
		ImageWorkerCount:                envInt("CHATGPT2API_IMAGE_WORKERS", 4, 1),
		ImageQueueSize:                  envInt("CHATGPT2API_IMAGE_QUEUE_SIZE", 128, 1),
		ImageAccountConcurrency:         envInt("CHATGPT2API_IMAGE_ACCOUNT_CONCURRENCY", 1, 1),
		RegisterInbucketAPIBase:         strings.TrimSpace(os.Getenv("CHATGPT2API_REGISTER_INBUCKET_API_BASE")),
		RegisterInbucketDomains:         envList("CHATGPT2API_REGISTER_INBUCKET_DOMAINS"),
		RegisterInbucketRandomSubdomain: envBool("CHATGPT2API_REGISTER_INBUCKET_RANDOM_SUBDOMAIN", true),
		RegisterProxyURL:                strings.TrimSpace(os.Getenv("CHATGPT2API_REGISTER_PROXY_URL")),
		RegisterMode:                    envString("CHATGPT2API_REGISTER_MODE", "total"),
		RegisterTotal:                   envInt("CHATGPT2API_REGISTER_TOTAL", 10, 1),
		RegisterThreads:                 envInt("CHATGPT2API_REGISTER_THREADS", 3, 1),
		RegisterTargetQuota:             envInt("CHATGPT2API_REGISTER_TARGET_QUOTA", 100, 1),
		RegisterTargetAvailable:         envInt("CHATGPT2API_REGISTER_TARGET_AVAILABLE", 10, 1),
		RegisterCheckIntervalSeconds:    envInt("CHATGPT2API_REGISTER_CHECK_INTERVAL_SECONDS", 5, 1),
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o755); err != nil {
		return Config{}, fmt.Errorf("create db dir: %w", err)
	}
	if err := os.MkdirAll(cfg.ImagesDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create images dir: %w", err)
	}
	if err := os.MkdirAll(cfg.BackupsDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create backups dir: %w", err)
	}
	if cfg.SessionSecret == "" {
		cfg.SessionSecret = randomSecret()
	}
	return cfg, nil
}

func (c Config) DebugLogging() bool {
	return c.LogLevel == "debug"
}

func normalizeLogLevel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "debug":
		return "debug"
	default:
		return "info"
	}
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

func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func randomSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "dev-session-secret"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func readVersionFile(path string, fallback string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return fallback
	}
	return value
}
