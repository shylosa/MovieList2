package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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

func TestRecognitionFieldsAndVersionedAICache(t *testing.T) {
	ctx := context.Background()
	db, err := New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	want := Movie{Filename: "The Bureau/01.mkv", TmdbID: 62476, MediaType: "tv", RecognitionSource: "gemini", RecognitionConfidence: .91, VerificationScore: .88, NeedsReview: true, ReviewReason: "ambiguous_exact", VoteAverage: 8.2, VoteCount: 450}
	if err := db.SaveMovie(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMovieByFilename(ctx, want.Filename)
	if err != nil || got == nil {
		t.Fatalf("lookup: %+v, %v", got, err)
	}
	if got.RecognitionSource != want.RecognitionSource || !got.NeedsReview || got.ReviewReason != want.ReviewReason || got.VoteAverage != want.VoteAverage || got.VoteCount != want.VoteCount {
		t.Fatalf("recognition fields lost: %+v", got)
	}

	updated := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	cache := AIResolution{OriginalFilename: want.Filename, ResolvedTitle: "The Bureau", MediaType: "tv", Confidence: .9, PipelineVersion: 25, Provider: "gemini", Model: "gemini-2.5-flash", UpdatedAt: updated}
	if err := db.SaveAIResolution(ctx, cache); err != nil {
		t.Fatal(err)
	}
	hit, stale, err := db.GetAIResolution(ctx, want.Filename, 25)
	if err != nil || stale || hit == nil || hit.Provider != "gemini" || !hit.UpdatedAt.Equal(updated) {
		t.Fatalf("current cache = %+v stale=%v err=%v", hit, stale, err)
	}
	hit, stale, err = db.GetAIResolution(ctx, want.Filename, 26)
	if err != nil || !stale || hit != nil {
		t.Fatalf("stale cache = %+v stale=%v err=%v", hit, stale, err)
	}
}

func TestInitSchemaUpgradesRound24Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE movies (filename TEXT PRIMARY KEY, tmdb_id INTEGER, title_ua TEXT, title_en TEXT, year TEXT, genres TEXT, cast TEXT, plot TEXT, poster_url TEXT, local_poster_path TEXT, media_type TEXT); CREATE TABLE ai_resolutions (original_filename TEXT PRIMARY KEY, resolved_title TEXT, year INTEGER, media_type TEXT, confidence REAL); INSERT INTO movies VALUES('old.mkv',7,'','Old','','','','','','','movie'); INSERT INTO ai_resolutions VALUES('old.mkv','Old',2003,'movie',0.9);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := db.GetMovieByFilename(ctx, "old.mkv")
	if err != nil || m == nil || m.TmdbID != 7 || m.TitleEN != "Old" {
		t.Fatalf("old movie lost: %+v %v", m, err)
	}
	hit, stale, err := db.GetAIResolution(ctx, "old.mkv", 25)
	if err != nil || hit != nil || !stale {
		t.Fatalf("old cache must be stale: %+v stale=%v err=%v", hit, stale, err)
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

func TestCleanOrphanPostersReportsCheckedAndPreservesDatabasePaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	posters := filepath.Join(dir, "posters")
	if err := os.Mkdir(posters, 0o755); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(posters, "valid.jpg")
	orphan := filepath.Join(posters, "orphan.jpg")
	if err := os.WriteFile(valid, []byte("valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMoviesBatch(ctx, []Movie{{Filename: "Valid.mkv", TmdbID: 1, LocalPosterPath: valid}}); err != nil {
		t.Fatal(err)
	}
	checked, deleted, err := db.CleanOrphanPosters(ctx, posters)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 2 || deleted != 1 {
		t.Fatalf("checked=%d deleted=%d", checked, deleted)
	}
	if _, err := os.Stat(valid); err != nil {
		t.Fatalf("valid poster removed: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan still exists: %v", err)
	}
}

func TestStaleAIResolutionCannotOverwriteNewPipeline(t *testing.T) {
	ctx := context.Background()
	db, err := New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	newer := AIResolution{OriginalFilename: "Film.mkv", ResolvedTitle: "Correct", PipelineVersion: 26, UpdatedAt: time.Now().UTC()}
	if err := db.SaveAIResolution(ctx, newer); err != nil {
		t.Fatal(err)
	}
	stale := AIResolution{OriginalFilename: "Film.mkv", ResolvedTitle: "Old wrong value", PipelineVersion: 25, UpdatedAt: newer.UpdatedAt.Add(time.Hour)}
	if err := db.SaveAIResolution(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, isStale, err := db.GetAIResolution(ctx, "Film.mkv", 26)
	if err != nil || isStale || got == nil || got.ResolvedTitle != "Correct" {
		t.Fatalf("new pipeline overwritten: got=%+v stale=%v err=%v", got, isStale, err)
	}
}

func TestCleanOrphanPostersHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	posters := filepath.Join(dir, "posters")
	if err := os.Mkdir(posters, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(posters, "orphan.jpg")
	if err := os.WriteFile(orphan, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	checked, deleted, err := db.CleanOrphanPosters(ctx, posters)
	if !errors.Is(err, context.Canceled) || checked != 0 || deleted != 0 {
		t.Fatalf("checked=%d deleted=%d err=%v", checked, deleted, err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("cancelled cleanup removed poster: %v", err)
	}
}
