package api

import (
	"context"
)

type structuredLogWriter func(ctx context.Context, logType string, summary string, detail map[string]any)

type structuredLogContextKey struct{}

type structuredLogContext struct {
	writer  structuredLogWriter
	logType string
	base    map[string]any
}

func withStructuredLog(ctx context.Context, writer structuredLogWriter, logType string, base map[string]any) context.Context {
	if writer == nil {
		return ctx
	}
	return context.WithValue(ctx, structuredLogContextKey{}, structuredLogContext{
		writer:  writer,
		logType: logType,
		base:    cloneLogDetail(base),
	})
}

func emitStructuredLog(ctx context.Context, summary string, detail map[string]any) {
	value := ctx.Value(structuredLogContextKey{})
	logCtx, ok := value.(structuredLogContext)
	if !ok || logCtx.writer == nil {
		return
	}
	payload := cloneLogDetail(logCtx.base)
	for key, value := range detail {
		payload[key] = value
	}
	logCtx.writer(ctx, logCtx.logType, summary, payload)
}

func appendStructuredLogAttempt(ctx context.Context, detail map[string]any) {
	if len(detail) == 0 {
		return
	}
	value := ctx.Value(structuredLogContextKey{})
	logCtx, ok := value.(structuredLogContext)
	if !ok {
		return
	}
	raw, ok := logCtx.base["attempts"]
	var attempts []map[string]any
	if ok {
		if typed, ok := raw.([]map[string]any); ok {
			attempts = append(attempts, typed...)
		} else if typed, ok := raw.([]any); ok {
			for _, item := range typed {
				if mapped, ok := item.(map[string]any); ok {
					attempts = append(attempts, mapped)
				}
			}
		}
	}
	attempts = append(attempts, cloneLogDetail(detail))
	logCtx.base["attempts"] = attempts
}

func identityLogFields(identity Identity) map[string]any {
	return map[string]any{
		"subject_id": identity.ID,
		"key_id":     identity.KeyID,
		"name":       identity.Name,
		"role":       identity.Role,
		"auth_type":  identity.AuthType,
	}
}

func logAttempts(ctx context.Context) []map[string]any {
	value := ctx.Value(structuredLogContextKey{})
	logCtx, ok := value.(structuredLogContext)
	if !ok {
		return nil
	}
	raw, ok := logCtx.base["attempts"]
	if !ok {
		return nil
	}
	if typed, ok := raw.([]map[string]any); ok {
		return typed
	}
	if typed, ok := raw.([]any); ok {
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	}
	return nil
}

func cloneLogDetail(detail map[string]any) map[string]any {
	if len(detail) == 0 {
		return map[string]any{}
	}
	next := make(map[string]any, len(detail))
	for key, value := range detail {
		next[key] = value
	}
	return next
}
