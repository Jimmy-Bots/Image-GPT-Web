package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

var ErrNoAvailableAccount = errors.New("no available account")

type AccountPool struct {
	store         *storage.Store
	maxPerAccount int
	mu            sync.Mutex
	nextIndex     int
	imageInflight map[string]int
}

func NewAccountPool(store *storage.Store, maxPerAccount int) *AccountPool {
	if maxPerAccount < 1 {
		maxPerAccount = 1
	}
	pool := &AccountPool{
		store:         store,
		maxPerAccount: maxPerAccount,
		imageInflight: make(map[string]int),
	}
	return pool
}

func (p *AccountPool) AcquireImage(ctx context.Context, excluded map[string]struct{}) (domain.Account, func(), error) {
	excluded = copySet(excluded)
	for {
		accounts, err := p.store.ListAccounts(ctx)
		if err != nil {
			return domain.Account{}, nil, err
		}
		candidates := readyImageAccounts(accounts, excluded)
		if len(candidates) == 0 {
			return domain.Account{}, nil, ErrNoAvailableAccount
		}
		p.mu.Lock()
		for offset := 0; offset < len(candidates); offset++ {
			index := (p.nextIndex + offset) % len(candidates)
			item := candidates[index]
			if p.imageInflight[item.AccessToken] >= p.maxPerAccount {
				continue
			}
			p.nextIndex = index + 1
			p.imageInflight[item.AccessToken]++
			p.mu.Unlock()
			var once sync.Once
			release := func() {
				once.Do(func() {
					p.release(item.AccessToken)
				})
			}
			return item, release, nil
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return domain.Account{}, nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (p *AccountPool) release(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.imageInflight[token] <= 1 {
		delete(p.imageInflight, token)
	} else {
		p.imageInflight[token]--
	}
}

func readyImageAccounts(accounts []domain.Account, excluded map[string]struct{}) []domain.Account {
	out := make([]domain.Account, 0, len(accounts))
	for _, item := range accounts {
		if item.AccessToken == "" {
			continue
		}
		if _, skip := excluded[item.AccessToken]; skip {
			continue
		}
		switch item.Status {
		case "禁用", "异常", "限流":
			continue
		}
		if item.ImageQuotaUnknown || item.Quota > 0 {
			out = append(out, item)
		}
	}
	return out
}

func copySet(input map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(input))
	for key := range input {
		out[key] = struct{}{}
	}
	return out
}
