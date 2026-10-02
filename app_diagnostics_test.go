package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func captureDiagnostics(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestStartupLogsVersion(t *testing.T) {
	output := captureDiagnostics(t)
	a := NewApp()
	a.cfg = &config.Config{AppVersion: "2.8.1", DBPath: filepath.Join(t.TempDir(), "movies.db"), PostersDir: t.TempDir()}
	a.startup(context.Background())
	defer a.db.Close()
	defer a.tmdbClient.Close()
	var event map[string]any
	if err := json.Unmarshal(bytes.Split(output.Bytes(), []byte("\n"))[0], &event); err != nil {
		t.Fatal(err)
	}
	if event["msg"] != "app_started" || event["version"] != "2.8.1" {
		t.Fatal(event)
	}
}

func TestMetadataRepairLogDescribesChangesAndPreservedIdentity(t *testing.T) {
	output := captureDiagnostics(t)
	before := storage.Movie{Filename: "film.mkv", TmdbID: 42, MediaType: "movie", TitleUA: "Український фільм", Plot: "Це український опис.", VerificationScore: .9, NeedsReview: true, ReviewReason: "test"}
	a := repairTestApp(t, before)
	a.repairDetailsFetcher = func(context.Context, tmdb.MediaType, int) (*tmdb.MovieInfo, error) {
		return &tmdb.MovieInfo{TMDBID: 42, MediaType: tmdb.MediaTypeMovie, TitleUA: before.TitleUA, Plot: "Новий український опис."}, nil
	}
	if _, err := a.RepairMetadata(before.Filename); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "metadata_repaired" {
			break
		}
	}
	for _, key := range []string{"tmdb_id", "media_type", "verification_score", "needs_review", "review_reason"} {
		if event["before_"+key] == nil || event["before_"+key] != event["after_"+key] {
			t.Fatalf("identity/state changed: %s: %v", key, event)
		}
	}
	if event["title_source"] != "tmdb" || event["plot_source"] != "tmdb" || event["duration_ms"] == nil || !reflect.DeepEqual(event["changed_fields"], []any{"plot"}) {
		t.Fatal(event)
	}
}
