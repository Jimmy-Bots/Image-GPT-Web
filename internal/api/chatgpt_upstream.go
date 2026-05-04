package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
	"gpt-image-web/internal/upstream/chatgpt"
)

type ChatGPTUpstream struct {
	store      *storage.Store
	pool       *AccountPool
	httpClient chatgpt.HTTPDoer
	textMu     sync.Mutex
	textIndex  int
}

func NewChatGPTUpstream(store *storage.Store, pool *AccountPool, proxyURL string) *ChatGPTUpstream {
	httpClient, err := chatgpt.NewHTTPClient(proxyURL)
	if err != nil {
		httpClient, _ = chatgpt.NewHTTPClient("")
	}
	return &ChatGPTUpstream{
		store:      store,
		pool:       pool,
		httpClient: httpClient,
	}
}

func (u *ChatGPTUpstream) ListModels(ctx context.Context) (map[string]any, error) {
	accounts, err := u.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var token string
	for _, account := range accounts {
		if account.Status != "禁用" && account.Status != "异常" && account.AccessToken != "" {
			token = account.AccessToken
			break
		}
	}
	var result map[string]any
	if token != "" {
		result, err = chatgpt.NewClient(token, chatgpt.WithHTTPClient(u.httpClient)).ListModels(ctx)
	}
	if token == "" || err != nil {
		result = fallbackModels()
	}
	appendImageModels(result)
	return result, nil
}

func (u *ChatGPTUpstream) GenerateImage(ctx context.Context, req ImageGenerationPayload) (map[string]any, error) {
	if req.N < 1 {
		req.N = 1
	}
	if req.N > 4 {
		return nil, fmt.Errorf("n must be between 1 and 4")
	}
	results := make([]map[string]any, 0, req.N)
	attempted := make(map[string]struct{})
	for index := 0; index < req.N; index++ {
		account, release, err := u.pool.AcquireImage(ctx, attempted)
		if err != nil {
			if len(results) > 0 {
				break
			}
			return nil, err
		}
		imageResults, err := chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).GenerateImage(ctx, chatgpt.ImageRequest{
			Prompt:         req.Prompt,
			Model:          req.Model,
			Size:           req.Size,
			ResponseFormat: req.ResponseFormat,
			PollTimeout:    120 * time.Second,
		})
		release()
		if err != nil {
			attempted[account.AccessToken] = struct{}{}
			u.markImageResult(ctx, account.AccessToken, false)
			if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
				status := "异常"
				quota := 0
				_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
				index--
				continue
			}
			if len(results) > 0 {
				break
			}
			return nil, err
		}
		u.markImageResult(ctx, account.AccessToken, true)
		for _, item := range imageResults {
			result := map[string]any{"revised_prompt": item.RevisedPrompt}
			if item.B64JSON != "" {
				result["b64_json"] = item.B64JSON
			}
			if item.URL != "" {
				result["url"] = item.URL
			}
			results = append(results, result)
			if len(results) >= req.N {
				break
			}
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("image generation returned no image")
	}
	return map[string]any{"created": time.Now().Unix(), "data": results}, nil
}

func (u *ChatGPTUpstream) EditImage(ctx context.Context, req ImageEditPayload) (map[string]any, error) {
	if req.N < 1 {
		req.N = 1
	}
	if req.N > 4 {
		return nil, fmt.Errorf("n must be between 1 and 4")
	}
	if len(req.Images) == 0 {
		return nil, fmt.Errorf("image is required")
	}
	results := make([]map[string]any, 0, req.N)
	attempted := make(map[string]struct{})
	inputs := make([]chatgpt.ImageInput, 0, len(req.Images))
	for _, image := range req.Images {
		inputs = append(inputs, chatgpt.ImageInput{
			Name:        image.Name,
			ContentType: image.ContentType,
			Data:        image.Data,
		})
	}
	for index := 0; index < req.N; index++ {
		account, release, err := u.pool.AcquireImage(ctx, attempted)
		if err != nil {
			if len(results) > 0 {
				break
			}
			return nil, err
		}
		imageResults, err := chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).EditImage(ctx, chatgpt.ImageRequest{
			Prompt:         req.Prompt,
			Model:          req.Model,
			Size:           req.Size,
			ResponseFormat: req.ResponseFormat,
			PollTimeout:    120 * time.Second,
			Images:         inputs,
		})
		release()
		if err != nil {
			attempted[account.AccessToken] = struct{}{}
			u.markImageResult(ctx, account.AccessToken, false)
			if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
				status := "异常"
				quota := 0
				_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
				index--
				continue
			}
			if len(results) > 0 {
				break
			}
			return nil, err
		}
		u.markImageResult(ctx, account.AccessToken, true)
		for _, item := range imageResults {
			result := map[string]any{"revised_prompt": item.RevisedPrompt}
			if item.B64JSON != "" {
				result["b64_json"] = item.B64JSON
			}
			if item.URL != "" {
				result["url"] = item.URL
			}
			results = append(results, result)
			if len(results) >= req.N {
				break
			}
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("image edit returned no image")
	}
	return map[string]any{"created": time.Now().Unix(), "data": results}, nil
}

