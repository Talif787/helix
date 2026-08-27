package observability

import (
	"context"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo, // unknown falls back to info
		"bogus": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNewLoggerLevelWiring(t *testing.T) {
	ctx := context.Background()

	if !NewLogger("debug", "json").Enabled(ctx, slog.LevelDebug) {
		t.Error("debug logger should have debug enabled")
	}
	if NewLogger("warn", "json").Enabled(ctx, slog.LevelInfo) {
		t.Error("warn logger should not have info enabled")
	}

	// Unknown level and format must not panic and must default to info level.
	l := NewLogger("bogus", "bogus")
	if l == nil {
		t.Fatal("NewLogger returned nil")
	}
	if l.Enabled(ctx, slog.LevelDebug) {
		t.Error("default (info) logger should not have debug enabled")
	}
	if !l.Enabled(ctx, slog.LevelInfo) {
		t.Error("default (info) logger should have info enabled")
	}

	// The text format path should also build a usable logger.
	if NewLogger("info", "text") == nil {
		t.Fatal("text logger is nil")
	}
}

func TestCorrelationIDRoundTrip(t *testing.T) {
	if got := CorrelationID(context.Background()); got != "" {
		t.Errorf("empty context correlation id = %q, want empty", got)
	}
	ctx := WithCorrelationID(context.Background(), "req-123")
	if got := CorrelationID(ctx); got != "req-123" {
		t.Errorf("correlation id = %q, want req-123", got)
	}
}

func TestLoggerFrom(t *testing.T) {
	base := NewLogger("info", "json")

	// With no correlation id, the base logger is returned unchanged.
	if got := LoggerFrom(context.Background(), base); got != base {
		t.Error("LoggerFrom with no correlation id should return the base logger unchanged")
	}

	// With a correlation id, an annotated (non-nil) logger is returned.
	ctx := WithCorrelationID(context.Background(), "abc")
	if got := LoggerFrom(ctx, base); got == nil {
		t.Error("LoggerFrom with a correlation id should return a logger")
	}
}
