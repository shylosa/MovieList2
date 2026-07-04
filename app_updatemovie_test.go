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
