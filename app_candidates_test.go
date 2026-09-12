package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func TestSearchTMDBCandidatesUsesFilenameInsteadOfWrongStoredTitle(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") != "bureau" {
			t.Errorf("query=%q", r.URL.Query().Get("query"))
		}
		if r.URL.Path == "/3/search/tv" {
			io.WriteString(w, `{"results":[{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27","popularity":35}]}`)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	db, err := storage.New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMovie(ctx, storage.Movie{Filename: "bureau.mkv", TmdbID: 802663, TitleEN: "The Bureau", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TMDBAPIKey: "test", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app := NewApp()
	app.ctx = ctx
	app.db = db
	app.tmdbClient = client
	got, err := app.SearchTMDBCandidates(CandidateSearchRequest{Filename: "bureau.mkv", MediaType: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TMDBID != 62476 {
		t.Fatalf("candidates=%+v", got)
	}
}

func TestDuplicateMovieIdentitiesNeedReview(t *testing.T) {
	movies := []storage.Movie{
		{Filename: "Ebigejl.2024.mkv", TmdbID: 100, MediaType: "movie", TitleUA: "Улюблені фільми"},
		{Filename: "Completely.Different.2020.mkv", TmdbID: 100, MediaType: "movie", TitleUA: "Улюблені фільми", RecognitionSource: "gemini"},
		{Filename: "Show.S01E01.mkv", TmdbID: 200, MediaType: "tv"},
		{Filename: "Show.S01E02.mkv", TmdbID: 200, MediaType: "tv"},
		{Filename: "Manual.Copy.mkv", TmdbID: 100, MediaType: "movie", RecognitionSource: "manual_id"},
	}
	patches := duplicateMovieIdentitiesForReview(movies)
	if len(patches) != 2 {
		t.Fatalf("patches=%+v", patches)
	}
	for _, patch := range patches {
		if !patch.NeedsReview || patch.ReviewReason != "duplicate_tmdb_id" {
			t.Fatalf("patch=%+v", patch)
		}
	}
}

func TestCandidateSearchTitleIgnoresUnresolvedPlaceholder(t *testing.T) {
	filename := "Daniels.Gotta.Die.2022.Dub.WEB-DLRip/Daniels.Gotta.Die.2022.Dub.WEB-DLRip.avi"
	if got := candidateSearchTitle("Unresolved: "+filename, filename); got != "" {
		t.Fatalf("technical placeholder used as query: %q", got)
	}
	if got := candidateSearchTitle(filename, filename); got != "" {
		t.Fatalf("filename placeholder used as query: %q", got)
	}
	parsed := tmdb.ParseFilename(filename)
	if parsed.CleanTitle != "Daniels Gotta Die" {
		t.Fatalf("parsed fallback=%q", parsed.CleanTitle)
	}
}

func TestBackfillMissingRatingsPreservesReviewAndDoesNotRepeat(t *testing.T) {
	ctx := context.Background()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":157547,"title":"Oculus","original_title":"Oculus","vote_average":6.5,"vote_count":3100}`)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	db, err := storage.New(filepath.Join(t.TempDir(), "ratings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMovie(ctx, storage.Movie{Filename: "Oculus.mkv", TmdbID: 157547, MediaType: "movie", NeedsReview: true, ReviewReason: "ambiguous_exact"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TMDBAPIKey: "test", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app := NewApp()
	app.ctx, app.db, app.tmdbClient = ctx, db, client
	if err := app.backfillMissingRatings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.backfillMissingRatings(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMovieByFilename(ctx, "Oculus.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got.VoteAverage != 6.5 || got.VoteCount != 3100 || !got.NeedsReview || got.ReviewReason != "ambiguous_exact" {
		t.Fatalf("backfilled movie=%+v", got)
	}
	if requests != 1 {
		t.Fatalf("requests=%d, want 1", requests)
	}
}
