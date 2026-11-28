package main

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"movielist-app/internal/ai"
	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func TestSearchTMDBCandidatesUsesFilenameInsteadOfWrongStoredTitle(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") != "The Bureau" {
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
	if err := db.SaveMovie(ctx, storage.Movie{Filename: "The.Bureau.2015.mkv", TmdbID: 802663, TitleEN: "The Bureau", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TMDBAPIKey: "test", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app := NewApp()
	app.ctx = ctx
	app.db = db
	app.tmdbClient = client
	got, err := app.SearchTMDBCandidates(CandidateSearchRequest{Filename: "The.Bureau.2015.mkv", MediaType: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TMDBID != 62476 {
		t.Fatalf("candidates=%+v", got)
	}
}

func TestSearchTMDBCandidatesIgnoresHistoricalAIResolution(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	queries := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query().Get("query")
		mu.Lock()
		queries = append(queries, query)
		mu.Unlock()
		switch query {
		case "AI Only Alias":
			// Кандидат, який повертає лише історична L2-назва. R6: її не має бути у запитах.
			io.WriteString(w, `{"results":[{"id":777777,"title":"AI Only Alias","original_title":"AI Only Alias","release_date":"2020-01-01"}]}`)
		case "Le Bureau des légendes":
			if r.URL.Path == "/3/search/movie" {
				io.WriteString(w, `{"results":[]}`)
				return
			}
			io.WriteString(w, `{"results":[{"id":62476,"name":"Le Bureau des légendes","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27"},{"id":89844,"name":"The Bureau","original_name":"The Bureau","first_air_date":"2015-01-01"}]}`)
		default:
			io.WriteString(w, `{"results":[]}`)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	dbDir := t.TempDir()
	db, err := storage.New(filepath.Join(dbDir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	filename := "Bjuro legend.HDTVRip.GeneralFilm/Bjuro legend.08.HDTVRip.GeneralFilm.avi"
	if err := db.SaveMoviesBatch(ctx, []storage.Movie{{Filename: filename, TmdbID: 62476, TitleEN: "Le Bureau des légendes", TitleUA: "Бюро легенд", Year: "2015", MediaType: "tv"}}); err != nil {
		t.Fatal(err)
	}
	// Історичний запис ai_resolutions у старій БД: редактор R6 його не читає.
	raw, err := sql.Open("sqlite", filepath.Join(dbDir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.ExecContext(ctx, `INSERT INTO ai_resolutions (original_filename, resolved_title, year, media_type, confidence, pipeline_version, provider, model, updated_at) VALUES (?, 'AI Only Alias', 2020, 'movie', 0.99, 25, 'gemini', 'gemini-2.5-flash', '2026-01-01T00:00:00Z')`, filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TMDBAPIKey: "test", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app := NewApp()
	app.ctx, app.db, app.tmdbClient = ctx, db, client
	got, err := app.SearchTMDBCandidates(CandidateSearchRequest{Filename: filename, MediaType: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	// Поточний запис відфільтровано; лишається лише 89844 зі stored_title-запиту.
	if len(got) != 1 || got[0].TMDBID != 89844 {
		t.Fatalf("candidates=%+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range queries {
		if q == "AI Only Alias" {
			t.Fatalf("historical L2 title used as candidate query: %v", queries)
		}
	}
	if len(queries) == 0 || queries[0] != "Le Bureau des légendes" {
		t.Fatalf("queries=%v", queries)
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

func TestExcludeCurrentCandidate(t *testing.T) {
	candidates := []tmdb.TMDBCandidate{
		{TMDBID: 1002109, MediaType: tmdb.MediaTypeMovie},
		{TMDBID: 42, MediaType: tmdb.MediaTypeMovie},
		{TMDBID: 1002109, MediaType: tmdb.MediaTypeTV},
	}
	got := excludeCurrentCandidate(candidates, &storage.Movie{TmdbID: 1002109, MediaType: "movie"})
	if len(got) != 2 || got[0].TMDBID != 42 || got[1].MediaType != tmdb.MediaTypeTV {
		t.Fatalf("filtered candidates=%+v", got)
	}
}

func TestCurrentNeedsReviewCandidateCanBeConfirmed(t *testing.T) {
	current := &storage.Movie{TmdbID: 62476, TitleUA: "Бюро легенд", TitleEN: "Le Bureau des légendes", Year: "2015", MediaType: "tv", NeedsReview: true}
	got := currentReviewCandidate(current)
	if len(got) != 1 || got[0].TMDBID != 62476 || got[0].MediaType != tmdb.MediaTypeTV || got[0].Title != "Бюро легенд" {
		t.Fatalf("current review candidate=%+v", got)
	}
	current.NeedsReview = false
	if got := currentReviewCandidate(current); len(got) != 0 {
		t.Fatalf("trusted current candidate leaked into alternatives: %+v", got)
	}
}

func TestConfirmCandidateKeepsExistingUkrainianPlot(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":586353,"title":"The Master and Margarita","original_title":"Мастер и Маргарита","release_date":"2024-01-25","overview":"Москва, 1930-е годы. Драматурга обвиняют в антисоветчине."}`)
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
	filename := "Master.i.Margarita.2023.WEB-DLRip.AVC.mkv"
	wantTitle := "Майстер і Маргарита"
	wantPlot := "Москва, 1930-ті роки. Драматурга звинувачують в антирадянщині."
	if err := db.SaveMoviesBatch(ctx, []storage.Movie{{Filename: filename, TmdbID: 586353, MediaType: "movie", TitleUA: wantTitle, Plot: wantPlot}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TMDBAPIKey: "test", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	defer client.Close()
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app := NewApp()
	app.ctx, app.db, app.cfg, app.tmdbClient = ctx, db, cfg, client
	if err := app.ConfirmTMDBCandidate(CandidateConfirmRequest{Filename: filename, TMDBID: 586353, MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	movie, err := db.GetMovieByFilename(ctx, filename)
	if err != nil || movie == nil || movie.TitleUA != wantTitle || movie.Plot != wantPlot {
		t.Fatalf("Ukrainian localization was lost: movie=%+v err=%v", movie, err)
	}
}

func TestConfirmCandidateReturnsBeforeBackgroundTranslation(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":1002109,"title":"Суперкопы 80","original_title":"Police Flash 80","release_date":"2026-03-18","overview":"Русский текст с буквой ы","genres":[],"credits":{"cast":[]}}`)
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
	filename := "Superkopy.80.2026.mkv"
	if err := db.SaveMoviesBatch(ctx, []storage.Movie{{Filename: filename, NeedsReview: true}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TMDBAPIKey: "test", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	defer client.Close()
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app := NewApp()
	app.ctx, app.db, app.cfg, app.tmdbClient = ctx, db, cfg, client
	app.aiClient = ai.NewClient(cfg)
	started, release := make(chan struct{}), make(chan struct{})
	app.candidateTranslationRunner = func(context.Context, []string, map[string]int) {
		close(started)
		<-release
	}
	updated := make(chan struct{}, 1)
	app.eventEmitter = func(_ context.Context, name string, _ ...interface{}) {
		if name == "movie-updated" {
			updated <- struct{}{}
		}
	}
	returned := make(chan error, 1)
	go func() {
		returned <- app.ConfirmTMDBCandidate(CandidateConfirmRequest{Filename: filename, TMDBID: 1002109, MediaType: "movie"})
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirmation waited for background translation")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background translation did not start")
	}
	movie, err := db.GetMovieByFilename(ctx, filename)
	if err != nil || movie == nil || movie.TmdbID != 1002109 {
		t.Fatalf("foreground selection not persisted: movie=%+v err=%v", movie, err)
	}
	close(release)
	app.wg.Wait()
	select {
	case <-updated:
	default:
		t.Fatal("movie-updated event not emitted")
	}
}

func TestBackgroundTranslationRejectsStaleCandidate(t *testing.T) {
	movies := map[string]storage.Movie{"Film.mkv": {Filename: "Film.mkv", TmdbID: 22, MediaType: "tv"}}
	if _, ok := translationTargetCurrent(movies, "Film.mkv", 11, "tv"); ok {
		t.Fatal("translation for replaced TMDB candidate was accepted")
	}
	if _, ok := translationTargetCurrent(movies, "Film.mkv", 22, "movie"); ok {
		t.Fatal("translation for a different media type with the same numeric ID was accepted")
	}
	if movie, ok := translationTargetCurrent(movies, "Film.mkv", 22, "tv"); !ok || movie.TmdbID != 22 {
		t.Fatalf("current translation target rejected: movie=%+v ok=%v", movie, ok)
	}
}

func TestExplicitMediaTypeWithoutHintUsesDirectFixPath(t *testing.T) {
	if !fixRequestUsesDirectPath(FixRequest{Filename: "Unknown.mkv", MediaType: "tv"}, nil) {
		t.Fatal("explicit TV type without a hint was routed to the untyped Gemini batch")
	}
	if fixRequestUsesDirectPath(FixRequest{Filename: "Unknown.mkv", MediaType: "auto"}, nil) {
		t.Fatal("auto request without a hint should retain the batch fallback")
	}
}

func TestMovieFromTMDBPreservesAmbiguousReviewState(t *testing.T) {
	movie := movieFromTMDB("Ambiguous.2024.mkv", &tmdb.MovieInfo{TMDBID: 42, MediaType: tmdb.MediaTypeMovie, AmbiguousExact: true})
	if !movie.NeedsReview || movie.ReviewReason != "ambiguous_exact" || movie.RecognitionSource != "tmdb" {
		t.Fatalf("movie=%+v", movie)
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

func TestCandidateSearchTitleRejectsFullAndBasenamePlaceholders(t *testing.T) {
	filename := "Bjuro legend.HDTVRip.GeneralFilm/Bjuro legend.08.HDTVRip.GeneralFilm.avi"
	for _, title := range []string{filename, filepath.Base(filename), "BJuro_LEGEND.08.HDTVRip.GeneralFilm.AVI"} {
		if got := candidateSearchTitle(title, filename); got != "" {
			t.Fatalf("placeholder %q used as query: %q", title, got)
		}
	}
}

func TestCandidateSearchQueriesAreCleanAndDeterministic(t *testing.T) {
	filename := "Bjuro legend.HDTVRip.GeneralFilm/Bjuro legend.08.HDTVRip.GeneralFilm.avi"
	queries := candidateSearchQueries(filepath.Base(filename), filename, nil)
	if len(queries) == 0 || queries[0].source != "filename" || queries[0].title != "Bjuro legend" {
		t.Fatalf("queries=%+v", queries)
	}
	for _, query := range queries {
		lower := strings.ToLower(query.title)
		for _, forbidden := range []string{"08", "hdtvrip", "generalfilm", "avi"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("dirty query %+v contains %q", query, forbidden)
			}
		}
	}
}

func TestCandidateSearchQueriesPreferManualThenStoredTitle(t *testing.T) {
	current := &storage.Movie{TmdbID: 62476, TitleEN: "The Bureau", TitleUA: "Бюро легенд", Year: "2015", MediaType: "tv"}
	queries := candidateSearchQueries("Le Bureau des légendes", "Bjuro legend.08.avi", current)
	want := []candidateQuery{{"manual", "Le Bureau des légendes"}, {"stored_title", "The Bureau"}, {"stored_title", "Бюро легенд"}, {"filename", "Bjuro legend"}}
	if len(queries) != len(want) {
		t.Fatalf("queries=%+v", queries)
	}
	for i := range want {
		if queries[i] != want[i] {
			t.Fatalf("queries[%d]=%+v want %+v", i, queries[i], want[i])
		}
	}
	if year := candidateSearchYear(0, current); year != 2015 {
		t.Fatalf("candidate year=%d", year)
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
