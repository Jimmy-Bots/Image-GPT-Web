package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"gpt-image-web/internal/config"
	"gpt-image-web/internal/storage"
)

const (
	defaultBackupHour      = 24
	defaultBackupMinute    = 0
	defaultBackupKeepLatest = 7
	backupTypeVersion      = "backup-v1"
)

type backupManager struct {
	cfg       config.Config
	store     *storage.Store
	logWriter structuredLogWriter

	stop     chan struct{}
	done     chan struct{}
	wake     chan struct{}
	stopOnce sync.Once

	runMu  sync.Mutex
	stateMu sync.RWMutex
	running bool
	nextRun time.Time
	lastRun backupState
}

type backupConfig struct {
	Enabled          bool
	ScheduleHour     int
	ScheduleMinute   int
	KeepLatest       int
	Encrypt          bool
	Passphrase       string
	R2AccountID      string
	R2AccessKeyID    string
	R2SecretAccessKey string
	R2Bucket         string
	R2Prefix         string
	IncludeEnv       bool
	IncludeCompose   bool
	IncludeVersion   bool
}

type backupArtifact struct {
	Key       string `json:"key"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type backupRemoteItem struct {
	Key          string `json:"key"`
	SizeBytes    int64  `json:"size_bytes"`
	LastModified string `json:"last_modified,omitempty"`
}

type backupState struct {
	Running        bool           `json:"running"`
	Enabled        bool           `json:"enabled"`
	ScheduleHour   int            `json:"schedule_hour"`
	ScheduleMinute int            `json:"schedule_minute"`
	KeepLatest     int            `json:"keep_latest"`
	NextRunAt      string         `json:"next_run_at,omitempty"`
	LastStartedAt  string         `json:"last_started_at,omitempty"`
	LastFinishedAt string         `json:"last_finished_at,omitempty"`
	LastDurationMS int64          `json:"last_duration_ms,omitempty"`
	LastStatus     string         `json:"last_status,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
	LastTrigger    string         `json:"last_trigger,omitempty"`
	LastArtifact   *backupArtifact `json:"last_artifact,omitempty"`
}

type backupRunResult struct {
	state    backupState
	artifact *backupArtifact
}

func newBackupManager(cfg config.Config, store *storage.Store) *backupManager {
	return &backupManager{
		cfg:   cfg,
		store: store,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		wake:  make(chan struct{}, 1),
	}
}

func (m *backupManager) SetLogWriter(writer structuredLogWriter) {
	m.logWriter = writer
}

func (m *backupManager) Start() {
	if m == nil || m.store == nil {
		return
	}
	go m.loop()
}

func (m *backupManager) RefreshSchedule() {
	if m == nil {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *backupManager) Close() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		close(m.stop)
	})
	<-m.done
}

func (m *backupManager) loop() {
	defer close(m.done)
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		next := m.computeNextRun()
		m.setNextRun(next)
		wait := time.Until(next)
		if wait < time.Second {
			wait = time.Second
		}
		timer.Reset(wait)
		select {
		case <-m.stop:
			return
		case <-m.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
			cfg, ok := m.currentConfig(context.Background())
			if !ok || !cfg.Enabled {
				continue
			}
			m.Run(context.Background(), "auto")
		}
	}
}

func (m *backupManager) currentConfig(ctx context.Context) (backupConfig, bool) {
	settings, err := m.store.GetSettings(ctx)
	if err != nil {
		return backupConfig{}, false
	}
	return backupConfigFromSettings(settings), true
}

