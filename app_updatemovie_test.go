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

	// 3. Call UpdateMovie with a TMDB ID hint.
	// Since we mocked TMDB to return a Ukrainian title ("Зміна долі"),
	// it must save the movie and return nil without calling Gemini.
	err = app.UpdateMovie(ctx, "the-change-up.mkv", "12345")
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
