package api

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

type accountAutoRefresher struct {
	store    *storage.Store
	upstream Upstream
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	runMu    sync.Mutex
	running  bool
	cursor   int
	stateMu  sync.RWMutex
	nextRun  time.Time
	lastRun  accountAutoRefreshState
}

const (
	defaultAutoRefreshIntervalMinutes = 5
	defaultAutoRefreshNormalBatch     = 8
	defaultAutoRefreshConcurrency     = 4
)

type accountAutoRefreshState struct {
	Running         bool   `json:"running"`
	IntervalMinutes int    `json:"interval_minutes"`
	Concurrency     int    `json:"concurrency"`
	NormalBatchSize int    `json:"normal_batch_size"`
	DueCount        int    `json:"due_count"`
	NextRunAt       string `json:"next_run_at,omitempty"`
	LastStartedAt   string `json:"last_started_at,omitempty"`
	LastFinishedAt  string `json:"last_finished_at,omitempty"`
	LastDurationMS  int64  `json:"last_duration_ms,omitempty"`
	LastSelected    int    `json:"last_selected"`
	LastLimited     int    `json:"last_limited"`
	LastNormal      int    `json:"last_normal"`
	LastRefreshed   int    `json:"last_refreshed"`
	LastFailed      int    `json:"last_failed"`
	LastError       string `json:"last_error,omitempty"`
}

func newAccountAutoRefresher(store *storage.Store, upstream Upstream) *accountAutoRefresher {
	return &accountAutoRefresher{
		store:    store,
		upstream: upstream,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (r *accountAutoRefresher) Start() {
	if r == nil || r.store == nil || r.upstream == nil {
		return
	}
	go r.loop()
}

func (r *accountAutoRefresher) Close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	<-r.done
}

func (r *accountAutoRefresher) loop() {
	defer close(r.done)
	interval := r.nextInterval()
	r.setNextRun(time.Now().Add(interval))
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-timer.C:
			r.setNextRun(time.Time{})
			r.runOnce()
			interval = r.nextInterval()
			r.setNextRun(time.Now().Add(interval))
			timer.Reset(interval)
		}
	}
}