func backupConfigFromSettings(settings map[string]any) backupConfig {
	raw, _ := settings["backup"].(map[string]any)
	cfg := backupConfig{
		Enabled:           boolMapValue(raw, "enabled"),
		ScheduleHour:      intMapValue(raw, "schedule_hour"),
		ScheduleMinute:    intMapValue(raw, "schedule_minute"),
		KeepLatest:        intMapValue(raw, "keep_latest"),
		Encrypt:           !rawHasFalse(raw, "encrypt"),
		Passphrase:        stringMapValue(raw, "passphrase"),
		R2AccountID:       stringMapValue(raw, "r2_account_id"),
		R2AccessKeyID:     stringMapValue(raw, "r2_access_key_id"),
		R2SecretAccessKey: stringMapValue(raw, "r2_secret_access_key"),
		R2Bucket:          stringMapValue(raw, "r2_bucket"),
		R2Prefix:          stringMapValue(raw, "r2_prefix"),
		IncludeEnv:        !rawHasFalse(raw, "include_env"),
		IncludeCompose:    !rawHasFalse(raw, "include_compose"),
		IncludeVersion:    !rawHasFalse(raw, "include_version"),
	}
	if cfg.ScheduleHour < 0 || cfg.ScheduleHour > 24*30 {
		cfg.ScheduleHour = defaultBackupHour
	}
	if cfg.ScheduleMinute < 0 || cfg.ScheduleMinute > 59 {
		cfg.ScheduleMinute = defaultBackupMinute
	}
	if cfg.KeepLatest < 1 {
		cfg.KeepLatest = defaultBackupKeepLatest
	}
	if cfg.R2Prefix == "" {
		cfg.R2Prefix = "gpt-image-web"
	}
	return cfg
}

func rawHasFalse(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, ok := values[key]
	if !ok {
		return false
	}
	return !boolFromAny(value)
}

func (m *backupManager) computeNextRun() time.Time {
	cfg, ok := m.currentConfig(context.Background())
	now := time.Now()
	if !ok || !cfg.Enabled {
		return now.Add(time.Hour)
	}
	interval := time.Duration(cfg.ScheduleHour)*time.Hour + time.Duration(cfg.ScheduleMinute)*time.Minute
	if interval < time.Minute {
		interval = time.Minute
	}
	return now.Add(interval)
}

func (m *backupManager) setNextRun(next time.Time) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.nextRun = next
}

func (m *backupManager) Status(ctx context.Context) backupState {
	cfg, ok := m.currentConfig(ctx)
	m.stateMu.RLock()
	state := m.lastRun
	state.Running = m.running
	if !m.nextRun.IsZero() {
		state.NextRunAt = m.nextRun.UTC().Format(time.RFC3339)
	} else {
		state.NextRunAt = ""
	}
	m.stateMu.RUnlock()
	if ok {
		state.Enabled = cfg.Enabled
		state.ScheduleHour = cfg.ScheduleHour
		state.ScheduleMinute = cfg.ScheduleMinute
		state.KeepLatest = cfg.KeepLatest
	}
	return state
}

func (m *backupManager) Run(ctx context.Context, trigger string) (*backupArtifact, error) {
	m.runMu.Lock()
	if m.running {
		m.runMu.Unlock()
		return nil, fmt.Errorf("backup is already running")
	}
	m.running = true
	m.runMu.Unlock()
	defer func() {
		m.runMu.Lock()
		m.running = false
		m.runMu.Unlock()
	}()

	result, err := m.runNow(ctx, trigger)
	m.stateMu.Lock()
	m.lastRun = result.state
	m.stateMu.Unlock()
	return result.artifact, err
}

func (m *backupManager) List(ctx context.Context) ([]backupRemoteItem, error) {
	cfg, ok := m.currentConfig(ctx)
	if !ok {
		return nil, fmt.Errorf("load backup settings failed")
	}
	if err := validateBackupConfig(cfg); err != nil {
		return nil, err
	}
	items, err := listBackupItems(ctx, cfg)
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Key > items[j].Key
	})
	return items, nil
}

func (m *backupManager) Delete(ctx context.Context, key string) error {
	cfg, ok := m.currentConfig(ctx)
	if !ok {
		return fmt.Errorf("load backup settings failed")
	}
	if err := validateBackupConfig(cfg); err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("missing backup key")
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.R2Prefix), "/")
	if prefix != "" {
		expected := prefix + "/"
		if !strings.HasPrefix(key, expected) {
			return fmt.Errorf("backup key is outside configured prefix")
		}
	}
	if err := deleteBackupKey(ctx, cfg, key); err != nil {
		m.emitLog(ctx, "删除备份失败", map[string]any{
			"status": "failed",
			"key":    key,
			"error":  err.Error(),
		})
		return err
	}
	m.emitLog(ctx, "备份已删除", map[string]any{
		"status": "success",
		"key":    key,
	})
	return nil
}

