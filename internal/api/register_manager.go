package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gpt-image-web/internal/config"
	"gpt-image-web/internal/register"
	"gpt-image-web/internal/storage"
)

const registerLogLimit = 400

type registerLogEntry struct {
	ID      string         `json:"id"`
	Time    time.Time      `json:"time"`
	Type    string         `json:"type"`
	Summary string         `json:"summary"`
	Detail  map[string]any `json:"detail,omitempty"`
}

type registerManager struct {
	cfg      config.Config
	store    *storage.Store
	upstream Upstream
	mu       sync.RWMutex
	running  bool
	cancel   context.CancelFunc
	state    register.BatchState
	lastErr  string
	lastRun  *register.RegisterResult
	logs     []registerLogEntry
}

func newRegisterManager(cfg config.Config, store *storage.Store, upstream Upstream) *registerManager {
	manager := &registerManager{
		cfg:      cfg,
		store:    store,
		upstream: upstream,
		state: register.BatchState{
			Config: defaultRegisterBatchConfig(cfg),
		},
	}
	if store != nil {
		if settings, err := store.GetSettings(context.Background()); err == nil {
			manager.state.Config = batchConfigFromSettings(cfg, settings)
		}
	}
	return manager
}

func defaultRegisterBatchConfig(cfg config.Config) register.BatchConfig {
	mode := register.RegisterMode(strings.TrimSpace(cfg.RegisterMode))
	if mode == "" {
		mode = register.RegisterModeTotal
	}
	return register.BatchConfig{
		Proxy:           strings.TrimSpace(cfg.RegisterProxyURL),
		Total:           maxInt(cfg.RegisterTotal, 1),
		Threads:         maxInt(cfg.RegisterThreads, 1),
		Mode:            mode,
		TargetQuota:     maxInt(cfg.RegisterTargetQuota, 1),
		TargetAvailable: maxInt(cfg.RegisterTargetAvailable, 1),
		CheckInterval:   time.Duration(maxInt(cfg.RegisterCheckIntervalSeconds, 1)) * time.Second,
		Mail: map[string]any{
			"provider":          "inbucket",
			"inbucket_api_base": strings.TrimSpace(cfg.RegisterInbucketAPIBase),
			"inbucket_domains":  append([]string(nil), cfg.RegisterInbucketDomains...),
			"random_subdomain":  cfg.RegisterInbucketRandomSubdomain,
			"spamok_base_url":   "https://spamok.com",
		},
	}.WithDefaults()
}

func (m *registerManager) Runtime() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runtimeLocked()
}

func (m *registerManager) Logs() []registerLogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]registerLogEntry, len(m.logs))
	copy(out, m.logs)
	return out
}

func (m *registerManager) SaveConfig(ctx context.Context, payload map[string]any) (register.BatchState, error) {
	current, err := m.store.GetSettings(ctx)
	if err != nil {
		return register.BatchState{}, err
	}
	next := cloneMap(current)
	next["register"] = payload
	saved, err := m.store.SaveSettings(ctx, next)
	if err != nil {
		return register.BatchState{}, err
	}
	cfg := batchConfigFromSettings(m.cfg, saved)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Config = cfg
	return m.state.Clone(), nil
}

func (m *registerManager) Start(ctx context.Context) (map[string]any, error) {
	m.mu.Lock()
	if m.running {
		out := m.runtimeLocked()
		m.mu.Unlock()
		return out, nil
	}
	cfg := m.state.Config
	runCtx, cancel := context.WithCancel(context.Background())
	m.running = true
	m.cancel = cancel
	m.lastErr = ""
	m.state.Enabled = true
	m.mu.Unlock()

	go m.runBatch(runCtx, cfg)
	return m.Runtime(), nil
}

func (m *registerManager) Stop() map[string]any {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.running = false
	m.state.Enabled = false
	out := m.runtimeLocked()
	m.mu.Unlock()
	return out
}

func (m *registerManager) RunOnce(ctx context.Context) (map[string]any, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil, errors.New("register batch is already running")
	}
	cfg := m.state.Config
	m.lastErr = ""
	m.mu.Unlock()

	registrar, err := m.newRegistrar(cfg)
	if err != nil {
		return nil, err
	}
	result, err := registrar.Register(register.WithThread(ctx, 1))
	if err != nil {
		m.mu.Lock()
		m.lastErr = err.Error()
		m.mu.Unlock()
		m.addLog(ctx, "error", "single run failed", map[string]any{"error": err.Error()})
		return nil, err
	}
	copyResult := result
	m.mu.Lock()
	m.lastRun = &copyResult
	m.lastErr = ""
	m.mu.Unlock()
	m.addLog(ctx, "info", "single run success", map[string]any{
		"email":      result.Email,
		"created_at": result.CreatedAt,
	})
	return m.Runtime(), nil
}

