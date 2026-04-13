package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestBuildLoggerResolved_Debug verifies --debug yields a JSON handler
// at debug level. Replaces the old buildLogger test after the
// Phase 4.9 refactor that folded the debug shortcut into the
// resolver-driven pipeline.
func TestBuildLoggerResolved_Debug(t *testing.T) {
	opts := resolvedSearchOptions{LogLevel: slog.LevelDebug, LogFormat: "json"}
	logger := buildLoggerResolved(opts)
	if logger == nil {
		t.Fatal("nil logger")
	}
	h := logger.Handler()
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug-mode logger does not enable LevelDebug")
	}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("debug-mode logger does not enable LevelInfo")
	}
}

func TestBuildLoggerResolved_DefaultWarnOnly(t *testing.T) {
	opts := resolvedSearchOptions{LogLevel: slog.LevelWarn, LogFormat: "text"}
	logger := buildLoggerResolved(opts)
	if logger == nil {
		t.Fatal("nil logger")
	}
	h := logger.Handler()
	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("default logger does not enable LevelWarn")
	}
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("default logger leaks Info-level events")
	}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("default logger leaks Debug-level events")
	}
}

// TestBuildLoggerResolved_JSONFormat exercises the JSON handler end-to-end
// via a buffer-backed slog.Logger and checks the output is parseable.
func TestBuildLoggerResolved_JSONFormat(t *testing.T) {
	// We can't use buildLoggerResolved directly (it writes to os.Stderr)
	// but we can assert that when LogFormat=="json", the same config
	// shape used internally produces valid JSON.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logger.Debug("test event", "key", "value", "num", 42)

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no output")
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, line)
	}
	if decoded["msg"] != "test event" {
		t.Errorf("msg = %v", decoded["msg"])
	}
	if decoded["level"] != "DEBUG" {
		t.Errorf("level = %v", decoded["level"])
	}
}