func (m *backupManager) Download(ctx context.Context, key string) ([]byte, string, error) {
	cfg, ok := m.currentConfig(ctx)
	if !ok {
		return nil, "", fmt.Errorf("load backup settings failed")
	}
	if err := validateBackupConfig(cfg); err != nil {
		return nil, "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, "", fmt.Errorf("missing backup key")
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.R2Prefix), "/")
	if prefix != "" {
		expected := prefix + "/"
		if !strings.HasPrefix(key, expected) {
			return nil, "", fmt.Errorf("backup key is outside configured prefix")
		}
	}
	payload, err := downloadBackupKey(ctx, cfg, key)
	if err != nil {
		return nil, "", err
	}
	if cfg.Encrypt {
		payload, err = decryptBackupPayload(payload, cfg.Passphrase)
		if err != nil {
			return nil, "", err
		}
	}
	filename := backupDownloadName(key)
	m.emitLog(ctx, "备份已下载", map[string]any{
		"status":   "success",
		"key":      key,
		"filename": filename,
		"size":     len(payload),
	})
	return payload, filename, nil
}

func (m *backupManager) runNow(ctx context.Context, trigger string) (backupRunResult, error) {
	cfg, ok := m.currentConfig(ctx)
	if !ok {
		return backupRunResult{state: backupState{LastStatus: "failed", LastError: "load settings failed", LastTrigger: trigger}}, fmt.Errorf("load backup settings failed")
	}
	if !cfg.Enabled && trigger == "auto" {
		return backupRunResult{state: backupState{Enabled: false, LastStatus: "skipped", LastTrigger: trigger}}, nil
	}
	if err := validateBackupConfig(cfg); err != nil {
		state := backupState{
			Enabled:        cfg.Enabled,
			ScheduleHour:   cfg.ScheduleHour,
			ScheduleMinute: cfg.ScheduleMinute,
			KeepLatest:     cfg.KeepLatest,
			LastStatus:     "failed",
			LastError:      err.Error(),
			LastTrigger:    trigger,
		}
		m.emitLog(ctx, "备份失败", map[string]any{
			"status":  "failed",
			"trigger": trigger,
			"error":   err.Error(),
		})
		return backupRunResult{state: state}, err
	}

	startedAt := time.Now().UTC()
	state := backupState{
		Enabled:        cfg.Enabled,
		ScheduleHour:   cfg.ScheduleHour,
		ScheduleMinute: cfg.ScheduleMinute,
		KeepLatest:     cfg.KeepLatest,
		LastStartedAt:  startedAt.Format(time.RFC3339),
		LastTrigger:    trigger,
	}
	artifact, err := m.buildAndUpload(ctx, cfg, startedAt)
	state.LastFinishedAt = time.Now().UTC().Format(time.RFC3339)
	state.LastDurationMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		state.LastStatus = "failed"
		state.LastError = err.Error()
		m.emitLog(ctx, "备份失败", map[string]any{
			"status":       "failed",
			"trigger":      trigger,
			"error":        err.Error(),
			"started_at":   state.LastStartedAt,
			"finished_at":  state.LastFinishedAt,
			"duration_ms":  state.LastDurationMS,
		})
		return backupRunResult{state: state}, err
	}
	state.LastStatus = "success"
	state.LastArtifact = artifact
	m.emitLog(ctx, "备份完成", map[string]any{
		"status":       "success",
		"trigger":      trigger,
		"started_at":   state.LastStartedAt,
		"finished_at":  state.LastFinishedAt,
		"duration_ms":  state.LastDurationMS,
		"artifact":     artifact,
	})
	return backupRunResult{state: state, artifact: artifact}, nil
}

