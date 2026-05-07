package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

type taskTestUpstream struct{}

func (taskTestUpstream) ListModels(ctx context.Context) (map[string]any, error) {
	return map[string]any{"object": "list", "data": []map[string]any{}}, nil
}

func (taskTestUpstream) GenerateImage(ctx context.Context, req ImageGenerationPayload) (map[string]any, error) {
	return map[string]any{
		"data": []map[string]any{
			{"url": "/images/test.png"},
		},
	}, nil
}

func (taskTestUpstream) EditImage(ctx context.Context, req ImageEditPayload) (map[string]any, error) {
	return map[string]any{
		"data": []map[string]any{
			{"url": "/images/test.png"},
		},
	}, nil
}

func (taskTestUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (taskTestUpstream) StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (taskTestUpstream) Responses(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (taskTestUpstream) StreamResponses(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (taskTestUpstream) AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (taskTestUpstream) StreamAnthropicMessages(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (taskTestUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	return 0, nil
}

func TestTaskQueuePersistsFinalStatus(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	imagesDir := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}

	dbPath := filepath.Join(dataDir, "app.db")
	store, err := storage.Open(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
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
		ID:             "task-1",
		OwnerID:        user.ID,
		Status:         taskQueued,
		Phase:          taskPhaseQueued,
		Mode:           "generate",
		Model:          "gpt-image-2",
		Prompt:         "test",
		RequestedCount: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateImageTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	queue := NewTaskQueue(store, taskTestUpstream{}, imagesDir, "", 1, 1)
	defer queue.Close()

	if err := queue.Submit(taskJob{
		OwnerID: user.ID,
		TaskID:  task.ID,
		Mode:    "generate",
		Gen: ImageGenerationPayload{
			Prompt:         "test",
			Model:          "gpt-image-2",
			N:              1,
			ResponseFormat: "b64_json",
		},
	}); err != nil {
		t.Fatalf("submit task: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		items, err := store.ListImageTasks(ctx, user.ID, []string{task.ID}, true, true)
		if err != nil {
			t.Fatalf("list tasks: %v", err)
		}
		if len(items) == 1 && items[0].Status == taskSuccess && items[0].Phase == taskPhaseFinished {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	items, err := store.ListImageTasks(ctx, user.ID, []string{task.ID}, true, true)
	if err != nil {
		t.Fatalf("list tasks final: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one task, got %d", len(items))
	}
	t.Fatalf("expected final task status success/finished, got status=%s phase=%s", items[0].Status, items[0].Phase)
}