func (r *accountAutoRefresher) nextInterval() time.Duration {
	settings, err := r.store.GetSettings(context.Background())
	if err != nil {
		return defaultAutoRefreshIntervalMinutes * time.Minute
	}
	minutes := intMapValue(settings, "refresh_account_interval_minute")
	if minutes < 1 {
		minutes = defaultAutoRefreshIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func (r *accountAutoRefresher) runOnce() {
	r.runMu.Lock()
	if r.running {
		r.runMu.Unlock()
		return
	}
	r.running = true
	r.runMu.Unlock()
	defer func() {
		r.runMu.Lock()
		r.running = false
		r.runMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	startedAt := time.Now().UTC()

	accounts, err := r.store.ListAccounts(ctx)
	if err != nil {
		log.Printf("account_auto_refresh list_accounts_failed err=%v", err)
		r.recordRun(accountAutoRefreshState{
			LastStartedAt:  startedAt.Format(time.RFC3339),
			LastFinishedAt: time.Now().UTC().Format(time.RFC3339),
			LastDurationMS: time.Since(startedAt).Milliseconds(),
			LastError:      fmt.Sprintf("list_accounts_failed: %v", err),
		})
		return
	}
	tokens, limitedCount, normalCount := r.pickTokens(accounts)
	if len(tokens) == 0 {
		r.recordRun(accountAutoRefreshState{
			LastStartedAt:  startedAt.Format(time.RFC3339),
			LastFinishedAt: time.Now().UTC().Format(time.RFC3339),
			LastDurationMS: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	log.Printf("account_auto_refresh start total=%d limited=%d normal=%d", len(tokens), limitedCount, normalCount)
	refreshed, errorsList := r.upstream.RefreshAccounts(ctx, tokens)
	state := accountAutoRefreshState{
		LastStartedAt:  startedAt.Format(time.RFC3339),
		LastFinishedAt: time.Now().UTC().Format(time.RFC3339),
		LastDurationMS: time.Since(startedAt).Milliseconds(),
		LastSelected:   len(tokens),
		LastLimited:    limitedCount,
		LastNormal:     normalCount,
		LastRefreshed:  refreshed,
		LastFailed:     len(errorsList),
	}
	if len(errorsList) > 0 {
		state.LastError = summarizeAutoRefreshErrors(errorsList)
		r.recordRun(state)
		log.Printf("account_auto_refresh done refreshed=%d failed=%d", refreshed, len(errorsList))
		return
	}
	r.recordRun(state)
	log.Printf("account_auto_refresh done refreshed=%d failed=0", refreshed)
}

func (r *accountAutoRefresher) pickTokens(accounts []domain.Account) ([]string, int, int) {
	limited := make([]string, 0)
	normal := make([]domain.Account, 0)
	for _, item := range accounts {
		if item.AccessToken == "" || item.Status == "禁用" || item.Status == "异常" {
			continue
		}
		if item.Status == "限流" {
			limited = append(limited, item.AccessToken)
			continue
		}
		normal = append(normal, item)
	}
	sort.Slice(normal, func(i, j int) bool {
		if normal[i].UpdatedAt.Equal(normal[j].UpdatedAt) {
			return normal[i].AccessToken < normal[j].AccessToken
		}
		return normal[i].UpdatedAt.Before(normal[j].UpdatedAt)
	})
	batch := r.normalBatchSize()
	if batch > len(normal) {
		batch = len(normal)
	}
	selected := make([]string, 0, len(limited)+batch)
	selected = append(selected, limited...)
	if batch == 0 {
		return selected, len(limited), 0
	}
	start := 0
	if len(normal) > 0 {
		start = r.cursor % len(normal)
	}
	for i := 0; i < batch; i++ {
		idx := (start + i) % len(normal)
		selected = append(selected, normal[idx].AccessToken)
	}
	r.cursor += batch
	return compactStrings(selected), len(limited), batch
}

func dueRefreshTokens(accounts []domain.Account, intervalMinutes int, now time.Time) []string {
	if intervalMinutes < 1 {
		intervalMinutes = defaultAutoRefreshIntervalMinutes
	}
	interval := time.Duration(intervalMinutes) * time.Minute
	candidates := make([]domain.Account, 0, len(accounts))
	for _, item := range accounts {
		if item.AccessToken == "" || item.Status == "禁用" || item.Status == "异常" {
			continue
		}
		if item.UpdatedAt.IsZero() || !item.UpdatedAt.Add(interval).After(now) {
			candidates = append(candidates, item)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].UpdatedAt.Equal(candidates[j].UpdatedAt) {
			return candidates[i].AccessToken < candidates[j].AccessToken
		}
		return candidates[i].UpdatedAt.Before(candidates[j].UpdatedAt)
	})
	tokens := make([]string, 0, len(candidates))
	for _, item := range candidates {
		tokens = append(tokens, item.AccessToken)
	}
	return compactStrings(tokens)
}

func (r *accountAutoRefresher) Status(ctx context.Context) accountAutoRefreshState {
	state := accountAutoRefreshState{
		IntervalMinutes: defaultAutoRefreshIntervalMinutes,
		Concurrency:     defaultAutoRefreshConcurrency,
		NormalBatchSize: defaultAutoRefreshNormalBatch,
	}
	if r == nil {
		return state
	}
	if settings, err := r.store.GetSettings(ctx); err == nil {
		if minutes := intMapValue(settings, "refresh_account_interval_minute"); minutes > 0 {
			state.IntervalMinutes = minutes
		}
		if concurrency := intMapValue(settings, "refresh_account_concurrency"); concurrency > 0 {
			state.Concurrency = concurrency
		}
		if batch := intMapValue(settings, "refresh_account_normal_batch_size"); batch > 0 {
			state.NormalBatchSize = batch
		}
	}
	if accounts, err := r.store.ListAccounts(ctx); err == nil {
		state.DueCount = len(dueRefreshTokens(accounts, state.IntervalMinutes, time.Now()))
	}
	r.runMu.Lock()
	state.Running = r.running
	r.runMu.Unlock()
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	state.NextRunAt = formatAutoRefreshTime(r.nextRun)
	state.LastStartedAt = r.lastRun.LastStartedAt
	state.LastFinishedAt = r.lastRun.LastFinishedAt
	state.LastDurationMS = r.lastRun.LastDurationMS
	state.LastSelected = r.lastRun.LastSelected
	state.LastLimited = r.lastRun.LastLimited
	state.LastNormal = r.lastRun.LastNormal
	state.LastRefreshed = r.lastRun.LastRefreshed
	state.LastFailed = r.lastRun.LastFailed
	state.LastError = r.lastRun.LastError
	return state
}

func (r *accountAutoRefresher) normalBatchSize() int {
	settings, err := r.store.GetSettings(context.Background())
	if err != nil {
		return defaultAutoRefreshNormalBatch
	}
	batch := intMapValue(settings, "refresh_account_normal_batch_size")
	if batch < 1 {
		return defaultAutoRefreshNormalBatch
	}
	return batch
}

func (r *accountAutoRefresher) setNextRun(value time.Time) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.nextRun = value.UTC()
}

func (r *accountAutoRefresher) recordRun(state accountAutoRefreshState) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.lastRun = state
}

func formatAutoRefreshTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func summarizeAutoRefreshErrors(items []map[string]string) string {
	if len(items) == 0 {
		return ""
	}
	first := strings.TrimSpace(items[0]["error"])
	if first == "" {
		first = "refresh failed"
	}
	if len(items) == 1 {
		return first
	}
	return fmt.Sprintf("%s (+%d more)", first, len(items)-1)
}