func (m *registerManager) runBatch(ctx context.Context, cfg register.BatchConfig) {
	logger := register.LoggerFunc(func(ctx context.Context, level string, format string, args ...any) {
		message := strings.TrimSpace(fmt.Sprintf(format, args...))
		if message == "" {
			return
		}
		m.addLog(ctx, level, message, map[string]any{"level": level})
	})
	runner, err := register.NewRunnerFactory(func() (*register.Registrar, error) {
		return m.newRegistrar(cfg)
	}, register.StorageAccountRepository{
		Store:       m.store,
		ProxyURL:    fallbackString(cfg.Proxy, m.cfg.ProxyURL),
		RefreshFunc: m.refreshAccountLikePool,
	}, logger, nil)
	if err != nil {
		m.finishBatch(register.BatchState{Config: cfg}, err)
		return
	}
	runner.WithStateHook(func(state register.BatchState) {
		m.mu.Lock()
		m.state = state.Clone()
		m.state.Enabled = m.running
		m.mu.Unlock()
	})
	state, err := runner.Run(ctx, cfg)
	m.finishBatch(state, err)
}

func (m *registerManager) finishBatch(state register.BatchState, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	m.cancel = nil
	m.state = state
	m.state.Enabled = false
	if err != nil && !errors.Is(err, context.Canceled) {
		m.lastErr = err.Error()
	}
	go m.logBatchResult(context.Background(), state, err)
}

func (m *registerManager) newRegistrar(cfg register.BatchConfig) (*register.Registrar, error) {
	provider, err := registerProviderFromMailConfig(cfg.Mail)
	if err != nil {
		return nil, err
	}
	return register.New(register.Options{
		Config: register.Config{
			ProxyURL:             fallbackString(cfg.Proxy, m.cfg.ProxyURL),
			RequestTimeout:       30 * time.Second,
			SentinelTimeout:      20 * time.Second,
			TokenExchangeTimeout: 60 * time.Second,
			WaitTimeout:          30 * time.Second,
			WaitInterval:         2 * time.Second,
		},
		MailProvider: provider,
		AccountRepo: register.StorageAccountRepository{
			Store:       m.store,
			ProxyURL:    fallbackString(cfg.Proxy, m.cfg.ProxyURL),
			RefreshFunc: m.refreshAccountLikePool,
		},
		LogSink: register.LogSinkFunc(func(ctx context.Context, level string, summary string, detail map[string]any) error {
			m.addLog(ctx, level, summary, detail)
			return nil
		}),
		Logger: register.LoggerFunc(func(ctx context.Context, level string, format string, args ...any) {
			message := strings.TrimSpace(fmt.Sprintf(format, args...))
			if message == "" {
				return
			}
			m.addLog(ctx, level, message, map[string]any{"level": level})
		}),
	})
}

func (m *registerManager) refreshAccountLikePool(ctx context.Context, token string) error {
	if m.upstream == nil {
		repo := register.StorageAccountRepository{
			Store:    m.store,
			ProxyURL: strings.TrimSpace(m.cfg.ProxyURL),
		}
		return repo.RefreshAccount(ctx, token)
	}
	_, errorsList := m.upstream.RefreshAccounts(ctx, []string{token})
	if len(errorsList) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.TrimSpace(errorsList[0]["error"]))
}

func (m *registerManager) runtimeLocked() map[string]any {
	state := m.state.Clone()
	state.Enabled = m.running
	out := map[string]any{
		"state":       state,
		"last_error":  m.lastErr,
		"running":     m.running,
		"last_result": nil,
	}
	if m.lastRun != nil {
		out["last_result"] = m.lastRun
	}
	return out
}

func (m *registerManager) logBatchResult(ctx context.Context, state register.BatchState, err error) {
	detail := map[string]any{
		"mode":              state.Config.Mode,
		"threads":           state.Config.Threads,
		"total":             state.Config.Total,
		"target_quota":      state.Config.TargetQuota,
		"target_available":  state.Config.TargetAvailable,
		"check_interval":    state.Config.CheckInterval.String(),
		"success":           state.Stats.Success,
		"fail":              state.Stats.Fail,
		"done":              state.Stats.Done,
		"running":           state.Stats.Running,
		"current_quota":     state.Stats.CurrentQuota,
		"current_available": state.Stats.CurrentAvailable,
		"elapsed_seconds":   state.Stats.ElapsedSeconds,
		"avg_seconds":       state.Stats.AvgSeconds,
		"success_rate":      state.Stats.SuccessRate,
		"job_id":            state.Stats.JobID,
		"started_at":        state.Stats.StartedAt,
		"updated_at":        state.Stats.UpdatedAt,
		"finished_at":       state.Stats.FinishedAt,
	}
	switch {
	case err == nil:
		m.addLog(ctx, "info", "batch completed", detail)
	case errors.Is(err, context.Canceled):
		detail["status"] = "canceled"
		m.addLog(ctx, "warn", "batch canceled", detail)
	default:
		detail["status"] = "error"
		detail["error"] = err.Error()
		m.addLog(ctx, "error", "batch failed", detail)
	}
}