func (u *ChatGPTUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
	if boolFromAny(req["stream"]) {
		return nil, fmt.Errorf("streaming chat completions are not migrated yet")
	}
	model := stringFromAny(req["model"], "auto")
	messages, err := messagesFromChatRequest(req)
	if err != nil {
		return nil, err
	}
	account, err := u.nextTextAccount(ctx, nil)
	if err != nil {
		return nil, err
	}
	content, err := chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).CollectText(ctx, messages, model)
	if err != nil {
		if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
			status := "异常"
			quota := 0
			_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
		}
		return nil, err
	}
	_ = u.store.MarkAccountUsed(ctx, account.AccessToken)
	return chatCompletionResponse(model, content, messages), nil
}

func (u *ChatGPTUpstream) Responses(ctx context.Context, req map[string]any) (map[string]any, error) {
	if boolFromAny(req["stream"]) {
		return nil, fmt.Errorf("streaming responses are not migrated yet")
	}
	model := stringFromAny(req["model"], "auto")
	messages := messagesFromResponseRequest(req)
	if len(messages) == 0 {
		return nil, fmt.Errorf("input is required")
	}
	account, err := u.nextTextAccount(ctx, nil)
	if err != nil {
		return nil, err
	}
	content, err := chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).CollectText(ctx, messages, model)
	if err != nil {
		if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
			status := "异常"
			quota := 0
			_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
		}
		return nil, err
	}
	_ = u.store.MarkAccountUsed(ctx, account.AccessToken)
	return responseCreateResponse(model, content), nil
}

func (u *ChatGPTUpstream) AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (u *ChatGPTUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	if len(tokens) == 0 {
		return 0, []map[string]string{}
	}
	const maxWorkers = 10
	type jobResult struct {
		token string
		err   error
	}
	jobs := make(chan string)
	results := make(chan jobResult, len(tokens))
	workerCount := maxWorkers
	if len(tokens) < workerCount {
		workerCount = len(tokens)
	}
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for token := range jobs {
				info, err := chatgpt.NewClient(token, chatgpt.WithHTTPClient(u.httpClient)).UserInfo(ctx)
				if err == nil {
					_, err = u.store.UpdateAccountRemoteInfo(ctx, token, info)
				}
				if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
					status := "异常"
					quota := 0
					_, _ = u.store.UpdateAccount(ctx, token, storage.AccountUpdate{Status: &status, Quota: &quota})
				}
				results <- jobResult{token: token, err: err}
			}
		}()
	}
	go func() {
		for _, token := range tokens {
			select {
			case <-ctx.Done():
				break
			case jobs <- token:
			}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	refreshed := 0
	errorsList := make([]map[string]string, 0)
	for result := range results {
		if result.err != nil {
			errorsList = append(errorsList, map[string]string{
				"access_token": maskToken(result.token),
				"error":        result.err.Error(),
			})
			continue
		}
		refreshed++
	}
	return refreshed, errorsList
}

func fallbackModels() map[string]any {
	return map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "auto", "object": "model", "owned_by": "chatgpt2api-go"},
			{"id": "gpt-5", "object": "model", "owned_by": "chatgpt2api-go"},
		},
	}
}

