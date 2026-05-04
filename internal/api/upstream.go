package api

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrUpstreamNotImplemented = errors.New("upstream_not_implemented")

type Upstream interface {
	ListModels(ctx context.Context) (map[string]any, error)
	GenerateImage(ctx context.Context, req ImageGenerationPayload) (map[string]any, error)
	EditImage(ctx context.Context, req ImageEditPayload) (map[string]any, error)
	ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error)
	StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error
	Responses(ctx context.Context, req map[string]any) (map[string]any, error)
	StreamResponses(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error
	AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error)
	RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string)
}

type ImageGenerationPayload struct {
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	N              int    `json:"n"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format"`
	Stream         bool   `json:"stream,omitempty"`
}

type ImageEditPayload struct {
	Prompt         string        `json:"prompt"`
	Model          string        `json:"model"`
	N              int           `json:"n"`
	Size           string        `json:"size,omitempty"`
	ResponseFormat string        `json:"response_format"`
	Images         []UploadImage `json:"-"`
}

type UploadImage struct {
	Name        string
	ContentType string
	Data        []byte
}

type NotImplementedUpstream struct{}

func (NotImplementedUpstream) ListModels(ctx context.Context) (map[string]any, error) {
	return map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "gpt-image-2", "object": "model", "owned_by": "chatgpt2api-go"},
			{"id": "codex-gpt-image-2", "object": "model", "owned_by": "chatgpt2api-go"},
			{"id": "auto", "object": "model", "owned_by": "chatgpt2api-go"},
			{"id": "gpt-5", "object": "model", "owned_by": "chatgpt2api-go"},
		},
	}, nil
}

func (NotImplementedUpstream) GenerateImage(ctx context.Context, req ImageGenerationPayload) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (NotImplementedUpstream) EditImage(ctx context.Context, req ImageEditPayload) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (NotImplementedUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (NotImplementedUpstream) StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (NotImplementedUpstream) Responses(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (NotImplementedUpstream) StreamResponses(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	return ErrUpstreamNotImplemented
}

func (NotImplementedUpstream) AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error) {
	return nil, ErrUpstreamNotImplemented
}

func (NotImplementedUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	errorsList := make([]map[string]string, 0, len(tokens))
	for _, token := range tokens {
		errorsList = append(errorsList, map[string]string{"access_token": token, "error": "upstream refresh not implemented yet"})
	}
	return 0, errorsList
}

func jsonData(value any) json.RawMessage {
	payload, _ := json.Marshal(value)
	return payload
}
