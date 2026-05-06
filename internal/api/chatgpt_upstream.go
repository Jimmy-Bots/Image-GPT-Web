package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	logWriter  structuredLogWriter
	refreshMu  sync.Mutex
	refreshSem chan struct{}
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
		refreshSem: make(chan struct{}, defaultAutoRefreshConcurrency),
	}
}

func (u *ChatGPTUpstream) SetLogWriter(writer structuredLogWriter) {
	u.logWriter = writer
}

func (u *ChatGPTUpstream) ensureRefreshConcurrency(limit int) {
	if limit < 1 {
		limit = defaultAutoRefreshConcurrency
	}
	u.refreshMu.Lock()
	defer u.refreshMu.Unlock()
	if u.refreshSem != nil && cap(u.refreshSem) == limit {
		return
	}
	u.refreshSem = make(chan struct{}, limit)
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
	appendImageModels(result, allowedPublicModelsFromSettingsMust(u.store.GetSettings(ctx)))
	return result, nil
}

func (u *ChatGPTUpstream) GenerateImage(ctx context.Context, req ImageGenerationPayload) (map[string]any, error) {
	maxCount := defaultImageMaxCount
	if settings, err := u.store.GetSettings(ctx); err == nil {
		if limit := intMapValue(settings, "image_max_count"); limit > 0 {
			maxCount = limit
		}
	}
	if req.N < 1 {
		req.N = 1
	}
	if req.N > maxCount {
		return nil, fmt.Errorf("n must be between 1 and %d", maxCount)
	}
	results := make([]map[string]any, 0, req.N)
	attempted := make(map[string]struct{})
	var lastErr error
	for index := 0; index < req.N; index++ {
		account, release, err := u.pool.AcquireImage(ctx, attempted)
		if err != nil {
			if len(results) > 0 {
				break
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		start := time.Now()
		log.Printf("upstream_image generate_attempt index=%d account=%s model=%s size=%s format=%s", index+1, maskToken(account.AccessToken), req.Model, req.Size, req.ResponseFormat)
		imageResults, err := chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).GenerateImage(ctx, chatgpt.ImageRequest{
			Prompt:         req.Prompt,
			Model:          req.Model,
			Size:           req.Size,
			ResponseFormat: req.ResponseFormat,
			PollTimeout:    120 * time.Second,
		})
		release()
		if err != nil {
			duration := time.Since(start).Milliseconds()
			log.Printf("upstream_image generate_failed index=%d account=%s duration_ms=%d err=%v", index+1, maskToken(account.AccessToken), duration, err)
			attempted[account.AccessToken] = struct{}{}
			lastErr = err
			u.markImageResult(ctx, account.AccessToken, false)
			appendStructuredLogAttempt(ctx, map[string]any{
				"status":      "attempt_failed",
				"mode":        "generate",
				"attempt":     index + 1,
				"token_ref":   accountTokenRef(account.AccessToken),
				"account":     maskToken(account.AccessToken),
				"duration_ms": duration,
				"error":       err.Error(),
				"will_switch": true,
			})
			if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
				status := "异常"
				quota := 0
				_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
				index--
				continue
			}
			index--
			continue
		}
		duration := time.Since(start).Milliseconds()
		log.Printf("upstream_image generate_success index=%d account=%s results=%d duration_ms=%d", index+1, maskToken(account.AccessToken), len(imageResults), duration)
		appendStructuredLogAttempt(ctx, map[string]any{
			"status":      len(attempted) > 0 && index > 0,
			"mode":        "generate",
			"attempt":     index + 1,
			"token_ref":   accountTokenRef(account.AccessToken),
			"account":     maskToken(account.AccessToken),
			"duration_ms": duration,
			"items":       len(imageResults),
			"success":     true,
		})
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
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("image generation returned no image")
	}
	return map[string]any{"created": time.Now().Unix(), "data": results}, nil
}

