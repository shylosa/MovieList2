package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCleanMissingMoviesKeepsRelativePathKeys(t *testing.T) {
	ctx := context.Background()

	db, err := New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer db.Close()

	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	if err := db.SaveMoviesBatch(ctx, []Movie{
		{Filename: "Series/episode01.mkv", TmdbID: 101, TitleEN: "Pilot"},
		{Filename: "deleted.mkv", TmdbID: 202, TitleEN: "Deleted"},
	}); err != nil {
		t.Fatalf("SaveMoviesBatch() error = %v", err)
	}

	deleted, err := db.CleanMissingMovies(ctx, []string{"Series/episode01.mkv"})
	if err != nil {
		t.Fatalf("CleanMissingMovies() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("CleanMissingMovies() deleted = %d, want 1", deleted)
	}

	kept, err := db.GetMovieByFilename(ctx, "Series/episode01.mkv")
	if err != nil {
		t.Fatalf("GetMovieByFilename(relative) error = %v", err)
	}
	if kept == nil || kept.TmdbID != 101 {
		t.Fatalf("relative-path movie was not preserved: %+v", kept)
	}

	removed, err := db.GetMovieByFilename(ctx, "deleted.mkv")
	if err != nil {
		t.Fatalf("GetMovieByFilename(deleted) error = %v", err)
	}
	if removed != nil {
		t.Fatalf("stale movie was not deleted: %+v", removed)
	}
}

func TestSaveMoviesBatchUnresolvedDoesNotDowngradeRecognized(t *testing.T) {
	ctx := context.Background()

	db, err := New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer db.Close()

	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	if err := db.SaveMoviesBatch(ctx, []Movie{
		{Filename: "Series/episode01.mkv", TmdbID: 101, TitleUA: "Pilot UA", TitleEN: "Pilot"},
	}); err != nil {
		t.Fatalf("initial SaveMoviesBatch() error = %v", err)
	}

	if err := db.SaveMoviesBatch(ctx, []Movie{
		{Filename: "Series/episode01.mkv", TmdbID: 0, TitleUA: "Series/episode01.mkv", TitleEN: "Unresolved: Series/episode01.mkv"},
	}); err != nil {
		t.Fatalf("unresolved SaveMoviesBatch() error = %v", err)
	}

	kept, err := db.GetMovieByFilename(ctx, "Series/episode01.mkv")
	if err != nil {
		t.Fatalf("GetMovieByFilename() error = %v", err)
	}
	if kept == nil {
		t.Fatal("movie missing after unresolved upsert")
	}
	if kept.TmdbID != 101 {
		t.Fatalf("TmdbID = %d, want 101", kept.TmdbID)
	}
	if kept.TitleUA != "Pilot UA" {
		t.Fatalf("TitleUA = %q, want %q", kept.TitleUA, "Pilot UA")
	}
	if kept.TitleEN != "Pilot" {
		t.Fatalf("TitleEN = %q, want %q", kept.TitleEN, "Pilot")
	}
}
