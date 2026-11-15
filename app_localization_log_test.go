package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"movielist-app/internal/storage"
	"movielist-app/internal/utils"
)

func TestLocalizationSnapshotLogsStoredTitleOnlyAtDebug(t *testing.T) {
	a, _ := newTestAppDB(t, []storage.Movie{{Filename: "fish.mkv", TmdbID: 1757519, MediaType: "movie", TitleUA: "Здоровий як риба"}})
	var output bytes.Buffer
	previous := slog.Default()
	var level slog.LevelVar
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: &level})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx := utils.EnsureTrace(context.Background())
	// Submitted data may differ from the catalog after merge or another update.
	submitted := []storage.Movie{{Filename: "fish.mkv", TmdbID: 0, TitleUA: "not the persisted title"}}
	a.logLocalizationSnapshot(ctx, submitted)
	if output.Len() != 0 {
		t.Fatalf("localization diagnostics leaked at INFO: %s", output.String())
	}
	level.Set(slog.LevelDebug)
	a.logLocalizationSnapshot(ctx, submitted)
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event["msg"] != "localization_saved" || event["title_ua"] != "Здоровий як риба" || event["tmdb_id"] != float64(1757519) || event["title_ukrainian"] != true || event["trace_id"] == nil {
		t.Fatalf("incorrect persisted localization diagnostic: %v", event)
	}
	if strings.Contains(output.String(), "not the persisted title") {
		t.Fatal("submitted title leaked instead of persisted state")
	}
}
