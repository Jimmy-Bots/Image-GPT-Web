package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gpt-image-web/internal/domain"
)

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	store, err := Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store, ctx
}

func TestApplyDailyTemporaryQuotaResetsToConfiguredTodayQuota(t *testing.T) {
	store, ctx := openTestStore(t)
	defer store.Close()

	user := domain.User{
		ID:                 "u1",
		Email:              "u1@example.com",
		Name:               "u1",
		PasswordHash:       "hash",
		Role:               domain.RoleUser,
		Status:             domain.UserStatusActive,
		PermanentQuota:     5,
		TemporaryQuota:     2,
		TemporaryQuotaDate: "2026-05-06",
		DailyTemporaryQuota: 9,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	got, err := store.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	if got.TemporaryQuota != 9 {
		t.Fatalf("temporary_quota=%d want=9", got.TemporaryQuota)
	}
	if got.TemporaryQuotaDate != today {
		t.Fatalf("temporary_quota_date=%q want=%q", got.TemporaryQuotaDate, today)
	}
	if got.AvailableQuota != 14 {
		t.Fatalf("available_quota=%d want=14", got.AvailableQuota)
	}
}

func TestUpdateUserTemporaryQuotaDoesNotOverwriteDailyConfiguredQuota(t *testing.T) {
	store, ctx := openTestStore(t)
	defer store.Close()

	user := domain.User{
		ID:                 "u2",
		Email:              "u2@example.com",
		Name:               "u2",
		PasswordHash:       "hash",
		Role:               domain.RoleUser,
		Status:             domain.UserStatusActive,
		PermanentQuota:     3,
		TemporaryQuota:     5,
		TemporaryQuotaDate: time.Now().Format("2006-01-02"),
		DailyTemporaryQuota: 7,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	next := 11
	got, err := store.UpdateUser(ctx, user.ID, UserUpdate{
		TemporaryQuota:     &next,
		TemporaryQuotaDate: &today,
	})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if got.TemporaryQuota != 11 {
		t.Fatalf("temporary_quota=%d want=11", got.TemporaryQuota)
	}
	if got.DailyTemporaryQuota != 7 {
		t.Fatalf("daily_temporary_quota=%d want=7", got.DailyTemporaryQuota)
	}
}