func (m *registerManager) addLog(ctx context.Context, level string, summary string, detail map[string]any) {
	entryDetail := cloneAnyMap(detail)
	if entryDetail == nil {
		entryDetail = map[string]any{}
	}
	if thread := register.ThreadFromContext(ctx); thread > 0 && entryDetail["thread"] == nil {
		entryDetail["thread"] = thread
	}
	if jobID := register.JobIDFromContext(ctx); jobID != "" && entryDetail["job_id"] == nil {
		entryDetail["job_id"] = jobID
	}
	entry := registerLogEntry{
		ID:      randomLogID(),
		Time:    time.Now().UTC(),
		Type:    "register",
		Summary: summary,
		Detail:  entryDetail,
	}
	if entry.Detail == nil {
		entry.Detail = map[string]any{}
	}
	if strings.TrimSpace(level) != "" && entry.Detail["level"] == nil {
		entry.Detail["level"] = strings.TrimSpace(level)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, entry)
	if len(m.logs) > registerLogLimit {
		m.logs = append([]registerLogEntry(nil), m.logs[len(m.logs)-registerLogLimit:]...)
	}
}

func batchConfigFromSettings(cfg config.Config, settings map[string]any) register.BatchConfig {
	out := defaultRegisterBatchConfig(cfg)
	registerSettings := mapAnyValue(settings["register"])
	if len(registerSettings) == 0 {
		return out
	}
	if proxy := stringMapValue(registerSettings, "proxy"); proxy != "" {
		out.Proxy = proxy
	}
	if total := intMapValue(registerSettings, "total"); total > 0 {
		out.Total = total
	}
	if threads := intMapValue(registerSettings, "threads"); threads > 0 {
		out.Threads = threads
	}
	if mode := strings.TrimSpace(anyString(registerSettings["mode"])); mode != "" {
		out.Mode = register.RegisterMode(mode)
	}
	if quota := intMapValue(registerSettings, "target_quota"); quota > 0 {
		out.TargetQuota = quota
	}
	if available := intMapValue(registerSettings, "target_available"); available > 0 {
		out.TargetAvailable = available
	}
	if seconds := intMapValue(registerSettings, "check_interval_seconds"); seconds > 0 {
		out.CheckInterval = time.Duration(seconds) * time.Second
	}
	mail := mapAnyValue(registerSettings["mail"])
	if provider := stringMapValue(mail, "provider"); provider != "" {
		out.Mail["provider"] = provider
	}
	if base := stringMapValue(mail, "inbucket_api_base"); base != "" {
		out.Mail["inbucket_api_base"] = base
	}
	if domains := stringSliceMapValue(mail, "inbucket_domains"); len(domains) > 0 {
		out.Mail["inbucket_domains"] = domains
	}
	if _, ok := mail["random_subdomain"]; ok {
		out.Mail["random_subdomain"] = boolMapValue(mail, "random_subdomain")
	}
	if base := stringMapValue(mail, "spamok_base_url"); base != "" {
		out.Mail["spamok_base_url"] = base
	}
	return out.WithDefaults()
}

func registerProviderFromMailConfig(mail map[string]any) (register.MailProvider, error) {
	switch strings.ToLower(strings.TrimSpace(stringMapValue(mail, "provider"))) {
	case "", "inbucket":
		return register.NewInbucketMailProvider(register.InbucketConfig{
			APIBase:         stringMapValue(mail, "inbucket_api_base"),
			Domains:         stringSliceMapValue(mail, "inbucket_domains"),
			RandomSubdomain: boolMapValue(mail, "random_subdomain"),
			RequestTimeout:  30 * time.Second,
			WaitTimeout:     30 * time.Second,
			WaitInterval:    2 * time.Second,
		}, nil)
	case "spamok":
		return register.NewSpamOKMailProvider(register.SpamOKConfig{
			BaseURL:        fallbackString(stringMapValue(mail, "spamok_base_url"), "https://spamok.com"),
			Domain:         "spamok.com",
			RequestTimeout: 30 * time.Second,
			WaitTimeout:    30 * time.Second,
			WaitInterval:   2 * time.Second,
		}, nil)
	default:
		return nil, fmt.Errorf("unsupported mail provider: %s", strings.TrimSpace(stringMapValue(mail, "provider")))
	}
}

func cloneMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return cloneMap(src)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return cloneMap(src)
	}
	return out
}

func mapAnyValue(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if out, ok := value.(map[string]any); ok && out != nil {
		return out
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func stringMapValue(values map[string]any, key string) string {
	return strings.TrimSpace(anyString(values[key]))
}

func stringSliceMapValue(values map[string]any, key string) []string {
	return anySliceToStrings(values[key])
}

func boolMapValue(values map[string]any, key string) bool {
	switch value := values[key].(type) {
	case bool:
		return value
	case string:
		value = strings.ToLower(strings.TrimSpace(value))
		return value == "1" || value == "true" || value == "yes" || value == "on"
	case float64:
		return value != 0
	default:
		return false
	}
}

func intMapValue(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		number, _ := value.Int64()
		return int(number)
	case string:
		number, _ := strconv.Atoi(strings.TrimSpace(value))
		return number
	default:
		return 0
	}
}

func anySliceToStrings(value any) []string {
	switch raw := value.(type) {
	case []string:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			text := strings.TrimSpace(anyString(item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return strings.TrimSpace(fallback)
	}
	return strings.TrimSpace(value)
}

func maxInt(value int, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}

func anyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case json.Number:
		return v.String()
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
