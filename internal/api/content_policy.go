package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type aiReviewSettings struct {
	Enabled bool
	BaseURL string
	APIKey  string
	Model   string
	Prompt  string
}

func (s *Server) checkContentPolicy(w http.ResponseWriter, r *http.Request, identity Identity, endpoint string, model string, text string) bool {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return false
	}
	if blocked := firstSensitiveWord(text, settings); blocked != "" {
		errText := "sensitive word rejected"
		s.logCall(r, identity, endpoint, model, "failed", errText, map[string]any{
			"matched_rule": blocked,
			"content_gate": "sensitive_words",
		})
		writeError(w, http.StatusBadRequest, "content_rejected", "request contains sensitive word")
		return false
	}
	if review := aiReviewConfig(settings); review.Enabled {
		if err := s.reviewContent(r.Context(), review, text); err != nil {
			s.logCall(r, identity, endpoint, model, "failed", err.Error(), map[string]any{
				"content_gate": "external_review",
			})
			writeError(w, http.StatusBadRequest, "content_rejected", err.Error())
			return false
		}
	}
	return true
}

func (s *Server) reviewContent(ctx context.Context, settings aiReviewSettings, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if settings.BaseURL == "" || settings.APIKey == "" || settings.Model == "" {
		return fmt.Errorf("ai review config is incomplete")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	if s.cfg.ProxyURL != "" {
		proxyURL, err := url.Parse(s.cfg.ProxyURL)
		if err != nil {
			return fmt.Errorf("invalid proxy url: %w", err)
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}
	prompt := settings.Prompt
	if prompt == "" {
		prompt = "判断用户请求是否允许。只回答 ALLOW 或 REJECT。"
	}
	payload := map[string]any{
		"model":       settings.Model,
		"temperature": 0,
		"messages": []map[string]string{{
			"role":    "user",
			"content": prompt + "\n\n用户请求:\n" + text + "\n\n只回答 ALLOW 或 REJECT。",
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(settings.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+settings.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ai review failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ai review failed: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var data map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return fmt.Errorf("ai review failed: %w", err)
	}
	answer := strings.TrimSpace(strings.ToLower(reviewAnswer(data)))
	if answer == "" {
		return fmt.Errorf("ai review returned empty result")
	}
	if strings.HasPrefix(answer, "allow") ||
		strings.HasPrefix(answer, "pass") ||
		strings.HasPrefix(answer, "true") ||
		strings.HasPrefix(answer, "yes") ||
		strings.HasPrefix(answer, "通过") ||
		strings.HasPrefix(answer, "允许") ||
		strings.HasPrefix(answer, "安全") {
		return nil
	}
	return fmt.Errorf("ai review rejected request")
}

func reviewAnswer(data map[string]any) string {
	choices, _ := data["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if content := stringFromAny(message["content"], ""); content != "" {
		return content
	}
	return stringFromAny(choice["text"], "")
}

func firstSensitiveWord(text string, settings map[string]any) string {
	words, ok := settings["sensitive_words"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range words {
		rule := strings.TrimSpace(fmt.Sprint(raw))
		if rule == "" {
			continue
		}
		if sensitiveRuleMatches(text, rule) {
			return rule
		}
	}
	return ""
}

func sensitiveRuleMatches(text string, rule string) bool {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return false
	}
	normalizedText := strings.ToLower(text)
	if strings.HasPrefix(strings.ToLower(rule), "re:") {
		pattern := strings.TrimSpace(rule[3:])
		if pattern == "" {
			return false
		}
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return false
		}
		return re.MatchString(normalizedText)
	}
	normalizedRule := strings.ToLower(rule)
	if strings.ContainsAny(normalizedRule, "*?") {
		pattern := wildcardToRegexp(normalizedRule)
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(normalizedText)
	}
	return strings.Contains(normalizedText, normalizedRule)
}

func wildcardToRegexp(rule string) string {
	var builder strings.Builder
	builder.WriteString("(?i)")
	for _, ch := range rule {
		switch ch {
		case '*':
			builder.WriteString(".*")
		case '?':
			builder.WriteString(".")
		default:
			builder.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	return builder.String()
}

func aiReviewConfig(settings map[string]any) aiReviewSettings {
	raw, ok := settings["ai_review"].(map[string]any)
	if !ok {
		return aiReviewSettings{}
	}
	return aiReviewSettings{
		Enabled: boolFromAny(raw["enabled"]),
		BaseURL: strings.TrimSpace(stringFromAny(raw["base_url"], "")),
		APIKey:  strings.TrimSpace(stringFromAny(raw["api_key"], "")),
		Model:   strings.TrimSpace(stringFromAny(raw["model"], "")),
		Prompt:  strings.TrimSpace(stringFromAny(raw["prompt"], "")),
	}
}
