package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"

	"gpt-image-web/internal/auth"
	"gpt-image-web/internal/domain"
)

const (
	defaultBaseURL           = "https://chatgpt.com"
	defaultClientVersion     = "prod-be885abbfcfe7b1f511e88b3003d9ee44757fbad"
	defaultClientBuildNumber = "5955942"
)

var (
	ErrInvalidAccessToken = errors.New("invalid_access_token")
	ErrImagePromptAdjust  = errors.New("image_prompt_adjust_required")
)

type ImagePromptAdjustError struct {
	Text string
}

func (e *ImagePromptAdjustError) Error() string {
	text := strings.TrimSpace(e.Text)
	if text == "" {
		return "上游返回了文本说明，请调整提示词后重试。"
	}
	return text
}

func (e *ImagePromptAdjustError) Unwrap() error {
	return ErrImagePromptAdjust
}

type HTTPDoer interface {
	Do(req *fhttp.Request) (*fhttp.Response, error)
}

type Client struct {
	baseURL           string
	accessToken       string
	httpClient        HTTPDoer
	userAgent         string
	deviceID          string
	sessionID         string
	clientVersion     string
	clientBuildNumber string
	powScriptSources  []string
	powDataBuild      string
}

type Option func(*Client)

func WithHTTPClient(httpClient HTTPDoer) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			c.baseURL = baseURL
		}
	}
}

func NewClient(accessToken string, options ...Option) *Client {
	c := &Client{
		baseURL:           defaultBaseURL,
		accessToken:       strings.TrimSpace(accessToken),
		httpClient:        mustDefaultHTTPClient(""),
		userAgent:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0",
		deviceID:          auth.RandomID(18),
		sessionID:         auth.RandomID(18),
		clientVersion:     defaultClientVersion,
		clientBuildNumber: defaultClientBuildNumber,
	}
	for _, option := range options {
		option(c)
	}
	return c
}

func NewHTTPClient(proxyURL string) (HTTPDoer, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profiles.Chrome_133),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithTimeoutSeconds(300),
	}
	if strings.TrimSpace(proxyURL) != "" {
		options = append(options, tlsclient.WithProxyUrl(strings.TrimSpace(proxyURL)))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}

func mustDefaultHTTPClient(proxyURL string) HTTPDoer {
	client, err := NewHTTPClient(proxyURL)
	if err != nil {
		panic(err)
	}
	return client
}

