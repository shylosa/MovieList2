package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func TestR5AppCorrelation(t *testing.T) {
	for _, mode := range []string{
		"normal", "reordered", "swapped_ids", "duplicate_ids", "duplicate_key", "unknown_key",
		"legacy", "ambiguous_legacy", "omitted_numeric", "missing", "original_conflict", "same_basename",
		"empty", "8plus2", "confirmed", "rollback", "stop_before_ai", "stop_response", "stop_retry",
		"stop_before_tmdb", "stop_before_commit",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a, dir := newTestAppDB(t, nil)
			a.cfg.MediaFolderPath = dir
			a.cfg.PostersDir = t.TempDir()
			a.cfg.TMDBAPIKey, a.cfg.GeminiAPIKey = "fixture", "fixture"
			a.cfg.GeminiModels = []string{"gemini-2.5-flash"}
			a.tmdbClient = tmdb.NewClient(a.cfg)
			defer a.tmdbClient.Close()
			a.aiClient = ai.NewClient(a.cfg)

			files := []string{"A.2024.mkv", "B.2024.mkv", "C.2024.mkv"}
			if mode == "same_basename" {
				files = []string{"Source1/Film.mkv", "Source2/Film.mkv"}
			}
			if mode == "8plus2" {
				files = []string{"A.mkv", "B.mkv", "C.mkv", "D.mkv", "E.mkv", "F.mkv", "G.mkv", "H.mkv", "I.mkv", "J.mkv"}
			}
			titles := []string{"Alpha Origin", "Beta Chronicle", "Crimson Horizon", "Delta Passage", "Echo Harbor",
				"Frost Kingdom", "Golden Divide", "Hidden Valley", "Ivory Tower", "Jade River"}
			row := func(slot, numeric, hypothesis int) map[string]any {
				return map[string]any{
					"id": numeric, "request_id": fmt.Sprintf("recognition-%d", slot),
					"original_file": filepath.Base(files[slot]), "en_title": titles[hypothesis], "year": 2024,
					"media_type": "movie", "status": "resolved", "reason": "fixture", "confidence": 0.98,
				}
			}
			wire := make([]map[string]any, len(files))
			wantIDs := make([]int, len(files))
			for i := range files {
				wire[i], wantIDs[i] = row(i, i, i), 101+i
			}
			retries := []int(nil)
			wantMap := make(map[string]int)
			switch mode {
			case "reordered":
				wire = []map[string]any{wire[2], wire[0], wire[1]}
			case "swapped_ids":
				wire[0]["id"], wire[1]["id"], wire[2]["id"] = 2, 0, 1
			case "duplicate_ids", "same_basename":
				for _, item := range wire {
					item["id"] = 0
				}
			case "duplicate_key", "confirmed":
				wire = append(wire, row(1, 1, 3))
				wantIDs[1] = 0
				if mode == "confirmed" {
					existing := storage.Movie{Filename: files[1], TmdbID: 900, MediaType: "tv", TitleEN: "Confirmed", RecognitionSource: "manual"}
					if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{existing}); err != nil {
						t.Fatal(err)
					}
					wantIDs[1] = 900
				}
			case "unknown_key":
				wire[1]["request_id"] = "unknown"
				wantIDs[1], retries = 0, []int{1}
			case "legacy", "ambiguous_legacy":
				delete(wire[1], "request_id")
				if mode == "ambiguous_legacy" {
					dup := row(1, 1, 3)
					delete(dup, "request_id")
					wire, wantIDs[1] = append(wire, dup), 0
				}
			case "omitted_numeric":
				delete(wire[0], "request_id")
				delete(wire[0], "id")
				wantIDs[0] = 0
			case "missing", "stop_retry":
				wire, retries = []map[string]any{wire[0], wire[2]}, []int{1}
				wantIDs[1] = 0
			case "original_conflict":
				wire[0]["original_file"], wire[0]["en_title"] = files[1], titles[1]
				wantIDs[0] = 0
			case "empty":
				wire[1]["en_title"], wire[1]["confidence"], wire[1]["status"] = "", 0, "unresolved"
				wantIDs[1] = 0
			case "8plus2":
				wire, retries = wire[:8], []int{8, 9}
				wantIDs[9] = 0
			}

			var aiCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, ":generateContent") {
					t.Errorf("unexpected inference endpoint %s", r.URL.Path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				attempt := int(aiCalls.Add(1)) - 1
				output := wire
				if attempt > 0 {
					if attempt > len(retries) {
						t.Errorf("unexpected inference attempt %d", attempt+1)
						output = nil
					} else {
						slot := retries[attempt-1]
						for i := range files {
							if strings.Contains(string(body), fmt.Sprintf("recognition-%d", i)) != (i == slot) {
								t.Errorf("retry %d did not submit only slot %d", attempt, slot)
							}
						}
						output = []map[string]any{}
						if mode == "8plus2" && slot == 8 {
							output = []map[string]any{row(slot, slot, slot)}
						}
					}
				}
				if mode == "stop_response" || mode == "stop_retry" && attempt == 1 {
					cancel()
				}
				reply, _ := json.Marshal(output)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, reply)
			}))
			defer server.Close()
			t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)

			var tmdbCalls int
			a.tmdbClient.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				tmdbCalls++
				path, query := r.URL.Path, r.URL.Query().Get("query")
				body := `{"results":[]}`
				switch {
				case path == "/3/search/movie":
					i := slices.Index(titles, query)
					if i < 0 {
						t.Errorf("unexpected search %q", query)
					}
					body = fmt.Sprintf(`{"results":[{"id":%d,"title":%q,"release_date":"2024-01-01"}]}`, 101+i, query)
				case path == "/3/search/tv":
					// Only movie candidates are defined in this fixture.
				default:
					var id int
					if n, _ := fmt.Sscanf(path, "/3/movie/%d", &id); n == 1 && id >= 101 && id < 101+len(titles) {
						body = fmt.Sprintf(`{"id":%d,"title":"Українська назва","original_title":%q,"release_date":"2024-01-01","overview":"Український опис."}`, id, titles[id-101])
					} else {
						t.Errorf("unexpected TMDB endpoint %s", path)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			a.eventEmitter = func(_ context.Context, event string, args ...interface{}) {
				if mode == "stop_before_tmdb" && event == "scan-progress" ||
					mode == "stop_before_commit" && event == "log-message" && strings.Contains(fmt.Sprint(args...), titles[len(files)-1]) &&
						strings.Contains(fmt.Sprint(args...), "TMDB знайшов") {
					cancel()
				}
			}
			if mode == "rollback" {
				db, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				_, err = db.ExecContext(ctx, `
					CREATE TABLE r5_parent(id INTEGER PRIMARY KEY);
					CREATE TABLE r5_guard(id INTEGER REFERENCES r5_parent(id) DEFERRABLE INITIALLY DEFERRED);
					CREATE TRIGGER r5_abort AFTER INSERT ON movies WHEN NEW.filename = 'B.2024.mkv'
						BEGIN INSERT INTO r5_guard VALUES (1); END;`)
				if err != nil {
					t.Fatal(err)
				}
			}

			var paths, wantAccepted []string
			for i, file := range files {
				paths = append(paths, filepath.Join(dir, filepath.FromSlash(file)))
				if !strings.HasPrefix(mode, "stop_") && mode != "rollback" {
					wantMap[file] = wantIDs[i]
					if wantIDs[i] == 101+i {
						wantAccepted = append(wantAccepted, file)
					}
				}
			}
			wantAI := 1 + len(retries)
			if mode == "stop_before_ai" {
				cancel()
				wantAI = 0
			}
			accepted := a.processGeminiQueue(ctx, paths, a.aiClient)
			rows, err := a.db.GetAllMovies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			stored := make(map[string]int)
			var correct, wrong, unresolved int
			for _, movie := range rows {
				file := movie.Filename
				stored[file] = movie.TmdbID
				switch {
				case movie.TmdbID == 0:
					unresolved++
				case movie.TmdbID == 900 && movie.TitleEN == "Confirmed" && movie.MediaType == "tv" && movie.RecognitionSource == "manual":
					correct++
				case movie.TmdbID == 101+slices.Index(files, file) && movie.MediaType == "movie":
					correct++
				default:
					wrong++
				}
			}
			// R6: робоча логіка не пише ai_resolutions — таблиця залишається порожньою.
			raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
			if err != nil {
				t.Fatal(err)
			}
			var l2Rows int
			if err := raw.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM ai_resolutions").Scan(&l2Rows); err != nil {
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			if l2Rows != 0 {
				t.Errorf("R6: processGeminiQueue wrote %d ai_resolutions rows, want 0", l2Rows)
			}
			t.Logf("R5 %s files=%v wire=%v IDs=%v correct/wrong/unresolved=%d/%d/%d AI=%d TMDB=%d rows=%d l2=%d",
				mode, files, wire, stored, correct, wrong, unresolved, aiCalls.Load(), tmdbCalls, len(rows), l2Rows)
			if !maps.Equal(stored, wantMap) || !slices.Equal(accepted, wantAccepted) || wrong != 0 {
				t.Errorf("stored=%v accepted=%v, want %v and %v", stored, accepted, wantMap, wantAccepted)
			}
			if int(aiCalls.Load()) != wantAI {
				t.Errorf("AI calls=%d, want %d", aiCalls.Load(), wantAI)
			}
			if strings.HasPrefix(mode, "stop_") && ctx.Err() != context.Canceled {
				t.Error("cancellation barrier did not fire")
			}
			if strings.HasPrefix(mode, "stop_") && mode != "stop_before_commit" && tmdbCalls != 0 {
				t.Errorf("TMDB called %d times after cancellation", tmdbCalls)
			}
		})
	}
}