func (u *ChatGPTUpstream) EditImage(ctx context.Context, req ImageEditPayload) (map[string]any, error) {
	maxCount := defaultImageMaxCount
	if settings, err := u.store.GetSettings(ctx); err == nil {
		if limit := intMapValue(settings, "image_max_count"); limit > 0 {
			maxCount = limit
		}
	}
	if req.N < 1 {
		req.N = 1
	}
	if req.N > maxCount {
		return nil, fmt.Errorf("n must be between 1 and %d", maxCount)
	}
	if len(req.Images) == 0 {
		return nil, fmt.Errorf("image is required")
	}
	results := make([]map[string]any, 0, req.N)
	attempted := make(map[string]struct{})
	var lastErr error
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
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		start := time.Now()
		log.Printf("upstream_image edit_attempt index=%d account=%s model=%s size=%s format=%s images=%d", index+1, maskToken(account.AccessToken), req.Model, req.Size, req.ResponseFormat, len(req.Images))
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
			duration := time.Since(start).Milliseconds()
			log.Printf("upstream_image edit_failed index=%d account=%s duration_ms=%d err=%v", index+1, maskToken(account.AccessToken), duration, err)
			attempted[account.AccessToken] = struct{}{}
			lastErr = err
			u.markImageResult(ctx, account.AccessToken, false)
			appendStructuredLogAttempt(ctx, map[string]any{
				"status":      "attempt_failed",
				"mode":        "edit",
				"attempt":     index + 1,
				"token_ref":   accountTokenRef(account.AccessToken),
				"account":     maskToken(account.AccessToken),
				"duration_ms": duration,
				"error":       err.Error(),
				"will_switch": true,
			})
			if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
				status := "异常"
				quota := 0
				_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
				index--
				continue
			}
			index--
			continue
		}
		duration := time.Since(start).Milliseconds()
		log.Printf("upstream_image edit_success index=%d account=%s results=%d duration_ms=%d", index+1, maskToken(account.AccessToken), len(imageResults), duration)
		appendStructuredLogAttempt(ctx, map[string]any{
			"status":      len(attempted) > 0 && index > 0,
			"mode":        "edit",
			"attempt":     index + 1,
			"token_ref":   accountTokenRef(account.AccessToken),
			"account":     maskToken(account.AccessToken),
			"duration_ms": duration,
			"items":       len(imageResults),
			"success":     true,
		})
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
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("image edit returned no image")
	}
	return map[string]any{"created": time.Now().Unix(), "data": results}, nil
}

func (u *ChatGPTUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
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

func (u *ChatGPTUpstream) StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	model := stringFromAny(req["model"], "auto")
	messages, err := messagesFromChatRequest(req)
	if err != nil {
		return err
	}
	account, err := u.nextTextAccount(ctx, nil)
	if err != nil {
		return err
	}
	completionID := "chatcmpl-" + auth.RandomID(12)
	created := time.Now().Unix()
	sentRole := false
	err = chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).StreamText(ctx, messages, model, func(delta string) error {
		payloadDelta := map[string]any{"content": delta}
		if !sentRole {
			payloadDelta["role"] = "assistant"
			sentRole = true
		}
		return onEvent(chatCompletionChunk(completionID, model, created, payloadDelta, nil))
	})
	if err != nil {
		if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
			status := "异常"
			quota := 0
			_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
		}
		return err
	}
	_ = u.store.MarkAccountUsed(ctx, account.AccessToken)
	if !sentRole {
		if err := onEvent(chatCompletionChunk(completionID, model, created, map[string]any{"role": "assistant", "content": ""}, nil)); err != nil {
			return err
		}
	}
	stop := "stop"
	return onEvent(chatCompletionChunk(completionID, model, created, map[string]any{}, &stop))
}

