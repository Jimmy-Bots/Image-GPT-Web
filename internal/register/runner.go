package register

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Runner struct {
	registrar *Registrar
	repo      AccountRepository
	logger    Logger
	now       func() time.Time
}

func NewRunner(registrar *Registrar, repo AccountRepository, logger Logger, now func() time.Time) (*Runner, error) {
	if registrar == nil {
		return nil, errors.New("registrar is required")
	}
	if repo == nil {
		return nil, ErrAccountRepoRequired
	}
	if logger == nil {
		logger = LoggerFunc(func(context.Context, string, string, ...any) {})
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Runner{registrar: registrar, repo: repo, logger: logger, now: now}, nil
}

func (r *Runner) Run(ctx context.Context, cfg BatchConfig) (BatchState, error) {
	cfg = cfg.withDefaults()
	startedAt := r.now()
	state := BatchState{
		Config:  cfg,
		Enabled: true,
		Stats: BatchStats{
			JobID:     randomID(newDefaultRandomSource(), 12),
			Threads:   cfg.Threads,
			StartedAt: &startedAt,
			UpdatedAt: &startedAt,
		},
	}
	metrics, err := r.poolMetrics(ctx)
	if err != nil {
		return state, err
	}
	state.Stats.CurrentQuota = metrics.CurrentQuota
	state.Stats.CurrentAvailable = metrics.CurrentAvailable

	type jobResult struct {
		ok  bool
		err error
	}

	results := make(chan jobResult, cfg.Threads)
	sem := make(chan struct{}, cfg.Threads)
	var wg sync.WaitGroup
	submitted := 0

	tryLaunch := func() bool {
		if state.Config.Mode == RegisterModeTotal && submitted >= state.Config.Total {
			return false
		}
		reached, err := r.targetReached(ctx, state.Config, submitted)
		if err != nil {
			results <- jobResult{ok: false, err: err}
			return false
		}
		if reached {
			return false
		}
		submitted++
		sem <- struct{}{}
		wg.Add(1)
		state.Stats.Running++
		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()
			_, err := r.registrar.Register(ctx)
			results <- jobResult{ok: err == nil, err: err}
		}(submitted)
		return true
	}

	for state.Stats.Running < cfg.Threads && tryLaunch() {
	}

	doneLaunching := false
	for state.Stats.Running > 0 || !doneLaunching {
		if state.Stats.Running == 0 && doneLaunching {
			break
		}
		if state.Stats.Running == 0 && !doneLaunching {
			select {
			case <-ctx.Done():
				return r.finishState(state, false), ctx.Err()
			case <-time.After(cfg.CheckInterval):
				if !tryLaunch() {
					doneLaunching = true
				}
			}
			continue
		}

		select {
		case <-ctx.Done():
			wg.Wait()
			return r.finishState(state, false), ctx.Err()
		case result := <-results:
			state.Stats.Running--
			state.Stats.Done++
			if result.ok {
				state.Stats.Success++
			} else {
				state.Stats.Fail++
			}
			state = r.bumpStats(state)
			for state.Stats.Running < cfg.Threads {
				if !tryLaunch() {
					doneLaunching = true
					break
				}
			}
		}
	}

	wg.Wait()
	return r.finishState(state, true), nil
}

func (r *Runner) targetReached(ctx context.Context, cfg BatchConfig, submitted int) (bool, error) {
	metrics, err := r.poolMetrics(ctx)
	if err != nil {
		return false, err
	}
	switch cfg.Mode {
	case RegisterModeQuota:
		return metrics.CurrentQuota >= cfg.TargetQuota, nil
	case RegisterModeAvailable:
		return metrics.CurrentAvailable >= cfg.TargetAvailable, nil
	default:
		return submitted >= cfg.Total, nil
	}
}

func (r *Runner) poolMetrics(ctx context.Context) (PoolMetrics, error) {
	items, err := r.repo.ListAccounts(ctx)
	if err != nil {
		return PoolMetrics{}, err
	}
	metrics := PoolMetrics{}
	for _, item := range items {
		if item.Status != "正常" {
			continue
		}
		metrics.CurrentAvailable++
		if !item.ImageQuotaUnknown {
			metrics.CurrentQuota += item.Quota
		}
	}
	return metrics, nil
}

func (r *Runner) bumpStats(state BatchState) BatchState {
	now := r.now()
	state.Stats.UpdatedAt = &now
	if state.Stats.StartedAt != nil {
		elapsed := now.Sub(*state.Stats.StartedAt).Seconds()
		state.Stats.ElapsedSeconds = round1(elapsed)
		if state.Stats.Success > 0 {
			state.Stats.AvgSeconds = round1(elapsed / float64(state.Stats.Success))
		}
		total := state.Stats.Success + state.Stats.Fail
		if total > 0 {
			state.Stats.SuccessRate = round1(float64(state.Stats.Success) * 100 / float64(total))
		}
	}
	return state
}

func (r *Runner) finishState(state BatchState, completed bool) BatchState {
	state.Enabled = false
	state = r.bumpStats(state)
	if completed {
		now := r.now()
		state.Stats.FinishedAt = &now
		state.Stats.UpdatedAt = &now
	}
	return state
}

func round1(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}
