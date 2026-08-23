// Package observability provides structured logging and, in later phases, metrics
// and tracing. Logs are written to stderr as required by twelve-factor: the process
// is a stream producer and the environment routes the stream.
package observability

import (
	"context"
	"log/slog"
	"os"
)

// NewLogger builds a slog.Logger for the given level ("debug", "info", "warn",
// "error") and format ("json" or "text"). Unknown values fall back to info/JSON,
// since configuration is already validated upstream and this must never panic.
func NewLogger(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type correlationKey struct{}

// CorrelationIDField is the structured log attribute key for the correlation id.
const CorrelationIDField = "correlation_id"

// WithCorrelationID returns a context carrying id, so that a request can be traced
// across components once the request path exists in later phases.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID extracts a correlation id from ctx, or "" if none is set.
func CorrelationID(ctx context.Context) string {
	if v, ok := ctx.Value(correlationKey{}).(string); ok {
		return v
	}
	return ""
}

// LoggerFrom returns base annotated with the context's correlation id when present,
// giving every log line on a request path a shared identifier.
func LoggerFrom(ctx context.Context, base *slog.Logger) *slog.Logger {
	if id := CorrelationID(ctx); id != "" {
		return base.With(CorrelationIDField, id)
	}
	return base
}
