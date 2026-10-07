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

func TestRecognitionSavedLogsPersistedIdentity(t *testing.T) {
	a, _ := newTestAppDB(t, []storage.Movie{{Filename: "verified.mkv", TmdbID: 42, TitleEN: "Verified", MediaType: "movie", RecognitionSource: "tmdb"}})
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx := utils.EnsureTrace(context.Background())
	err := a.saveRecognitionBatch(ctx, []storage.Movie{
		{Filename: "verified.mkv", TmdbID: 0, NeedsReview: true, ReviewReason: "unresolved"},
		{Filename: "unknown.mkv", TmdbID: 0, NeedsReview: true, ReviewReason: "unresolved"},
		{Filename: "source:abc/show.mkv", TmdbID: 84, MediaType: "tv", RecognitionSource: "groq", NeedsReview: true, ReviewReason: "low_verification_score", VerificationScore: 0.9},
	}, "gemini_queue")
	if err != nil {
		t.Fatal(err)
	}
	events := make(map[string]map[string]any)
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "recognition_saved" {
			if event["trace_id"] == nil || event["stage"] != "gemini_queue" {
				t.Fatalf("missing operation context: %v", event)
			}
			events[event["file"].(string)] = event
		}
	}
	if len(events) != 3 {
		t.Fatalf("expected three saved identities: %s", output.String())
	}
	verified := events["verified.mkv"]
	if verified["tmdb_id"] != float64(42) || verified["resolved"] != true || verified["recognition_source"] != "tmdb" {
		t.Fatalf("logged submitted placeholder rather than preserved identity: %v", verified)
	}
	unknown := events["unknown.mkv"]
	if unknown["tmdb_id"] != float64(0) || unknown["resolved"] != false || unknown["needs_review"] != true {
		t.Fatalf("incorrect unresolved state: %v", unknown)
	}
	show := events["source:abc/show.mkv"]
	if show["tmdb_id"] != float64(84) || show["media_type"] != "tv" || show["recognition_source"] != "groq" || show["review_reason"] != "low_verification_score" {
		t.Fatalf("incorrect saved identity: %v", show)
	}
}

func TestRecognitionSaveFailureDoesNotLogSuccess(t *testing.T) {
	a, _ := newTestAppDB(t, nil)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.saveRecognitionBatch(ctx, []storage.Movie{{Filename: "cancelled.mkv", TmdbID: 42}}, "tmdb_scan"); err == nil {
		t.Fatal("cancelled persistence must fail")
	}
	if strings.Contains(output.String(), "recognition_saved") || strings.Contains(output.String(), "batch_save_success") {
		t.Fatalf("failed persistence reported as successful: %s", output.String())
	}
}