func (c *Client) UserInfo(ctx context.Context) (domain.Account, error) {
	if c.accessToken == "" {
		return domain.Account{}, errors.New("access token is required")
	}

	type result struct {
		name string
		data map[string]any
		err  error
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	ch := make(chan result, 3)
	go func() {
		data, err := c.getJSON(ctx, "/backend-api/me", nil)
		if err != nil {
			err = fmt.Errorf("/backend-api/me: %w", err)
		}
		ch <- result{name: "me", data: data, err: err}
	}()
	go func() {
		data, err := c.postJSON(ctx, "/backend-api/conversation/init", map[string]any{
			"gizmo_id":                nil,
			"requested_default_model": nil,
			"conversation_id":         nil,
			"timezone_offset_min":     -480,
		}, nil)
		if err != nil {
			err = fmt.Errorf("/backend-api/conversation/init: %w", err)
		}
		ch <- result{name: "init", data: data, err: err}
	}()
	go func() {
		data, err := c.getJSON(ctx, "/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480", map[string]string{
			"X-OpenAI-Target-Path":  "/backend-api/accounts/check/v4-2023-04-27",
			"X-OpenAI-Target-Route": "/backend-api/accounts/check/v4-2023-04-27",
		})
		if err != nil {
			err = fmt.Errorf("/backend-api/accounts/check/v4-2023-04-27: %w", err)
		}
		ch <- result{name: "account", data: data, err: err}
	}()

	var mePayload, initPayload, accountPayload map[string]any
	for i := 0; i < 3; i++ {
		item := <-ch
		if item.err != nil {
			return domain.Account{}, item.err
		}
		switch item.name {
		case "me":
			mePayload = item.data
		case "init":
			initPayload = item.data
		case "account":
			accountPayload = item.data
		}
	}

	defaultAccount := nestedMap(accountPayload, "accounts", "default", "account")
	planType := stringValue(defaultAccount["plan_type"], "free")
	limitsProgress := sliceValue(initPayload["limits_progress"])
	quota, restoreAt, unknown := extractImageQuota(limitsProgress)
	status := "正常"
	if !unknown && quota == 0 {
		status = "限流"
	}
	if unknown && strings.EqualFold(planType, "free") {
		status = "限流"
	}

	limitsJSON, _ := json.Marshal(limitsProgress)
	rawJSON, _ := json.Marshal(map[string]any{
		"me":      mePayload,
		"init":    initPayload,
		"account": defaultAccount,
	})
	return domain.Account{
		AccessToken:       c.accessToken,
		Type:              planType,
		Status:            status,
		Quota:             quota,
		ImageQuotaUnknown: unknown,
		Email:             stringValue(mePayload["email"], ""),
		UserID:            stringValue(mePayload["id"], ""),
		LimitsProgress:    limitsJSON,
		DefaultModelSlug:  stringValue(initPayload["default_model_slug"], ""),
		RestoreAt:         restoreAt,
		RawJSON:           rawJSON,
	}, nil
}

func (c *Client) ListModels(ctx context.Context) (map[string]any, error) {
	data, err := c.getJSON(ctx, "/backend-api/models?history_and_training_disabled=false", map[string]string{
		"X-OpenAI-Target-Path":  "/backend-api/models",
		"X-OpenAI-Target-Route": "/backend-api/models",
	})
	if err != nil {
		return nil, err
	}
	models := sliceValue(data["models"])
	items := make([]map[string]any, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		slug := strings.TrimSpace(stringValue(item["slug"], ""))
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		items = append(items, map[string]any{
			"id":         slug,
			"object":     "model",
			"created":    intValue(item["created"], 0),
			"owned_by":   stringValue(item["owned_by"], "chatgpt"),
			"permission": []any{},
			"root":       slug,
			"parent":     nil,
		})
	}
	return map[string]any{"object": "list", "data": items}, nil
}

func (c *Client) getJSON(ctx context.Context, path string, extraHeaders map[string]string) (map[string]any, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req, targetPath(path), extraHeaders)
	return c.doJSON(req)
}

func (c *Client) postJSON(ctx context.Context, path string, payload any, extraHeaders map[string]string) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req, targetPath(path), extraHeaders)
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req)
}

func (c *Client) doJSON(req *fhttp.Request) (map[string]any, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == fhttp.StatusUnauthorized {
		io.Copy(io.Discard, resp.Body)
		return nil, ErrInvalidAccessToken
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("%s failed: HTTP %d %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) applyHeaders(req *fhttp.Request, target string, extra map[string]string) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("Sec-Ch-Ua", `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("OAI-Device-Id", c.deviceID)
	req.Header.Set("OAI-Session-Id", c.sessionID)
	req.Header.Set("OAI-Language", "zh-CN")
	req.Header.Set("OAI-Client-Version", c.clientVersion)
	req.Header.Set("OAI-Client-Build-Number", c.clientBuildNumber)
	req.Header.Set("X-OpenAI-Target-Path", target)
	req.Header.Set("X-OpenAI-Target-Route", target)
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	for key, value := range extra {
		if value == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func targetPath(path string) string {
	if before, _, ok := strings.Cut(path, "?"); ok {
		return before
	}
	return path
}

func extractImageQuota(items []any) (int, string, bool) {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(item["feature_name"], "") != "image_gen" {
			continue
		}
		return intValue(item["remaining"], 0), stringValue(item["reset_after"], ""), false
	}
	return 0, "", true
}

func nestedMap(root map[string]any, keys ...string) map[string]any {
	current := root
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		current = next
	}
	return current
}

func sliceValue(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return []any{}
}

func stringValue(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return fallback
	}
	return text
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		v, err := typed.Int64()
		if err == nil {
			return int(v)
		}
	}
	return fallback
}
