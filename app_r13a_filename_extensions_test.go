package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/tmdb"
)

func TestR13AFilenameExtensionsAutomaticScan(t *testing.T) {
	const title = "Охрана звёзд"
	const tmdbID = 1551025
	for _, fixture := range []struct {
		filename string
		year     int
	}{
		{title + ".mkv", 0},
		{title + ".2026.mkv", 2026},
	} {
		t.Run(fixture.filename, func(t *testing.T) {
			a, dir := newTestAppDB(t, nil)
			a.cfg.MediaFolderPath, a.cfg.PostersDir = dir, t.TempDir()
			a.cfg.TMDBAPIKey, a.cfg.GeminiAPIKey = "fixture", "fixture"
			a.cfg.GeminiModels = []string{"gemini-r13a-flash", "gemini-r13a-pro"}
			a.tmdbClient, a.aiClient = tmdb.NewClient(a.cfg), ai.NewClient(a.cfg)
			t.Cleanup(a.tmdbClient.Close)

			var inference atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch filepath.Base(r.URL.Path) {
				case "gemini-r13a-flash:generateContent", "gemini-r13a-pro:generateContent":
					inference.Add(1)
				default:
					t.Errorf("unexpected Gemini endpoint %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`,
					`[{"id":0,"request_id":"recognition-0","en_title":"Low Confidence Guess","media_type":"movie","status":"resolved","confidence":0.2}]`)
			}))
			t.Cleanup(server.Close)
			t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)

			work := r10bWork{id: r10bMovie(tmdbID), year: 2026, original: "Guarding Stars", localized: "Охорона зірок"}
			var traffic r12aObservation
			rec := &r9bRecorder{}
			a.tmdbClient.SetTransport(rec.roundTrip(r12aTMDB(t, r12aFixture{
				primary: title, works: []r10bWork{work},
			}, &traffic, nil, nil)))

			path := filepath.Join(dir, fixture.filename)
			parsed := tmdb.ParseFilename(path)
			scan := a.recognizeAutomaticScan(t.Context(), []string{path})
			rows, err := a.db.GetAllMovies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			wire := rec.snapshot()
			t.Logf("R13A scan %s", r10bJSON(map[string]any{
				"clean_title": parsed.CleanTitle, "year": parsed.Year, "wire": wire,
				"ai": inference.Load(), "rows": rows,
			}))

			searchQuery := url.Values{"query": {title}, "language": {"uk-UA"}}
			if fixture.year != 0 {
				searchQuery.Set("year", strconv.Itoa(fixture.year))
			}
			wantWire := []string{
				"GET /3/search/movie?" + searchQuery.Encode(),
				"GET /3/movie/1551025?append_to_response=credits&language=uk-UA",
			}
			if !slices.Equal(wire, wantWire) || inference.Load() != 0 {
				t.Errorf("TMDB calls = %v, want %v; Gemini calls = %d, want 0", wire, wantWire, inference.Load())
			}
			if parsed.CleanTitle != title || parsed.Year != fixture.year ||
				scan.tmdbAccepted != 1 || scan.aiAccepted != 0 || scan.unresolved != 0 {
				t.Errorf("unexpected parse or scan result: %+v / %+v", parsed, scan)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d SQLite rows, want 1", len(rows))
			}
			row := rows[0]
			if row.Filename != fixture.filename || row.TmdbID != tmdbID || row.MediaType != "movie" ||
				row.TitleEN != work.original || row.TitleUA != work.localized || row.Year != strconv.Itoa(work.year) ||
				row.NeedsReview || row.ReviewReason != "" {
				t.Errorf("unexpected SQLite row: %+v", row)
			}
		})
	}
}
