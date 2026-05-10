package chatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type imageTraceContextKey struct{}
type imageTraceModeContextKey struct{}

type imageTraceMode struct {
	rawEnabled bool
}

type imageTrace struct {
	mu   sync.Mutex
	data map[string]any
}

func WithImageTrace(ctx context.Context) context.Context {
	return WithImageTraceMode(ctx, false)
}

func WithImageTraceMode(ctx context.Context, rawEnabled bool) context.Context {
	if imageTraceFromContext(ctx) != nil {
		return context.WithValue(ctx, imageTraceModeContextKey{}, imageTraceMode{rawEnabled: rawEnabled})
	}
	ctx = context.WithValue(ctx, imageTraceContextKey{}, &imageTrace{
		data: map[string]any{},
	})
	return context.WithValue(ctx, imageTraceModeContextKey{}, imageTraceMode{rawEnabled: rawEnabled})
}

func imageTraceRawEnabled(ctx context.Context) bool {
	mode, _ := ctx.Value(imageTraceModeContextKey{}).(imageTraceMode)
	return mode.rawEnabled
}

func ImageTraceSnapshot(ctx context.Context) map[string]any {
	trace := imageTraceFromContext(ctx)
	if trace == nil {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.data) == 0 {
		return nil
	}
	raw, err := json.Marshal(trace.data)
	if err != nil {
		return cloneAnyMap(trace.data)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return cloneAnyMap(trace.data)
	}
	return out
}

func traceImageSet(ctx context.Context, key string, value any) {
	trace := imageTraceFromContext(ctx)
	if trace == nil || strings.TrimSpace(key) == "" {
		return
	}
	if !imageTraceRawEnabled(ctx) && isRawTraceKey(key) {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.data[key] = sanitizeImageTraceValue(value, 0)
}

func traceImageAppend(ctx context.Context, key string, value any, limit int) {
	trace := imageTraceFromContext(ctx)
	if trace == nil || strings.TrimSpace(key) == "" {
		return
	}
	if !imageTraceRawEnabled(ctx) && isRawTraceKey(key) {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	var items []any
	if raw, ok := trace.data[key]; ok {
		if typed, ok := raw.([]any); ok {
			items = append(items, typed...)
		}
	}
	items = append(items, sanitizeImageTraceValue(value, 0))
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	trace.data[key] = items
}

func traceJSONString(value any, maxLen int) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return truncateTraceString(fmt.Sprintf("%v", value), maxLen)
	}
	return truncateTraceString(string(raw), maxLen)
}

func imageTraceFromContext(ctx context.Context) *imageTrace {
	trace, _ := ctx.Value(imageTraceContextKey{}).(*imageTrace)
	return trace
}

func sanitizeImageTraceValue(value any, depth int) any {
	if depth > 4 {
		return "<max_depth>"
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return truncateTraceString(typed, 4000)
	case []string:
		limit := len(typed)
		if limit > 16 {
			limit = 16
		}
		out := make([]any, 0, limit+1)
		for _, item := range typed[:limit] {
			out = append(out, sanitizeImageTraceValue(item, depth+1))
		}
		if len(typed) > limit {
			out = append(out, fmt.Sprintf("<truncated %d items>", len(typed)-limit))
		}
		return out
	case []any:
		limit := len(typed)
		if limit > 16 {
			limit = 16
		}
		out := make([]any, 0, limit+1)
		for _, item := range typed[:limit] {
			out = append(out, sanitizeImageTraceValue(item, depth+1))
		}
		if len(typed) > limit {
			out = append(out, fmt.Sprintf("<truncated %d items>", len(typed)-limit))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		count := 0
		for key, item := range typed {
			if count >= 32 {
				out["_truncated_keys"] = len(typed) - count
				break
			}
			out[key] = sanitizeImageTraceValue(item, depth+1)
			count++
		}
		return out
	default:
		return typed
	}
}

func truncateTraceString(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "...<truncated>"
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func isRawTraceKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "prepare_raw", "reference_uploads_raw", "sse_raw_events", "sse_raw_json", "poll_conversation_raw", "download_raw":
		return true
	default:
		return false
	}
}