func appendImageModels(result map[string]any) {
	rawItems, _ := result["data"].([]map[string]any)
	if rawItems == nil {
		if values, ok := result["data"].([]any); ok {
			for _, value := range values {
				if item, ok := value.(map[string]any); ok {
					rawItems = append(rawItems, item)
				}
			}
		}
	}
	seen := make(map[string]struct{}, len(rawItems))
	for _, item := range rawItems {
		if id, ok := item["id"].(string); ok {
			seen[id] = struct{}{}
		}
	}
	for _, id := range []string{"gpt-image-2", "codex-gpt-image-2"} {
		if _, ok := seen[id]; ok {
			continue
		}
		rawItems = append(rawItems, map[string]any{
			"id":         id,
			"object":     "model",
			"created":    0,
			"owned_by":   "chatgpt2api-go",
			"permission": []any{},
			"root":       id,
			"parent":     nil,
		})
	}
	result["data"] = rawItems
}

func maskToken(token string) string {
	if len(token) <= 12 {
		return "***"
	}
	return token[:6] + "..." + token[len(token)-4:]
}

func (u *ChatGPTUpstream) markImageResult(ctx context.Context, accessToken string, success bool) {
	_, _ = u.store.MarkImageResult(ctx, accessToken, success)
}

func (u *ChatGPTUpstream) nextTextAccount(ctx context.Context, excluded map[string]struct{}) (domain.Account, error) {
	accounts, err := u.store.ListAccounts(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	candidates := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if account.AccessToken == "" {
			continue
		}
		if excluded != nil {
			if _, ok := excluded[account.AccessToken]; ok {
				continue
			}
		}
		if account.Status == "禁用" || account.Status == "异常" {
			continue
		}
		candidates = append(candidates, account)
	}
	if len(candidates) == 0 {
		return domain.Account{}, ErrNoAvailableAccount
	}
	u.textMu.Lock()
	index := u.textIndex % len(candidates)
	u.textIndex++
	u.textMu.Unlock()
	return candidates[index], nil
}

func messagesFromChatRequest(req map[string]any) ([]chatgpt.Message, error) {
	if rawMessages, ok := req["messages"].([]any); ok && len(rawMessages) > 0 {
		messages := make([]chatgpt.Message, 0, len(rawMessages))
		for _, raw := range rawMessages {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			content := textFromContent(item["content"])
			if content == "" {
				continue
			}
			messages = append(messages, chatgpt.Message{
				Role:    stringFromAny(item["role"], "user"),
				Content: content,
			})
		}
		if len(messages) > 0 {
			return messages, nil
		}
	}
	prompt := stringFromAny(req["prompt"], "")
	if prompt != "" {
		return []chatgpt.Message{{Role: "user", Content: prompt}}, nil
	}
	return nil, fmt.Errorf("messages or prompt is required")
}

func messagesFromResponseRequest(req map[string]any) []chatgpt.Message {
	input := req["input"]
	if text := textFromContent(input); text != "" {
		return []chatgpt.Message{{Role: "user", Content: text}}
	}
	items, ok := input.([]any)
	if !ok {
		return nil
	}
	messages := make([]chatgpt.Message, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content := textFromContent(item["content"])
		if content == "" {
			continue
		}
		role := stringFromAny(item["role"], "user")
		messages = append(messages, chatgpt.Message{Role: role, Content: content})
	}
	return messages
}

func textFromContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		var parts []string
		for _, raw := range typed {
			switch item := raw.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				itemType := stringFromAny(item["type"], "")
				if itemType == "text" || itemType == "input_text" || itemType == "output_text" {
					parts = append(parts, stringFromAny(item["text"], ""))
				}
			}
		}
		return strings.Join(parts, "")
	case map[string]any:
		return textFromContent(typed["content"])
	default:
		return ""
	}
}

func chatCompletionResponse(model string, content string, messages []chatgpt.Message) map[string]any {
	promptTokens := 0
	for _, message := range messages {
		promptTokens += roughTokenCount(message.Content)
	}
	completionTokens := roughTokenCount(content)
	return map[string]any{
		"id":      "chatcmpl-" + auth.RandomID(12),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
}

func responseCreateResponse(model string, content string) map[string]any {
	return map[string]any{
		"id":      "resp_" + auth.RandomID(12),
		"object":  "response",
		"created": time.Now().Unix(),
		"model":   model,
		"output": []map[string]any{{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": content,
			}},
		}},
		"output_text": content,
	}
}

func boolFromAny(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

func stringFromAny(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return fallback
		}
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		if text == "" {
			return fallback
		}
		return text
	}
}

func roughTokenCount(text string) int {
	words := strings.Fields(text)
	if len(words) > 0 {
		return len(words)
	}
	if text == "" {
		return 0
	}
	return (len([]rune(text)) + 3) / 4
}