func validateBackupConfig(cfg backupConfig) error {
	if strings.TrimSpace(cfg.R2AccountID) == "" {
		return fmt.Errorf("missing r2_account_id")
	}
	if strings.TrimSpace(cfg.R2AccessKeyID) == "" {
		return fmt.Errorf("missing r2_access_key_id")
	}
	if strings.TrimSpace(cfg.R2SecretAccessKey) == "" {
		return fmt.Errorf("missing r2_secret_access_key")
	}
	if strings.TrimSpace(cfg.R2Bucket) == "" {
		return fmt.Errorf("missing r2_bucket")
	}
	if cfg.Encrypt && strings.TrimSpace(cfg.Passphrase) == "" {
		return fmt.Errorf("missing backup passphrase")
	}
	return nil
}

func (m *backupManager) buildAndUpload(ctx context.Context, cfg backupConfig, startedAt time.Time) (*backupArtifact, error) {
	stamp := startedAt.Format("20060102-150405")
	workDir := filepath.Join(m.cfg.BackupsDir, "tmp-"+stamp)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("create backup temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	dbSnapshot := filepath.Join(workDir, "app-backup.db")
	if err := m.store.BackupDatabase(ctx, dbSnapshot); err != nil {
		return nil, fmt.Errorf("backup database: %w", err)
	}

	files, err := m.collectBackupFiles(cfg, dbSnapshot)
	if err != nil {
		return nil, err
	}

	payload, shaSum, err := buildEncryptedArchive(files, cfg)
	if err != nil {
		return nil, err
	}

	objectKey := buildBackupObjectKey(cfg.R2Prefix, startedAt)
	if err := uploadToR2(ctx, cfg, objectKey, payload); err != nil {
		return nil, err
	}
	if err := rotateBackups(ctx, cfg, cfg.KeepLatest); err != nil {
		return nil, err
	}
	return &backupArtifact{
		Key:       objectKey,
		SizeBytes: int64(len(payload)),
		SHA256:    shaSum,
	}, nil
}

func buildBackupObjectKey(prefix string, startedAt time.Time) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	filename := fmt.Sprintf("backup-%s.tar.gz.enc", startedAt.UTC().Format("20060102-150405"))
	if prefix == "" {
		return filename
	}
	return prefix + "/" + filename
}

