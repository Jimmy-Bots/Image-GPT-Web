package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

type ChatRequirements struct {
	Token          string
	ProofToken     string
	TurnstileToken string
	SOToken        string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *Client) CollectText(ctx context.Context, messages []Message, model string) (string, error) {
	var builder strings.Builder
	err := c.StreamText(ctx, messages, model, func(delta string) error {
		builder.WriteString(delta)
		return nil
	})
	if err != nil {
		return "", err
	}
	return builder.String(), nil
}

func (c *Client) StreamText(ctx context.Context, messages []Message, model string, onDelta func(string) error) error {
	if len(messages) == 0 {
		return errors.New("messages are required")
	}
	if strings.TrimSpace(model) == "" {
		model = "auto"
	}
	if err := c.bootstrap(ctx); err != nil {
		return err
	}
	requirements, err := c.chatRequirements(ctx)
	if err != nil {
		return err
	}
	path, timezone := c.chatTarget()
	payload := c.conversationPayload(messages, model, timezone)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	headers := map[string]string{
		"Accept":       "text/event-stream",
		"Content-Type": "application/json",
		"OpenAI-Sentinel-Chat-Requirements-Token": requirements.Token,
	}
	if requirements.ProofToken != "" {
		headers["OpenAI-Sentinel-Proof-Token"] = requirements.ProofToken
	}
	if requirements.TurnstileToken != "" {
		headers["OpenAI-Sentinel-Turnstile-Token"] = requirements.TurnstileToken
	}
	if requirements.SOToken != "" {
		headers["OpenAI-Sentinel-SO-Token"] = requirements.SOToken
	}
	c.applyHeaders(req, path, headers)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == fhttp.StatusUnauthorized {
		io.Copy(io.Discard, resp.Body)
		return ErrInvalidAccessToken
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s failed: HTTP %d %s", path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return parseConversationSSE(resp.Body, onDelta)
}

func (c *Client) bootstrap(ctx context.Context) error {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Sec-Ch-Ua", `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("bootstrap failed: HTTP %d", resp.StatusCode)
	}
	html, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	c.powScriptSources, c.powDataBuild = parsePOWResources(string(html))
	return nil
}

func (c *Client) chatRequirements(ctx context.Context) (ChatRequirements, error) {
	path := "/backend-api/sentinel/chat-requirements"
	sourceP := ""
	if c.accessToken == "" {
		path = "/backend-anon/sentinel/chat-requirements"
	}
	sourceP = buildLegacyRequirementsToken(c.userAgent, c.powScriptSources, c.powDataBuild)
	body := map[string]any{"p": sourceP}
	data, err := c.postJSON(ctx, path, body, nil)
	if err != nil {
		return ChatRequirements{}, err
	}
	if arkose, ok := data["arkose"].(map[string]any); ok && boolValue(arkose["required"]) {
		return ChatRequirements{}, errors.New("chat requirements requires arkose token")
	}
	req := ChatRequirements{
		Token:   stringValue(data["token"], ""),
		SOToken: stringValue(data["so_token"], ""),
	}
	if proof, ok := data["proofofwork"].(map[string]any); ok && boolValue(proof["required"]) {
		token, err := buildProofToken(
			stringValue(proof["seed"], ""),
			stringValue(proof["difficulty"], ""),
			c.userAgent,
			c.powScriptSources,
			c.powDataBuild,
		)
		if err != nil {
			return ChatRequirements{}, err
		}
		req.ProofToken = token
	}
	if req.Token == "" {
		return ChatRequirements{}, fmt.Errorf("missing chat requirements token")
	}
	return req, nil
}

func (c *Client) chatTarget() (string, string) {
	if c.accessToken != "" {
		return "/backend-api/conversation", "Asia/Shanghai"
	}
	return "/backend-anon/conversation", "America/Los_Angeles"
}

func (c *Client) conversationPayload(messages []Message, model string, timezone string) map[string]any {
	return map[string]any{
		"action":                        "next",
		"messages":                      conversationMessages(messages),
		"model":                         model,
		"parent_message_id":             newUUID(),
		"conversation_mode":             map[string]any{"kind": "primary_assistant"},
		"conversation_origin":           nil,
		"force_paragen":                 false,
		"force_paragen_model_slug":      "",
		"force_rate_limit":              false,
		"force_use_sse":                 true,
		"history_and_training_disabled": true,
		"reset_rate_limits":             false,
		"suggestions":                   []any{},
		"supported_encodings":           []any{},
		"system_hints":                  []any{},
		"timezone":                      timezone,
		"timezone_offset_min":           -480,
		"variant_purpose":               "comparison_implicit",
		"websocket_request_id":          newUUID(),
		"client_contextual_info": map[string]any{
			"is_dark_mode":       false,
			"time_since_loaded":  120,
			"page_height":        900,
			"page_width":         1400,
			"pixel_ratio":        2,
			"screen_height":      1440,
			"screen_width":       2560,
			"app_name":           "chatgpt.com",
			"supports_buffering": true,
		},
	}
}

func conversationMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		out = append(out, map[string]any{
			"id":     newUUID(),
			"author": map[string]any{"role": role},
			"content": map[string]any{
				"content_type": "text",
				"parts":        []string{message.Content},
			},
		})
	}
	return out
}

func parseConversationSSE(reader io.Reader, onDelta func(string) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	currentText := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			return nil
		}
		nextText, ok := assistantTextFromPayload(payload, currentText)
		if !ok || nextText == currentText {
			continue
		}
		delta := nextText
		if strings.HasPrefix(nextText, currentText) {
			delta = nextText[len(currentText):]
		}
		currentText = nextText
		if delta != "" {
			if err := onDelta(delta); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func assistantTextFromPayload(payload string, currentText string) (string, bool) {
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return currentText, false
	}
	if text, ok := assistantTextFromEvent(event); ok {
		return text, true
	}
	if text, ok := patchTextFromEvent(event, currentText); ok {
		return text, true
	}
	return currentText, false
}

func assistantTextFromEvent(event map[string]any) (string, bool) {
	candidates := []any{event, event["v"]}
	for _, candidate := range candidates {
		item, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		message, ok := item["message"].(map[string]any)
		if !ok {
			continue
		}
		author, _ := message["author"].(map[string]any)
		if stringValue(author["role"], "") != "assistant" {
			continue
		}
		content, _ := message["content"].(map[string]any)
		parts := sliceValue(content["parts"])
		var builder strings.Builder
		for _, part := range parts {
			if text, ok := part.(string); ok {
				builder.WriteString(text)
			}
		}
		if builder.Len() > 0 {
			return builder.String(), true
		}
	}
	return "", false
}

func patchTextFromEvent(event map[string]any, currentText string) (string, bool) {
	if event["p"] == "/message/content/parts/0" {
		return applyPatchOperation(event, currentText)
	}
	if op, ok := event["o"].(string); ok && op == "patch" {
		ops := sliceValue(event["v"])
		text := currentText
		changed := false
		for _, raw := range ops {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			next, ok := patchTextFromEvent(item, text)
			if ok {
				text = next
				changed = true
			}
		}
		return text, changed
	}
	return currentText, false
}

func applyPatchOperation(operation map[string]any, currentText string) (string, bool) {
	value := stringValue(operation["v"], "")
	switch operation["o"] {
	case "append":
		return currentText + value, true
	case "replace":
		return value, true
	default:
		return currentText, false
	}
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("uuid: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true") || typed == "1"
	default:
		return false
	}
}
