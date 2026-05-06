package api

import "context"

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
