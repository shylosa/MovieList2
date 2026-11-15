package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLocalizationCompletionPersistsAndInvalidatesChangedSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "movies.db")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	base := Movie{Filename: "original.mkv", TmdbID: 1, MediaType: "movie", TitleUA: "Original", TitleEN: "Original"}
	if err := db.SaveMoviesBatch(ctx, []Movie{base}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkLocalizationCompleted(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Reopening and idempotent schema initialization preserve completion.
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		n := 0
		if err := db.ForEachRecognizedLocalization(ctx, func(string, string, string) error { n++; return nil }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("completed original title retried after restart")
	}
	for _, field := range []string{"title", "original", "plot", "identity", "media_type", "rating"} {
		t.Run(field, func(t *testing.T) {
			if err := db.SaveMoviesBatch(ctx, []Movie{base}); err != nil {
				t.Fatal(err)
			}
			if err := db.MarkLocalizationCompleted(ctx, base); err != nil {
				t.Fatal(err)
			}
			changed := base
			switch field {
			case "title":
				changed.TitleUA = "Changed"
			case "original":
				changed.TitleEN = "Changed"
			case "plot":
				changed.Plot = "New plot"
			case "identity":
				changed.TmdbID = 2
			case "media_type":
				changed.MediaType = "tv"
			case "rating":
				changed.VoteAverage = 8
			}
			if err := db.SaveMoviesBatch(ctx, []Movie{changed}); err != nil {
				t.Fatal(err)
			}
			// A late response for the old snapshot cannot suppress changed text.
			if err := db.MarkLocalizationCompleted(ctx, base); err != nil {
				t.Fatal(err)
			}
			want := 1
			if field == "rating" {
				want = 0
			}
			if got := count(); got != want {
				t.Fatalf("candidates = %d; want %d", got, want)
			}
			// Reset without the non-empty merge semantics of SaveMoviesBatch.
			if _, err := db.db.ExecContext(ctx, `DELETE FROM movies`); err != nil {
				t.Fatal(err)
			}
		})
	}
}
