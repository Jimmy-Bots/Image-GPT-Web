package register

import (
	"context"
	"encoding/json"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
	"gpt-image-web/internal/upstream/chatgpt"
)

type StorageAccountRepository struct {
	Store *storage.Store
}

func (r StorageAccountRepository) AddAccessToken(ctx context.Context, token string, password string) (bool, error) {
	return r.Store.UpsertAccountToken(ctx, token, password)
}

func (r StorageAccountRepository) RefreshAccount(ctx context.Context, token string) error {
	client := chatgpt.NewClient(token)
	info, err := client.UserInfo(ctx)
	if err != nil {
		return err
	}
	_, err = r.Store.UpdateAccountRemoteInfo(ctx, token, info)
	return err
}

func (r StorageAccountRepository) ListAccounts(ctx context.Context) ([]AccountSnapshot, error) {
	items, err := r.Store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, AccountSnapshot{
			Status:            item.Status,
			Quota:             item.Quota,
			ImageQuotaUnknown: item.ImageQuotaUnknown,
		})
	}
	return out, nil
}

type StorageLogSink struct {
	Store *storage.Store
	Now   func() time.Time
}

func (s StorageLogSink) Log(ctx context.Context, level string, summary string, detail map[string]any) error {
	if s.Store == nil {
		return nil
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}
	payload, _ := json.Marshal(detail)
	return s.Store.AddLog(ctx, domain.SystemLog{
		ID:      randomID(newDefaultRandomSource(), 12),
		Time:    now,
		Type:    registerLogType,
		Summary: summary,
		Detail:  payload,
	})
}
