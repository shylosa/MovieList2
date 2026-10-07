package utils

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestConfiguredLogLevel(t *testing.T) {
	for _, tt := range []struct {
		value string
		level slog.Level
		valid bool
	}{
		{"", slog.LevelInfo, true}, {"info", slog.LevelInfo, true},
		{" DEBUG ", slog.LevelDebug, true}, {"warn", slog.LevelWarn, true},
		{"error", slog.LevelError, true}, {"unknown-secret-value", slog.LevelInfo, false},
	} {
		level, valid := configuredLogLevel(tt.value)
		if level != tt.level || valid != tt.valid {
			t.Fatalf("configuredLogLevel(%q) = %v, %v", tt.value, level, valid)
		}
	}
}

func TestSetLogLevelUpdatesHandlerWithoutLoggingConfigValue(t *testing.T) {
	previous, previousLevel := slog.Default(), loggerLevel.Level()
	t.Cleanup(func() { slog.SetDefault(previous); loggerLevel.Set(previousLevel) })
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: &loggerLevel})))
	SetLogLevel("info")
	slog.Debug("hidden_diagnostic")
	SetLogLevel("debug")
	slog.Debug("visible_diagnostic")
	SetLogLevel("unknown-secret-value")
	slog.Debug("hidden_after_invalid")
	got := output.String()
	if !strings.Contains(got, "visible_diagnostic") || strings.Contains(got, "hidden_diagnostic") || strings.Contains(got, "hidden_after_invalid") || strings.Contains(got, "unknown-secret-value") {
		t.Fatalf("unexpected level filtering or config disclosure: %s", got)
	}
}
