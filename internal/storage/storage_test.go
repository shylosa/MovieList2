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

func TestPatchMovie_MergesNonEmptyFields(t *testing.T) {
	ctx := context.Background()

	db, err := New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer db.Close()

	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	// Insert base record.
	base := Movie{
		Filename: "films/Dune.mkv",
		TmdbID:   1234,
		TitleEN:  "Dune",
		TitleUA:  "Дюна",
		Year:     "2021",
		Plot:     "Original plot.",
	}
	if err := db.SaveMovie(ctx, base); err != nil {
		t.Fatalf("SaveMovie() error = %v", err)
	}

	// Patch: only TitleUA and Plot are non-empty — they must overwrite.
	// TmdbID=0 and TitleEN="" — they must be preserved from base.
	patch := Movie{
		Filename: "films/Dune.mkv",
		TitleUA:  "Дюна: Частина перша",
		Plot:     "Updated plot.",
	}
	if err := db.PatchMovie(ctx, patch); err != nil {
		t.Fatalf("PatchMovie() error = %v", err)
	}

	got, err := db.GetMovieByFilename(ctx, "films/Dune.mkv")
	if err != nil {
		t.Fatalf("GetMovieByFilename() error = %v", err)
	}
	if got == nil {
		t.Fatal("record missing after PatchMovie")
	}

	// Non-empty patch fields must overwrite.
	if got.TitleUA != "Дюна: Частина перша" {
		t.Errorf("TitleUA = %q, want %q", got.TitleUA, "Дюна: Частина перша")
	}
	if got.Plot != "Updated plot." {
		t.Errorf("Plot = %q, want %q", got.Plot, "Updated plot.")
	}

	// Empty patch fields must preserve base values.
	if got.TmdbID != 1234 {
		t.Errorf("TmdbID = %d, want 1234", got.TmdbID)
	}
	if got.TitleEN != "Dune" {
		t.Errorf("TitleEN = %q, want %q", got.TitleEN, "Dune")
	}
	if got.Year != "2021" {
		t.Errorf("Year = %q, want %q", got.Year, "2021")
	}
}

func TestPatchMovie_InsertsWhenMissing(t *testing.T) {
	ctx := context.Background()

	db, err := New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer db.Close()

	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	// PatchMovie on a non-existent filename must insert.
	patch := Movie{
		Filename: "films/NewMovie.mkv",
		TmdbID:   9999,
		TitleEN:  "New Movie",
		TitleUA:  "Новий фільм",
	}
	if err := db.PatchMovie(ctx, patch); err != nil {
		t.Fatalf("PatchMovie() on missing record error = %v", err)
	}

	got, err := db.GetMovieByFilename(ctx, "films/NewMovie.mkv")
	if err != nil {
		t.Fatalf("GetMovieByFilename() error = %v", err)
	}
	if got == nil {
		t.Fatal("record not inserted by PatchMovie")
	}
	if got.TmdbID != 9999 {
		t.Errorf("TmdbID = %d, want 9999", got.TmdbID)
	}
	if got.TitleUA != "Новий фільм" {
		t.Errorf("TitleUA = %q, want %q", got.TitleUA, "Новий фільм")
	}
}

func TestSetAndGetState(t *testing.T) {
	ctx := context.Background()

	db, err := New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer db.Close()

	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	if err := db.SetState(ctx, "last_scan_at", "2026-07-04 12:00"); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}
	got := db.GetState(ctx, "last_scan_at")
	if got != "2026-07-04 12:00" {
		t.Errorf("GetState = %q; want %q", got, "2026-07-04 12:00")
	}

	if err := db.SetState(ctx, "last_scan_at", "2026-07-04 15:30"); err != nil {
		t.Fatalf("SetState upsert failed: %v", err)
	}
	got = db.GetState(ctx, "last_scan_at")
	if got != "2026-07-04 15:30" {
		t.Errorf("GetState after upsert = %q; want %q", got, "2026-07-04 15:30")
	}

	got = db.GetState(ctx, "nonexistent_key")
	if got != "" {
		t.Errorf("GetState(nonexistent) = %q; want empty string", got)
	}
}