func (m *backupManager) collectBackupFiles(cfg backupConfig, dbSnapshot string) ([]backupFile, error) {
	files := []backupFile{
		{
			Name: "data/app.db",
			Path: dbSnapshot,
		},
	}
	settingsSnapshot, err := m.store.GetSettings(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load settings snapshot: %w", err)
	}
	settingsRaw, err := json.MarshalIndent(settingsSnapshot, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings snapshot: %w", err)
	}
	files = append(files, backupFile{
		Name: "config/settings.json",
		Data: settingsRaw,
	})
	addIfExists := func(enabled bool, archiveName string, path string) error {
		if !enabled {
			return nil
		}
		if strings.TrimSpace(path) == "" {
			return nil
		}
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		files = append(files, backupFile{Name: archiveName, Path: path})
		return nil
	}
	if err := addIfExists(cfg.IncludeEnv, ".env", filepath.Join(m.cfg.RootDir, ".env")); err != nil {
		return nil, fmt.Errorf("check .env: %w", err)
	}
	if err := addIfExists(cfg.IncludeCompose, "docker-compose.yml", filepath.Join(m.cfg.RootDir, "docker-compose.yml")); err != nil {
		return nil, fmt.Errorf("check docker-compose: %w", err)
	}
	if err := addIfExists(cfg.IncludeVersion, "VERSION", filepath.Join(m.cfg.RootDir, "VERSION")); err != nil {
		return nil, fmt.Errorf("check VERSION: %w", err)
	}
	manifest := map[string]any{
		"version":      backupTypeVersion,
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"app_version":  m.cfg.AppVersion,
		"files":        fileManifestNames(files),
		"database_path": m.cfg.DatabasePath,
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	files = append(files, backupFile{Name: "manifest.json", Data: raw})
	return files, nil
}

type backupFile struct {
	Name string
	Path string
	Data []byte
}

func fileManifestNames(files []backupFile) []string {
	names := make([]string, 0, len(files))
	for _, item := range files {
		names = append(names, item.Name)
	}
	return names
}

func buildEncryptedArchive(files []backupFile, cfg backupConfig) ([]byte, string, error) {
	var plain bytes.Buffer
	gzw := gzip.NewWriter(&plain)
	tw := tar.NewWriter(gzw)
	for _, file := range files {
		if err := appendTarFile(tw, file); err != nil {
			tw.Close()
			gzw.Close()
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		gzw.Close()
		return nil, "", err
	}
	if err := gzw.Close(); err != nil {
		return nil, "", err
	}
	body := plain.Bytes()
	if cfg.Encrypt {
		encrypted, err := encryptBackupPayload(body, cfg.Passphrase)
		if err != nil {
			return nil, "", err
		}
		body = encrypted
	}
	sum := sha256.Sum256(body)
	return body, fmt.Sprintf("%x", sum[:]), nil
}

func appendTarFile(tw *tar.Writer, file backupFile) error {
	var data []byte
	if file.Path != "" {
		raw, err := os.ReadFile(file.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", file.Path, err)
		}
		data = raw
	} else {
		data = file.Data
	}
	header := &tar.Header{
		Name:    file.Name,
		Mode:    0o600,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func encryptBackupPayload(plain []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	key := pbkdf2.Key([]byte(passphrase), salt, 120000, 32, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plain, nil)
	envelope := map[string]any{
		"version":    backupTypeVersion,
		"algorithm":  "aes-256-gcm",
		"kdf":        "pbkdf2-sha256",
		"iterations": 120000,
		"salt":       base64.StdEncoding.EncodeToString(salt),
		"nonce":      base64.StdEncoding.EncodeToString(nonce),
		"ciphertext": base64.StdEncoding.EncodeToString(ciphertext),
	}
	return json.Marshal(envelope)
}

func decryptBackupPayload(raw []byte, passphrase string) ([]byte, error) {
	var envelope struct {
		Algorithm  string `json:"algorithm"`
		Iterations int    `json:"iterations"`
		Salt       string `json:"salt"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode encrypted backup payload: %w", err)
	}
	if envelope.Ciphertext == "" || envelope.Salt == "" || envelope.Nonce == "" {
		return nil, fmt.Errorf("invalid encrypted backup payload")
	}
	salt, err := base64.StdEncoding.DecodeString(envelope.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode backup salt: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode backup nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode backup ciphertext: %w", err)
	}
	iterations := envelope.Iterations
	if iterations < 1 {
		iterations = 120000
	}
	key := pbkdf2.Key([]byte(passphrase), salt, iterations, 32, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt backup payload: %w", err)
	}
	return plain, nil
}

func uploadToR2(ctx context.Context, cfg backupConfig, objectKey string, payload []byte) error {
	req, err := signedR2Request(ctx, cfg, http.MethodPut, objectKey, nil, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("upload backup to r2: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("upload backup to r2 failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func downloadBackupKey(ctx context.Context, cfg backupConfig, key string) ([]byte, error) {
	req, err := signedR2Request(ctx, cfg, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download backup from r2: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("download backup from r2 failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read backup response: %w", err)
	}
	return body, nil
}

func rotateBackups(ctx context.Context, cfg backupConfig, keepLatest int) error {
	keys, err := listBackupKeys(ctx, cfg)
	if err != nil {
		return err
	}
	if keepLatest < 1 || len(keys) <= keepLatest {
		return nil
	}
	sort.Strings(keys)
	stale := keys[:len(keys)-keepLatest]
	for _, key := range stale {
		if err := deleteBackupKey(ctx, cfg, key); err != nil {
			return err
		}
	}
	return nil
}

func listBackupKeys(ctx context.Context, cfg backupConfig) ([]string, error) {
	items, err := listBackupItems(ctx, cfg)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Key) != "" {
			keys = append(keys, strings.TrimSpace(item.Key))
		}
	}
	return keys, nil
}

func listBackupItems(ctx context.Context, cfg backupConfig) ([]backupRemoteItem, error) {
	query := url.Values{}
	query.Set("list-type", "2")
	prefix := strings.Trim(strings.TrimSpace(cfg.R2Prefix), "/")
	if prefix != "" {
		query.Set("prefix", prefix+"/")
	}
	req, err := signedR2Request(ctx, cfg, http.MethodGet, "", query, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("list backups failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read list backups response: %w", err)
	}
	return parseS3ListItems(body), nil
}

func deleteBackupKey(ctx context.Context, cfg backupConfig, key string) error {
	req, err := signedR2Request(ctx, cfg, http.MethodDelete, key, nil, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("delete backup failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func backupDownloadName(key string) string {
	name := path.Base(strings.TrimSpace(key))
	if name == "" || name == "." || name == "/" {
		return "backup.tar.gz"
	}
	switch {
	case strings.HasSuffix(name, ".tar.gz.enc"):
		return strings.TrimSuffix(name, ".enc")
	case strings.HasSuffix(name, ".enc"):
		return strings.TrimSuffix(name, ".enc") + ".tar.gz"
	case strings.HasSuffix(name, ".tar.gz"):
		return name
	default:
		return name + ".tar.gz"
	}
}

func parseS3ListItems(body []byte) []backupRemoteItem {
	type content struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		Size         int64  `xml:"Size"`
	}
	type result struct {
		Contents []content `xml:"Contents"`
	}
	var parsed result
	if err := xmlUnmarshal(body, &parsed); err != nil {
		return nil
	}
	items := make([]backupRemoteItem, 0, len(parsed.Contents))
	for _, item := range parsed.Contents {
		if strings.TrimSpace(item.Key) != "" {
			items = append(items, backupRemoteItem{
				Key:          strings.TrimSpace(item.Key),
				SizeBytes:    item.Size,
				LastModified: strings.TrimSpace(item.LastModified),
			})
		}
	}
	return items
}

func xmlUnmarshal(data []byte, target any) error {
	return xml.Unmarshal(data, target)
}

func signedR2Request(ctx context.Context, cfg backupConfig, method string, objectKey string, query url.Values, body []byte) (*http.Request, error) {
	baseURL := fmt.Sprintf("https://%s.r2.cloudflarestorage.com/%s", cfg.R2AccountID, cfg.R2Bucket)
	if strings.TrimSpace(objectKey) != "" {
		baseURL += "/" + strings.TrimLeft(objectKey, "/")
	}
	if len(query) > 0 {
		baseURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create r2 request: %w", err)
	}
	if err := signR2Request(req, cfg, body); err != nil {
		return nil, fmt.Errorf("sign r2 request: %w", err)
	}
	return req, nil
}

func signR2Request(req *http.Request, cfg backupConfig, body []byte) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)

	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", amzDate)

	canonicalHeaders := strings.Join([]string{
		"host:" + req.URL.Host,
		"x-amz-content-sha256:" + payloadHash,
		"x-amz-date:" + amzDate,
		"",
	}, "\n")
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL.Query()),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	scope := dateStamp + "/auto/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signingKey := deriveSigningKey(cfg.R2SecretAccessKey, dateStamp, "auto", "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	authHeader := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cfg.R2AccessKeyID,
		scope,
		signedHeaders,
		signature,
	)
	req.Header.Set("Authorization", authHeader)
	return nil
}

func canonicalPath(u *url.URL) string {
	if u == nil || u.EscapedPath() == "" {
		return "/"
	}
	return u.EscapedPath()
}

func canonicalQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	type pair struct {
		key   string
		value string
	}
	pairs := make([]pair, 0)
	for key, rawValues := range values {
		escapedKey := awsEscape(key)
		if len(rawValues) == 0 {
			pairs = append(pairs, pair{key: escapedKey, value: ""})
			continue
		}
		for _, value := range rawValues {
			pairs = append(pairs, pair{key: escapedKey, value: awsEscape(value)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	parts := make([]string, 0, len(pairs))
	for _, item := range pairs {
		parts = append(parts, item.key+"="+item.value)
	}
	return strings.Join(parts, "&")
}

func awsEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (m *backupManager) emitLog(ctx context.Context, summary string, detail map[string]any) {
	if m == nil || m.logWriter == nil {
		return
	}
	m.logWriter(ctx, "backup", summary, detail)
}