func (u *ChatGPTUpstream) Responses(ctx context.Context, req map[string]any) (map[string]any, error) {
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

func (u *ChatGPTUpstream) StreamResponses(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	model := stringFromAny(req["model"], "auto")
	messages := messagesFromResponseRequest(req)
	if len(messages) == 0 {
		return fmt.Errorf("input is required")
	}
	account, err := u.nextTextAccount(ctx, nil)
	if err != nil {
		return err
	}
	responseID := "resp_" + auth.RandomID(12)
	itemID := "msg_" + auth.RandomID(12)
	created := time.Now().Unix()
	fullText := strings.Builder{}
	if err := onEvent(responseCreatedEvent(responseID, model, created)); err != nil {
		return err
	}
	if err := onEvent(map[string]any{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item":         responseTextItem(itemID, "", "in_progress"),
	}); err != nil {
		return err
	}
	err = chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).StreamText(ctx, messages, model, func(delta string) error {
		fullText.WriteString(delta)
		return onEvent(map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       itemID,
			"output_index":  0,
			"content_index": 0,
			"delta":         delta,
		})
	})
	if err != nil {
		if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
			status := "异常"
			quota := 0
			_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
		}
		return err
	}
	_ = u.store.MarkAccountUsed(ctx, account.AccessToken)
	text := fullText.String()
	if err := onEvent(map[string]any{
		"type":          "response.output_text.done",
		"item_id":       itemID,
		"output_index":  0,
		"content_index": 0,
		"text":          text,
	}); err != nil {
		return err
	}
	item := responseTextItem(itemID, text, "completed")
	if err := onEvent(map[string]any{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item":         item,
	}); err != nil {
		return err
	}
	return onEvent(responseCompletedEvent(responseID, model, created, []map[string]any{item}))
}

func (u *ChatGPTUpstream) AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error) {
	model := stringFromAny(req["model"], "auto")
	messages := messagesFromAnthropicRequest(req)
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages are required")
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
	return anthropicMessageResponse(model, content, messages), nil
}

func (u *ChatGPTUpstream) StreamAnthropicMessages(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	model := stringFromAny(req["model"], "auto")
	messages := messagesFromAnthropicRequest(req)
	if len(messages) == 0 {
		return fmt.Errorf("messages are required")
	}
	account, err := u.nextTextAccount(ctx, nil)
	if err != nil {
		return err
	}
	messageID := "msg_" + auth.RandomID(12)
	inputTokens := messageTokenCount(messages)
	outputText := strings.Builder{}
	if err := onEvent(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
		},
	}); err != nil {
		return err
	}
	if err := onEvent(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
		return err
	}
	err = chatgpt.NewClient(account.AccessToken, chatgpt.WithHTTPClient(u.httpClient)).StreamText(ctx, messages, model, func(delta string) error {
		outputText.WriteString(delta)
		return onEvent(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": delta}})
	})
	if err != nil {
		if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
			status := "异常"
			quota := 0
			_, _ = u.store.UpdateAccount(ctx, account.AccessToken, storage.AccountUpdate{Status: &status, Quota: &quota})
		}
		return err
	}
	_ = u.store.MarkAccountUsed(ctx, account.AccessToken)
	if err := onEvent(map[string]any{"type": "content_block_stop", "index": 0}); err != nil {
		return err
	}
	if err := onEvent(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": roughTokenCount(outputText.String())},
	}); err != nil {
		return err
	}
	return onEvent(map[string]any{"type": "message_stop"})
}

