package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

type mockTransport struct {
	base   http.RoundTripper
	scheme string
	host   string
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = m.scheme
	newReq.URL.Host = m.host
	return m.base.RoundTrip(newReq)
}

func TestUpdateMovie_BypassesGeminiOnCyrillicTMDB(t *testing.T) {
	ctx := context.Background()

	// 1. Create a mock HTTP server to represent TMDB
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id": 12345,
			"title": "Зміна долі",
			"original_title": "The Change-Up",
			"release_date": "2011-08-05",
			"overview": "Два друга міняються тілами."
		}`)
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// safe to ignore: httptest.Server always provides a valid URL.
	u, _ := url.Parse(srv.URL)

	// 2. Set up App config, storage, and tmdb client
	tempDB := filepath.Join(t.TempDir(), "movies.db")
	db, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	app := NewApp()
	app.db = db
	app.cfg = &config.Config{
		DBPath:     tempDB,
		TMDBAPIKey: "fake-key",
		PostersDir: t.TempDir(),
	}
	app.tmdbClient = tmdb.NewClient(app.cfg)
	app.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})

	// 3. Call updateMovie with a TMDB ID hint (no Wails ctx — avoids EventsEmit in tests).
	// Since we mocked TMDB to return a Ukrainian title ("Зміна долі"),
	// it must save the movie and return nil without calling Gemini.
	err = app.updateMovie(ctx, "the-change-up.mkv", "12345")
	if err != nil {
		t.Fatalf("UpdateMovie failed: %v", err)
	}

	m, err := db.GetMovieByFilename(ctx, "the-change-up.mkv")
	if err != nil {
		t.Fatalf("GetMovieByFilename failed: %v", err)
	}
	if m == nil {
		t.Fatalf("movie was not saved")
	}

	if m.TmdbID != 12345 {
		t.Errorf("expected TmdbID 12345, got %d", m.TmdbID)
	}
	if m.TitleUA != "Зміна долі" {
		t.Errorf("expected TitleUA 'Зміна долі', got '%s'", m.TitleUA)
	}
	if m.Plot != "Два друга міняються тілами." {
		t.Errorf("expected Plot 'Два друга міняються тілами.', got '%s'", m.Plot)
	}
}

func TestUpdateMovieAuthoritativeManualTitleNeverCallsGemini(t *testing.T) {
	ctx := context.Background()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/movie"):
			io.WriteString(w, `{"results":[{"id":181886,"title":"Enemy","original_title":"Enemy","release_date":"2014-03-14","popularity":25},{"id":103663,"title":"Enemy","original_title":"Shatru","release_date":"2013-08-23","popularity":2}]}`)
		case strings.HasSuffix(r.URL.Path, "/search/tv"):
			io.WriteString(w, `{"results":[]}`)
		case strings.HasSuffix(r.URL.Path, "/movie/181886"):
			io.WriteString(w, `{"id":181886,"title":"Enemy","original_title":"Enemy","release_date":"2014-03-14"}`)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(handler)
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
	app := NewApp()
	app.db = db
	app.cfg = &config.Config{TMDBAPIKey: "fake", PostersDir: t.TempDir()}
	app.tmdbClient = tmdb.NewClient(app.cfg)
	app.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app.aiClient = nil // A successful manual title must not need or call Gemini.
	if err := app.updateMovie(ctx, "Vrag.2013.mkv", "Enemy"); err != nil {
		t.Fatal(err)
	}
	movie, err := db.GetMovieByFilename(ctx, "Vrag.2013.mkv")
	if err != nil || movie == nil || movie.TmdbID != 181886 {
		t.Fatalf("saved movie=%+v err=%v; want TMDB 181886", movie, err)
	}
}

func TestMergeGeminiWithTMDBStrictTypeUsesOnlyRequestedEndpoint(t *testing.T) {
	ctx := context.Background()
	movieCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/movie"):
			movieCalled = true
			io.WriteString(w, `{"results":[{"id":802663,"title":"The Bureau","original_title":"The Bureau","release_date":"2020-01-01"}]}`)
		case strings.HasSuffix(r.URL.Path, "/search/tv"):
			io.WriteString(w, `{"results":[{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27","popularity":35}]}`)
		case strings.HasSuffix(r.URL.Path, "/tv/62476"):
			io.WriteString(w, `{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	cfg := &config.Config{TMDBAPIKey: "fake", PostersDir: t.TempDir(), MediaFolderPath: t.TempDir()}
	client := tmdb.NewClient(cfg)
	defer client.Close()
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	app := NewApp()
	app.ctx, app.cfg, app.tmdbClient = ctx, cfg, client
	app.eventEmitter = func(context.Context, string, ...interface{}) {}
	year := 2015
	got := app.mergeGeminiWithTMDBForType(ctx, filepath.Join(cfg.MediaFolderPath, "The.Bureau.2015.mkv"), ai.RecognizedTitle{ENTitle: "The Bureau", Year: &year, MediaType: "tv", Status: "resolved", Confidence: 1}, true)
	if movieCalled || got.TmdbID != 62476 || got.MediaType != "tv" {
		t.Fatalf("strict type lookup: movieCalled=%v movie=%+v", movieCalled, got)
	}
}

func TestSameMovieIdentityRequiresTypeAndID(t *testing.T) {
	existing := &storage.Movie{TmdbID: 22, MediaType: "movie"}
	if !sameMovieIdentity(existing, storage.Movie{TmdbID: 22, MediaType: "movie"}) {
		t.Fatal("same identity was not recognized")
	}
	if sameMovieIdentity(existing, storage.Movie{TmdbID: 22, MediaType: "tv"}) || sameMovieIdentity(existing, storage.Movie{TmdbID: 23, MediaType: "movie"}) {
		t.Fatal("different identity was treated as the same movie")
	}
}

func TestMergeGeminiWithTMDBAcceptsTVWhenGeminiSaysMovie(t *testing.T) {
	ctx := context.Background()
	year := 2025

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(r.URL.Path, "/search/movie"):
			io.WriteString(w, `{"results":[]}`)
		case strings.Contains(r.URL.Path, "/search/tv"):
			io.WriteString(w, `{
				"results": [{
					"id": 98765,
					"name": "Scarpetta",
					"original_name": "Scarpetta",
					"first_air_date": "2025-03-05",
					"media_type": "tv",
					"original_language": "en",
					"popularity": 42
				}]
			}`)
		case strings.Contains(r.URL.Path, "/tv/98765"):
			io.WriteString(w, `{
				"id": 98765,
				"name": "Scarpetta",
				"original_name": "Scarpetta",
				"first_air_date": "2025-03-05",
				"overview": "A medical examiner series.",
				"genres": [{"name": "Drama"}],
				"credits": {"cast": [{"name": "Nicole Kidman"}]}
			}`)
		default:
			t.Fatalf("unexpected TMDB request path: %s", r.URL.Path)
		}
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	app := NewApp()
	app.cfg = &config.Config{
		TMDBAPIKey: "fake-key",
		PostersDir: t.TempDir(),
	}
	app.tmdbClient = tmdb.NewClient(app.cfg)
	app.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})

	movie := app.mergeGeminiWithTMDB(ctx, filepath.Join("Скарпетта", "Скарпетта 8.WEB-DLRip.mkv"), ai.RecognizedTitle{
		ENTitle:    "Scarpetta",
		Year:       &year,
		MediaType:  "movie",
		Confidence: 0.95,
	})

	if movie.TmdbID != 98765 {
		t.Fatalf("expected TV result to be accepted, got tmdb_id=%d", movie.TmdbID)
	}
	if movie.MediaType != string(tmdb.MediaTypeTV) {
		t.Fatalf("expected media_type tv, got %q", movie.MediaType)
	}
	if movie.TitleEN != "Scarpetta" {
		t.Fatalf("expected title Scarpetta, got %q", movie.TitleEN)
	}
}

func TestMergeGeminiWithTMDBPrefersPopularExactTVForTheBureau(t *testing.T) {
	ctx := context.Background()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/movie"):
			io.WriteString(w, `{"results":[{"id":802663,"title":"The Bureau","original_title":"The Bureau","release_date":"2020-01-01","popularity":999}]}`)
		case strings.HasSuffix(r.URL.Path, "/search/tv"):
			io.WriteString(w, `{"results":[{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27","popularity":1}]}`)
		case strings.HasSuffix(r.URL.Path, "/tv/62476"):
			io.WriteString(w, `{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27"}`)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	u, _ := url.Parse(server.URL)
	root := t.TempDir()
	app := NewApp()
	app.cfg = &config.Config{MediaFolderPath: root, TMDBAPIKey: "fake", PostersDir: t.TempDir()}
	app.tmdbClient = tmdb.NewClient(app.cfg)
	app.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
	movie := app.mergeGeminiWithTMDB(ctx, filepath.Join(root, "Bjuro legend", "episode.08.avi"), ai.RecognizedTitle{
		ENTitle: "The Bureau", MediaType: "movie", Confidence: 0.9, Status: "resolved",
	})
	if movie.TmdbID != 62476 || movie.MediaType != "tv" {
		t.Fatalf("automatic exact merge = %+v; want TV 62476", movie)
	}
}

func TestMaxTitleSimilarity_CyrillicCandidate(t *testing.T) {
	info := &tmdb.MovieInfo{
		TitleEN: "Scarpetta",
		TitleUA: "Скарпетта",
	}

	// Кириличний кандидат — через TitleUA (до FIX-20 давав 0, тепер 1)
	if got := maxTitleSimilarity("Скарпетта", info); got < 0.99 {
		t.Errorf("maxTitleSimilarity('Скарпетта') = %.4f; want >= 0.99 (TitleUA match)", got)
	}
	// Latin кандидат — через TitleEN
	if got := maxTitleSimilarity("Scarpetta", info); got < 0.99 {
		t.Errorf("maxTitleSimilarity('Scarpetta') = %.4f; want >= 0.99 (TitleEN match)", got)
	}
	// Повна невідповідність
	if got := maxTitleSimilarity("Dune", info); got > 0.5 {
		t.Errorf("maxTitleSimilarity('Dune') = %.4f; want < 0.5", got)
	}
}

func TestMaxTitleSimilarity_MatchedAlias(t *testing.T) {
	info := &tmdb.MovieInfo{
		TitleEN:      "Scarpetta",
		TitleUA:      "Скарпетта",
		MatchedAlias: "Kay Scarpetta",
	}
	if got := maxTitleSimilarity("Kay Scarpetta", info); got < 0.90 {
		t.Errorf("maxTitleSimilarity('Kay Scarpetta') = %.4f; want >= 0.90 (MatchedAlias)", got)
	}
}

func TestLocalizedTitleCannotReplaceStrongGeminiVerification(t *testing.T) {
	info := &tmdb.MovieInfo{
		TitleEN:     "Shatru",
		TitleUA:     "Ворог",
		SearchTitle: "Enemy",
	}
	strong := tmdb.TitleSimilarity("Enemy", info.TitleEN)
	localized := maxLocalizedTitleSimilarity("Enemy", info)
	if strong >= geminiTMDBVerifyMinJW {
		t.Fatalf("strong similarity = %.3f; want rejection", strong)
	}
	if localized < 0.99 {
		t.Fatalf("fixture must prove localized false positive, got %.3f", localized)
	}
}

func TestGeminiTMDBYearCompatible(t *testing.T) {
	year2023 := 2023
	if !geminiTMDBYearCompatible(2023, &year2023, "2024") {
		t.Fatal("year difference of one should be accepted")
	}
	if geminiTMDBYearCompatible(2023, &year2023, "2025") {
		t.Fatal("year difference of two should be rejected")
	}
}

func TestPreserveRecognizedContextOverridesWeakGeminiType(t *testing.T) {
	year := 2020
	rec := preserveRecognizedContext(ai.RecognizedTitle{ENTitle: "The Bureau", Year: &year, MediaType: "movie"}, &storage.Movie{
		TmdbID: 62476, Year: "2015", MediaType: "tv",
	})
	if rec.Year == nil || *rec.Year != 2015 || rec.MediaType != "tv" {
		t.Fatalf("recognition context=%+v", rec)
	}
}

func TestIdentityReplacementConflictKeepsStableRecord(t *testing.T) {
	existing := &storage.Movie{Filename: "Bjuro.avi", TmdbID: 62476, Year: "2015", MediaType: "tv"}
	if !identityReplacementConflicts(existing, storage.Movie{TmdbID: 802663, Year: "2020", MediaType: "movie"}) {
		t.Fatal("movie replacement must conflict with recognized TV identity")
	}
	if identityReplacementConflicts(existing, storage.Movie{TmdbID: 62476, Year: "2015", MediaType: "tv"}) {
		t.Fatal("stable identity was reported as a conflict")
	}
}

func TestUpdateMovieSameExplicitTypeClearsReviewWithoutAI(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	filename := "Bjuro legend/Bjuro legend.08.avi"
	if err := db.SaveMoviesBatch(ctx, []storage.Movie{{Filename: filename, TmdbID: 62476, TitleEN: "Le Bureau des légendes", Year: "2015", MediaType: "tv", NeedsReview: true, ReviewReason: "media_type_conflict"}}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx, app.db = ctx, db
	if err := app.updateMovieWithMediaType(ctx, filename, "", "tv"); err != nil {
		t.Fatal(err)
	}
	movie, err := db.GetMovieByFilename(ctx, filename)
	if err != nil || movie == nil {
		t.Fatalf("movie=%+v err=%v", movie, err)
	}
	if movie.NeedsReview || movie.ReviewReason != "" || movie.TmdbID != 62476 || movie.MediaType != "tv" || movie.RecognitionSource != "manual_id" {
		t.Fatalf("confirmed movie=%+v", movie)
	}
}

func TestValidatedAliasIsStrongSignal(t *testing.T) {
	info := &tmdb.MovieInfo{
		TitleEN:      "Harry Potter and the Philosopher's Stone",
		MatchedAlias: "Harry Potter and the Sorcerer's Stone",
	}
	if got := maxTitleSimilarity("Harry Potter and the Sorcerer's Stone", info); got < 0.99 {
		t.Fatalf("validated alias similarity = %.3f; want exact match", got)
	}
}

func TestExtractIMDBID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://www.imdb.com/title/tt4925252", "tt4925252"},
		{"https://www.imdb.com/title/tt2316411/?ref_=ext_shr_lnk", "tt2316411"},
		{"TT2316411", "tt2316411"},
		{"https://www.imdb.com/title/not-an-id", ""},
		{"Enemy 2013", ""},
	}
	for _, tt := range tests {
		if got := extractIMDBID(tt.input); got != tt.want {
			t.Errorf("extractIMDBID(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestUpdateMovieIMDbHintUsesFindAndBypassesSearch(t *testing.T) {
	ctx := context.Background()
	searchCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"):
			searchCalled = true
			t.Fatalf("IMDb hint must not use text search: %s", r.URL.Path)
		case r.URL.Path == "/3/find/tt2316411":
			if r.URL.Query().Get("external_source") != "imdb_id" {
				t.Fatalf("missing external_source=imdb_id")
			}
			_, _ = io.WriteString(w, `{"movie_results":[{"id":103663}],"tv_results":[]}`)
		case r.URL.Path == "/3/movie/103663":
			_, _ = io.WriteString(w, `{
				"id":103663,
				"title":"Ворог",
				"original_title":"Enemy",
				"release_date":"2014-03-14",
				"overview":"Історія професора.",
				"genres":[{"name":"Thriller"}],
				"credits":{"cast":[]}
			}`)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	tempDB := filepath.Join(t.TempDir(), "movies.db")
	db, err := storage.New(tempDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.db = db
	app.cfg = &config.Config{MediaFolderPath: t.TempDir(), TMDBAPIKey: "fake-key", PostersDir: t.TempDir()}
	app.tmdbClient = tmdb.NewClient(app.cfg)
	app.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})

	filename := "Vrag.2013.mkv"
	if err := app.updateMovie(ctx, filename, "https://www.imdb.com/title/tt2316411/"); err != nil {
		t.Fatal(err)
	}
	movie, err := db.GetMovieByFilename(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	if movie == nil || movie.TmdbID != 103663 || movie.TitleEN != "Enemy" {
		t.Fatalf("unexpected saved movie: %+v", movie)
	}
	if searchCalled {
		t.Fatal("text search was called")
	}
}

func TestUpdateMovieUnknownIMDbHintDoesNotFallBack(t *testing.T) {
	ctx := context.Background()
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/3/find/tt9999999" {
			t.Errorf("unexpected fallback request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"movie_results":[],"tv_results":[]}`)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	tempDB := filepath.Join(t.TempDir(), "movies.db")
	db, err := storage.New(tempDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.db = db
	app.cfg = &config.Config{TMDBAPIKey: "fake-key", PostersDir: t.TempDir()}
	app.tmdbClient = tmdb.NewClient(app.cfg)
	app.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})

	err = app.updateMovie(ctx, "unknown.mkv", "tt9999999")
	if err == nil {
		t.Fatal("unknown IMDb ID must return an explicit error")
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d; want only one /find request", requestCount)
	}
}

func TestFetchByIMDBSupportsTVResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/3/find/tt7654321":
			_, _ = io.WriteString(w, `{"movie_results":[],"tv_results":[{"id":76543}]}`)
		case "/3/tv/76543":
			_, _ = io.WriteString(w, `{"id":76543,"name":"Тестовий серіал","original_name":"Test Series","first_air_date":"2020-01-01","overview":"Опис","credits":{"cast":[]}}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	cfg := &config.Config{TMDBAPIKey: "fake-key", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})

	info, err := client.FetchByIMDB(context.Background(), "TT7654321", "")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.TMDBID != 76543 || info.MediaType != tmdb.MediaTypeTV {
		t.Fatalf("unexpected TV result: %+v", info)
	}
}

func TestRescueEmptyGeminiWithFolderFindsBorrowedTVTitle(t *testing.T) {
	ctx := context.Background()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(r.URL.Path, "/search/movie"):
			io.WriteString(w, `{"results":[]}`)
		case strings.Contains(r.URL.Path, "/search/tv"):
			if r.URL.Query().Get("query") != "Scarpetta" {
				io.WriteString(w, `{"results":[]}`)
				return
			}
			io.WriteString(w, `{
				"results": [{
					"id": 98765,
					"name": "Scarpetta",
					"original_name": "Scarpetta",
					"first_air_date": "2025-03-05",
					"media_type": "tv",
					"original_language": "en",
					"popularity": 42
				}]
			}`)
		case strings.Contains(r.URL.Path, "/tv/98765"):
			io.WriteString(w, `{
				"id": 98765,
				"name": "Scarpetta",
				"original_name": "Scarpetta",
				"first_air_date": "2025-03-05",
				"overview": "A medical examiner series.",
				"genres": [{"name": "Drama"}],
				"credits": {"cast": [{"name": "Nicole Kidman"}]}
			}`)
		default:
			t.Fatalf("unexpected TMDB request path: %s", r.URL.Path)
		}
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	mediaRoot := t.TempDir()
	app := NewApp()
	app.cfg = &config.Config{
		MediaFolderPath: mediaRoot,
		TMDBAPIKey:      "fake-key",
		PostersDir:      t.TempDir(),
	}
	app.tmdbClient = tmdb.NewClient(app.cfg)
	app.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})

	movie := app.rescueEmptyGeminiWithFolder(ctx, filepath.Join(mediaRoot, "Скарпетта", "Скарпетта 8.WEB-DLRip.mkv"))

	if movie.TmdbID != 98765 {
		t.Fatalf("expected rescue to find TV result, got tmdb_id=%d", movie.TmdbID)
	}
	if movie.MediaType != string(tmdb.MediaTypeTV) {
		t.Fatalf("expected media_type tv, got %q", movie.MediaType)
	}
	if movie.TitleEN != "Scarpetta" {
		t.Fatalf("expected title Scarpetta, got %q", movie.TitleEN)
	}
}
