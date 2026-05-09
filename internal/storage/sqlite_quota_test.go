package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/timeutil"
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
	today := timeutil.ShanghaiDayString(time.Now())
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
		TemporaryQuotaDate: timeutil.ShanghaiDayString(time.Now()),
		DailyTemporaryQuota: 7,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	today := timeutil.ShanghaiDayString(time.Now())
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

func TestListImageTasksPageFiltersDoNotUseAmbiguousColumns(t *testing.T) {
	store, ctx := openTestStore(t)
	defer store.Close()

	now := time.Now().UTC()
	user := domain.User{
		ID:             "task-user",
		Email:          "task@example.com",
		Name:           "Task User",
		PasswordHash:   "hash",
		Role:           domain.RoleUser,
		Status:         domain.UserStatusActive,
		QuotaUnlimited: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	task := domain.ImageTask{
		ID:             "task-filter-1",
		OwnerID:        user.ID,
		Status:         "success",
		Phase:          "finished",
		Mode:           "generate",
		Model:          "gpt-image-2",
		Size:           "1024x1024",
		Prompt:         "hello world",
		RequestedCount: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateImageTask(ctx, task); err != nil {
		t.Fatalf("create image task: %v", err)
	}

	items, total, err := store.ListImageTasksPage(ctx, "", ImageTaskPageQuery{
		Page:     1,
		PageSize: 25,
		Query:    "task@example.com",
		Status:   "success",
		Mode:     "generate",
		Model:    "gpt-image-2",
		Size:     "1024x1024",
		Deleted:  "active",
	})
	if err != nil {
		t.Fatalf("list image tasks page: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d want=1", total)
	}
	if len(items) != 1 {
		t.Fatalf("len(items)=%d want=1", len(items))
	}
	if items[0].ID != task.ID {
		t.Fatalf("item id=%q want=%q", items[0].ID, task.ID)
	}
}
