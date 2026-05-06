package register

import (
	"context"
	"strings"
)

type logContextKey struct{}

type LogContext struct {
	Thread int
	JobID  string
}

func WithThread(ctx context.Context, thread int) context.Context {
	if thread <= 0 {
		return ctx
	}
	meta := LogContextFromContext(ctx)
	meta.Thread = thread
	return context.WithValue(ctx, logContextKey{}, meta)
}

func WithJobID(ctx context.Context, jobID string) context.Context {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return ctx
	}
	meta := LogContextFromContext(ctx)
	meta.JobID = jobID
	return context.WithValue(ctx, logContextKey{}, meta)
}

func ThreadFromContext(ctx context.Context) int {
	return LogContextFromContext(ctx).Thread
}

func JobIDFromContext(ctx context.Context) string {
	return strings.TrimSpace(LogContextFromContext(ctx).JobID)
}

func LogContextFromContext(ctx context.Context) LogContext {
	if ctx == nil {
		return LogContext{}
	}
	meta, _ := ctx.Value(logContextKey{}).(LogContext)
	return meta
}
