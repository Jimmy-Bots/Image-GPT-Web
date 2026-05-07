package integration

import (
	"context"

	"gpt-image-web/internal/api"
)

type fakeStreamUpstream struct{}

func (fakeStreamUpstream) ListModels(ctx context.Context) (map[string]any, error) {
	return map[string]any{"object": "list", "data": []any{}}, nil
}

func (fakeStreamUpstream) GenerateImage(ctx context.Context, req api.ImageGenerationPayload) (map[string]any, error) {
	return map[string]any{"data": []any{}}, nil
}

func (fakeStreamUpstream) EditImage(ctx context.Context, req api.ImageEditPayload) (map[string]any, error) {
	return map[string]any{"data": []any{}}, nil
}

func (fakeStreamUpstream) ChatCompletions(ctx context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (fakeStreamUpstream) StreamChatCompletions(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	if err := onEvent(map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": int64(1),
		"model":   "auto",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": "hel"}, "finish_reason": nil}},
	}); err != nil {
		return err
	}
	return onEvent(map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": int64(1),
		"model":   "auto",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	})
}

func (fakeStreamUpstream) Responses(ctx context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (fakeStreamUpstream) StreamResponses(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	if err := onEvent(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_test"}}); err != nil {
		return err
	}
	return onEvent(map[string]any{"type": "response.output_text.delta", "delta": "hi"})
}

func (fakeStreamUpstream) AnthropicMessages(ctx context.Context, req map[string]any) (map[string]any, error) {
	return map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"model":         "auto",
		"content":       []map[string]any{{"type": "text", "text": "hello"}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 1, "output_tokens": 1},
	}, nil
}

func (fakeStreamUpstream) StreamAnthropicMessages(ctx context.Context, req map[string]any, onEvent func(map[string]any) error) error {
	if err := onEvent(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_test"}}); err != nil {
		return err
	}
	if err := onEvent(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "hello"}}); err != nil {
		return err
	}
	return onEvent(map[string]any{"type": "message_stop"})
}

func (fakeStreamUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	return 0, nil
}

var _ api.Upstream = fakeStreamUpstream{}