func (u *ChatGPTUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	if len(tokens) == 0 {
		return 0, []map[string]string{}
	}
	workerCount := defaultAutoRefreshConcurrency
	autoRemoveInvalid := false
	settings, err := u.store.GetSettings(ctx)
	if err == nil {
		workerCount = intMapValue(settings, "refresh_account_concurrency")
		autoRemoveInvalid = boolMapValue(settings, "auto_remove_invalid_accounts")
		u.ensureRefreshConcurrency(workerCount)
	}
	if workerCount < 1 {
		workerCount = defaultAutoRefreshConcurrency
	}
	type jobResult struct {
		token string
		err   error
	}
	jobs := make(chan string)
	results := make(chan jobResult, len(tokens))
	if len(tokens) < workerCount {
		workerCount = len(tokens)
	}
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for token := range jobs {
				jobCtx := withStructuredLog(ctx, u.logWriter, "account", map[string]any{
					"token_ref": accountTokenRef(token),
					"account":   maskToken(token),
				})
				select {
				case <-ctx.Done():
					results <- jobResult{token: token, err: ctx.Err()}
					continue
				case u.refreshSem <- struct{}{}:
				}
				info, err := chatgpt.NewClient(token, chatgpt.WithHTTPClient(u.httpClient)).UserInfo(jobCtx)
				<-u.refreshSem
				replacedToken := token
				if err == nil {
					_, err = u.store.UpdateAccountRemoteInfo(jobCtx, token, info)
				}
				if errors.Is(err, chatgpt.ErrInvalidAccessToken) {
					_ = u.store.SetAccountRecovery(jobCtx, token, "recovering", err.Error())
					emitStructuredLog(jobCtx, "账号 access token 失效，尝试重登录", map[string]any{
						"status":    "relogin_start",
						"token_ref": accountTokenRef(token),
						"account":   maskToken(token),
						"error":     err.Error(),
					})
					reloginResult, reloginErr := u.reloginAccount(jobCtx, token)
					if reloginErr == nil {
						replacedToken = reloginResult.NewToken
						info, err = chatgpt.NewClient(reloginResult.NewToken, chatgpt.WithHTTPClient(u.httpClient)).UserInfo(jobCtx)
						if err == nil {
							_, err = u.store.UpdateAccountRemoteInfo(jobCtx, reloginResult.NewToken, info)
						}
						if err == nil {
							_ = u.store.SetAccountRecovery(jobCtx, reloginResult.NewToken, "", "")
							emitStructuredLog(jobCtx, "账号重登录刷新成功", map[string]any{
								"status":        "relogin_success",
								"token_ref":     accountTokenRef(reloginResult.NewToken),
								"old_token_ref": accountTokenRef(token),
								"account":       maskToken(reloginResult.NewToken),
								"email":         reloginResult.Email,
							})
						} else {
							_ = u.store.SetAccountRecovery(jobCtx, reloginResult.NewToken, "recover_failed", err.Error())
							emitStructuredLog(jobCtx, "账号重登录后刷新失败", map[string]any{
								"status":        "relogin_refresh_failed",
								"token_ref":     accountTokenRef(reloginResult.NewToken),
								"old_token_ref": accountTokenRef(token),
								"account":       maskToken(reloginResult.NewToken),
								"email":         reloginResult.Email,
								"error":         err.Error(),
							})
						}
					} else {
						status := "异常"
						quota := 0
						_, _ = u.store.UpdateAccount(jobCtx, token, storage.AccountUpdate{Status: &status, Quota: &quota})
						_ = u.store.SetAccountRecovery(jobCtx, token, "recover_failed", reloginErr.Error())
						emitStructuredLog(jobCtx, "账号重登录失败", map[string]any{
							"status":    "relogin_failed",
							"token_ref": accountTokenRef(token),
							"account":   maskToken(token),
							"error":     reloginErr.Error(),
						})
						if autoRemoveInvalid {
							removed, removeErr := u.store.DeleteAccounts(jobCtx, []string{token})
							if removeErr != nil {
								emitStructuredLog(jobCtx, "自动移除异常账号失败", map[string]any{
									"status":    "auto_remove_failed",
									"token_ref": accountTokenRef(token),
									"account":   maskToken(token),
									"error":     removeErr.Error(),
									"reason":    "relogin_failed",
								})
							} else if removed > 0 {
								emitStructuredLog(jobCtx, "自动移除异常账号", map[string]any{
									"status":    "auto_removed",
									"token_ref": accountTokenRef(token),
									"account":   maskToken(token),
									"removed":   removed,
									"reason":    "relogin_failed",
									"error":     reloginErr.Error(),
								})
							}
						}
						err = reloginErr
					}
				}
				results <- jobResult{token: replacedToken, err: err}
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
				"access_token": result.token,
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

func appendImageModels(result map[string]any, allowed []string) {
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
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, item := range allowed {
		allowedSet[item] = struct{}{}
	}
	for _, id := range []string{"gpt-image-2", "codex-gpt-image-2"} {
		if len(allowedSet) > 0 {
			if _, ok := allowedSet[id]; !ok {
				continue
			}
		}
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

func allowedPublicModelsFromSettingsMust(settings map[string]any, err error) []string {
	if err != nil {
		return nil
	}
	return allowedPublicModelsFromSettings(settings)
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

func messagesFromAnthropicRequest(req map[string]any) []chatgpt.Message {
	messages := make([]chatgpt.Message, 0)
	if system := textFromAnthropicContent(req["system"]); system != "" {
		messages = append(messages, chatgpt.Message{Role: "system", Content: system})
	}
	rawMessages, _ := req["messages"].([]any)
	for _, raw := range rawMessages {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content := textFromAnthropicContent(item["content"])
		if content == "" {
			continue
		}
		role := stringFromAny(item["role"], "user")
		if role != "assistant" && role != "system" {
			role = "user"
		}
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

func textFromAnthropicContent(content any) string {
	switch typed := content.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, raw := range typed {
			switch item := raw.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				blockType := stringFromAny(item["type"], "")
				switch blockType {
				case "text":
					parts = append(parts, stringFromAny(item["text"], ""))
				case "tool_use":
					parts = append(parts, anthropicToolUseText(item))
				case "tool_result":
					parts = append(parts, "Tool result "+stringFromAny(item["tool_use_id"], "")+": "+textFromAnthropicContent(item["content"]))
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		if text := stringFromAny(typed["text"], ""); text != "" {
			return text
		}
		return textFromAnthropicContent(typed["content"])
	default:
		return ""
	}
}

func anthropicToolUseText(item map[string]any) string {
	name := stringFromAny(item["name"], "")
	input := item["input"]
	payload, _ := json.Marshal(input)
	return "<tool_calls><tool_call><tool_name>" + name + "</tool_name><parameters>" + string(payload) + "</parameters></tool_call></tool_calls>"
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

func anthropicMessageResponse(model string, content string, messages []chatgpt.Message) map[string]any {
	return map[string]any{
		"id":            "msg_" + auth.RandomID(12),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []map[string]any{{"type": "text", "text": content}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  messageTokenCount(messages),
			"output_tokens": roughTokenCount(content),
		},
	}
}

func messageTokenCount(messages []chatgpt.Message) int {
	total := 0
	for _, message := range messages {
		total += roughTokenCount(message.Content)
	}
	return total
}

func chatCompletionChunk(id string, model string, created int64, delta map[string]any, finishReason *string) map[string]any {
	var finish any
	if finishReason != nil {
		finish = *finishReason
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
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

func responseCreatedEvent(id string, model string, created int64) map[string]any {
	return map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":                  id,
			"object":              "response",
			"created_at":          created,
			"status":              "in_progress",
			"error":               nil,
			"incomplete_details":  nil,
			"model":               model,
			"output":              []any{},
			"parallel_tool_calls": false,
		},
	}
}

func responseCompletedEvent(id string, model string, created int64, output []map[string]any) map[string]any {
	return map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":                  id,
			"object":              "response",
			"created_at":          created,
			"status":              "completed",
			"error":               nil,
			"incomplete_details":  nil,
			"model":               model,
			"output":              output,
			"parallel_tool_calls": false,
		},
	}
}

func responseTextItem(id string, text string, status string) map[string]any {
	return map[string]any{
		"id":     id,
		"type":   "message",
		"status": status,
		"role":   "assistant",
		"content": []map[string]any{{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
		}},
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
