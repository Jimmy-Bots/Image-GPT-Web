package register

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Runner struct {
	register func(context.Context) (RegisterResult, error)
	repo     AccountRepository
	logger   Logger
	now      func() time.Time
	onState  func(BatchState)
}

func NewRunner(registrar *Registrar, repo AccountRepository, logger Logger, now func() time.Time) (*Runner, error) {
	if registrar == nil {
		return nil, errors.New("registrar is required")
	}
	return newRunnerWithRegisterFunc(func(ctx context.Context) (RegisterResult, error) {
		return registrar.Register(ctx)
	}, repo, logger, now)
}

func NewRunnerFactory(factory func() (*Registrar, error), repo AccountRepository, logger Logger, now func() time.Time) (*Runner, error) {
	if factory == nil {
		return nil, errors.New("registrar factory is required")
	}
	return newRunnerWithRegisterFunc(func(ctx context.Context) (RegisterResult, error) {
		registrar, err := factory()
		if err != nil {
			return RegisterResult{}, err
		}
		return registrar.Register(ctx)
	}, repo, logger, now)
}

func newRunnerWithRegisterFunc(registerFn func(context.Context) (RegisterResult, error), repo AccountRepository, logger Logger, now func() time.Time) (*Runner, error) {
	if registerFn == nil {
		return nil, errors.New("register func is required")
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
	return &Runner{register: registerFn, repo: repo, logger: logger, now: now}, nil
}

func (r *Runner) WithStateHook(fn func(BatchState)) *Runner {
	if r == nil {
		return r
	}
	r.onState = fn
	return r
}

func (r *Runner) Run(ctx context.Context, cfg BatchConfig) (BatchState, error) {
	cfg = cfg.withDefaults()
	startedAt := r.now()
	jobID := randomID(newDefaultRandomSource(), 6)
	batchCtx := WithJobID(ctx, jobID)
	monitorMode := cfg.Mode == RegisterModeQuota || cfg.Mode == RegisterModeAvailable
	state := BatchState{
		Config:  cfg,
		Enabled: true,
		Stats: BatchStats{
			JobID:     jobID,
			Threads:   cfg.Threads,
			StartedAt: &startedAt,
			UpdatedAt: &startedAt,
		},
	}
	r.emitState(state)
	r.logger.Printf(batchCtx, "info", "batch start mode=%s total=%d threads=%d quota=%d available=%d", cfg.Mode, cfg.Total, cfg.Threads, cfg.TargetQuota, cfg.TargetAvailable)
	metrics, err := r.poolMetrics(batchCtx)
	if err != nil {
		r.logger.Printf(batchCtx, "error", "pool metrics failed: %v", err)
		return state, err
	}
	state.Stats.CurrentQuota = metrics.CurrentQuota
	state.Stats.CurrentAvailable = metrics.CurrentAvailable
	r.emitState(state)
	r.logger.Printf(batchCtx, "info", "pool metrics quota=%d available=%d", metrics.CurrentQuota, metrics.CurrentAvailable)

	type jobResult struct {
		ok  bool
		err error
	}

	results := make(chan jobResult, cfg.Threads)
	slots := make(chan int, cfg.Threads)
	for i := 1; i <= cfg.Threads; i++ {
		slots <- i
	}
	var wg sync.WaitGroup
	submitted := 0
	monitoring := false
	targetLogged := false

	tryLaunch := func() (bool, error) {
		if state.Config.Mode == RegisterModeTotal {
			if state.Stats.Success >= state.Config.Total {
				if !targetLogged {
					r.logger.Printf(batchCtx, "info", "target reached success=%d total=%d", state.Stats.Success, state.Config.Total)
					targetLogged = true
				}
				return false, nil
			}
			if state.Stats.Success+state.Stats.Running >= state.Config.Total {
				return false, nil
			}
		} else {
			reached, err := r.targetReached(batchCtx, &state)
			if err != nil {
				r.logger.Printf(batchCtx, "error", "target check failed: %v", err)
				return false, err
			}
			state = r.bumpStats(state)
			r.emitState(state)
			if reached {
				if !monitoring {
					monitoring = true
					r.logger.Printf(batchCtx, "info", "target reached, enter monitor mode quota=%d available=%d", state.Stats.CurrentQuota, state.Stats.CurrentAvailable)
				}
				return false, nil
			}
			if monitoring {
				monitoring = false
				r.logger.Printf(batchCtx, "warn", "target dropped below threshold, resume registering quota=%d available=%d", state.Stats.CurrentQuota, state.Stats.CurrentAvailable)
			}
		}
		submitted++
		threadID := <-slots
		wg.Add(1)
		state.Stats.Running++
		state = r.bumpStats(state)
		r.emitState(state)
		workerCtx := WithThread(batchCtx, threadID)
		r.logger.Printf(workerCtx, "info", "launch attempt=%d running=%d", submitted, state.Stats.Running)
		go func(slotID int, attempt int) {
			defer wg.Done()
			defer func() { slots <- slotID }()
			workerCtx := WithThread(batchCtx, slotID)
			r.logger.Printf(workerCtx, "info", "start attempt=%d", attempt)
			_, err := r.register(workerCtx)
			if err != nil {
				r.logger.Printf(workerCtx, "error", "failed attempt=%d: %v", attempt, err)
			} else {
				r.logger.Printf(workerCtx, "info", "success attempt=%d", attempt)
			}
			results <- jobResult{ok: err == nil, err: err}
		}(threadID, submitted)
		return true, nil
	}

	fillWorkers := func() error {
		for state.Stats.Running < cfg.Threads {
			launched, err := tryLaunch()
			if err != nil {
				return err
			}
			if launched {
				continue
			}
			return nil
		}
		return nil
	}

	if err := fillWorkers(); err != nil {
		return r.finishState(state, false), err
	}
	for {
		if !monitorMode && state.Stats.Success >= state.Config.Total && state.Stats.Running == 0 {
			break
		}
		if state.Stats.Running == 0 {
			select {
			case <-batchCtx.Done():
				if monitorMode {
					r.logger.Printf(batchCtx, "warn", "monitor canceled while idle")
				} else {
					r.logger.Printf(batchCtx, "warn", "canceled while idle")
				}
				return r.finishState(state, false), batchCtx.Err()
			case <-time.After(cfg.CheckInterval):
				if monitorMode {
					r.logger.Printf(batchCtx, "info", "monitor tick interval=%s quota=%d available=%d", cfg.CheckInterval, state.Stats.CurrentQuota, state.Stats.CurrentAvailable)
				} else {
					r.logger.Printf(batchCtx, "info", "tick interval=%s waiting for next launch", cfg.CheckInterval)
				}
				if err := fillWorkers(); err != nil {
					return r.finishState(state, false), err
				}
			}
			continue
		}

		select {
		case <-batchCtx.Done():
			wg.Wait()
			r.logger.Printf(batchCtx, "warn", "canceled while workers running")
			return r.finishState(state, false), batchCtx.Err()
		case result := <-results:
			state.Stats.Running--
			state.Stats.Done++
			if result.ok {
				state.Stats.Success++
			} else {
				state.Stats.Fail++
			}
			state = r.bumpStats(state)
			r.emitState(state)
			r.logger.Printf(batchCtx, "info", "progress done=%d success=%d fail=%d running=%d rate=%s", state.Stats.Done, state.Stats.Success, state.Stats.Fail, state.Stats.Running, formatRunnerPercent(state.Stats.SuccessRate))
			if err := fillWorkers(); err != nil {
				return r.finishState(state, false), err
			}
		}
	}

	wg.Wait()
	r.logger.Printf(batchCtx, "info", "completed done=%d success=%d fail=%d elapsed=%.1fs", state.Stats.Done, state.Stats.Success, state.Stats.Fail, state.Stats.ElapsedSeconds)
	return r.finishState(state, true), nil
}

func (r *Runner) targetReached(ctx context.Context, state *BatchState) (bool, error) {
	metrics, err := r.poolMetrics(ctx)
	if err != nil {
		return false, err
	}
	state.Stats.CurrentQuota = metrics.CurrentQuota
	state.Stats.CurrentAvailable = metrics.CurrentAvailable
	switch state.Config.Mode {
	case RegisterModeQuota:
		return metrics.CurrentQuota >= state.Config.TargetQuota, nil
	case RegisterModeAvailable:
		return metrics.CurrentAvailable >= state.Config.TargetAvailable, nil
	default:
		return state.Stats.Success >= state.Config.Total, nil
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
	r.emitState(state)
	return state
}

func (r *Runner) emitState(state BatchState) {
	if r == nil || r.onState == nil {
		return
	}
	r.onState(state.Clone())
}

func round1(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}

func formatRunnerPercent(value float64) string {
	if value <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", value)
}
