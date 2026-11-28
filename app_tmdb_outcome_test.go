package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func TestR4AppScanOutcomes(t *testing.T) {
	for _, mode := range []string{"mixed", "outage", "stop_before_ai", "stop_before_next_batch", "rollback", "ai_rollback", "l2_outage"} {
		t.Run(mode, func(t *testing.T) {
			const aFile, bFile, cFile, dFile, eFile = "Alpha.2024.mkv", "Beta.2024.mkv", "Charlie.2024.mkv", "Delta.2024.mkv", "Echo.2024.mkv"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			existing := storage.Movie{
				Filename: cFile, TmdbID: 900, MediaType: "movie",
				TitleUA: "Українська назва", Plot: "Український опис із перевіреними деталями.",
				VoteCount: 1, RecognitionSource: "tmdb", NeedsReview: true, ReviewReason: "fixture",
			}
			a, dir := newTestAppDB(t, []storage.Movie{existing})
			a.cfg.MediaFolderPath = dir
			a.cfg.PostersDir = t.TempDir()
			a.cfg.TMDBAPIKey = "fixture"
			a.cfg.GeminiAPIKey = "fixture"
			a.cfg.GeminiModels = []string{"gemini-2.5-flash"}
			a.tmdbClient = tmdb.NewClient(a.cfg)
			defer a.tmdbClient.Close()
			a.aiClient = ai.NewClient(a.cfg)

			files := []string{aFile, bFile, cFile, dFile, eFile}
			switch mode {
			case "outage":
				files = []string{aFile, bFile}
			case "stop_before_ai", "ai_rollback", "l2_outage":
				files = []string{bFile}
			case "stop_before_next_batch":
				files = nil
				for i := 1; i <= 11; i++ {
					files = append(files, fmt.Sprintf("Fixture%02d.2024.mkv", i))
				}
			}

			var mu sync.Mutex
			var inputs [][]string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, ":generateContent") {
					t.Errorf("unexpected AI endpoint %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var submitted []string
				var response []map[string]any
				for _, file := range files {
					if strings.Contains(string(body), file) {
						i := len(submitted)
						submitted = append(submitted, file)
						response = append(response, map[string]any{"id": i, "request_id": fmt.Sprintf("recognition-%d", i), "original_file": file, "en_title": "Unverified Hypothesis", "media_type": "movie", "status": "resolved", "confidence": 0.98})
					}
				}
				mu.Lock()
				inputs = append(inputs, submitted)
				mu.Unlock()
				if mode == "ai_rollback" && len(submitted) == 1 {
					response[0]["en_title"] = "Verified Hypothesis"
				}
				reply, _ := json.Marshal(response)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, reply)
			}))
			defer provider.Close()
			t.Setenv("GOOGLE_GEMINI_BASE_URL", provider.URL)

			fastWorker := make(chan struct{})
			var releaseSlow sync.Once
			a.eventEmitter = func(_ context.Context, event string, data ...interface{}) {
				if event != "log-message" {
					return
				}
				message := fmt.Sprint(data...)
				if strings.Contains(message, "TMDB: '"+dFile+"'") {
					releaseSlow.Do(func() { close(fastWorker) })
				}
				if mode == "stop_before_next_batch" && strings.Contains(message, "Gemini пачка 2/2") {
					cancel()
				}
			}
			var endpointMu sync.Mutex
			wire := make(map[string]int)
			a.tmdbClient.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				path, query := r.URL.Path, r.URL.Query().Get("query")
				endpointMu.Lock()
				wire[path+" "+query]++
				endpointMu.Unlock()
				status, body := 200, `{"results":[]}`
				switch {
				case mode == "outage" || query == "Charlie" || query == "Cached Hypothesis":
					status, body = 500, "fixture outage"
				case query == "Echo":
					return nil, context.Canceled
				case (query == "Alpha" || query == "Delta" || query == "Verified Hypothesis") && path == "/3/search/movie":
					id := map[string]int{"Alpha": 101, "Delta": 104, "Verified Hypothesis": 102}[query]
					if query == "Alpha" && (mode == "mixed" || mode == "rollback") {
						select {
						case <-fastWorker:
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
					}
					body = fmt.Sprintf(`{"results":[{"id":%d,"title":%q,"release_date":"2024-01-01"}]}`, id, query)
				case path == "/3/movie/101" || path == "/3/movie/104" || path == "/3/movie/102":
					id := path[strings.LastIndex(path, "/")+1:]
					title := map[string]string{"101": "Alpha", "104": "Delta", "102": "Verified Hypothesis"}[id]
					body = fmt.Sprintf(`{"id":%s,"title":"Українська назва","original_title":%q,"release_date":"2024-01-01","overview":"Український опис."}`, id, title)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))

			if mode == "l2_outage" || mode == "mixed" {
				file := bFile
				if mode == "mixed" {
					file = cFile
				}
				err := a.db.SaveAIResolution(ctx, storage.AIResolution{OriginalFilename: file, ResolvedTitle: "Cached Hypothesis", MediaType: "movie", Confidence: 0.99, PipelineVersion: recognitionPipelineVersion})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "rollback" || mode == "ai_rollback" {
				db, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				file := aFile
				if mode == "ai_rollback" {
					file = bFile
				}
				_, err = db.ExecContext(ctx, fmt.Sprintf(`
					CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY);
					CREATE TABLE fixture_guard(id INTEGER REFERENCES fixture_parent(id) DEFERRABLE INITIALLY DEFERRED);
					CREATE TRIGGER fixture_abort AFTER INSERT ON movies WHEN NEW.filename = '%s'
						BEGIN INSERT INTO fixture_guard VALUES (1); END;
				`, file))
				if err != nil {
					t.Fatal(err)
				}
			}

			paths := make([]string, len(files))
			for i, file := range files {
				paths[i] = filepath.Join(dir, file)
			}
			var movies []storage.Movie
			var queue, accepted []string
			if mode == "outage" {
				a.diskFileScanner = func(context.Context) ([]string, error) {
					return append(paths, filepath.Join(dir, cFile)), nil
				}
				a.aiModelsCache = a.cfg.GeminiModels
				a.RunScan()
				a.wg.Wait()
			} else {
				movies, queue, _ = a.processScanResults(ctx, a.runTMDBScan(ctx, paths))
				err := a.saveRecognitionBatch(ctx, movies, "tmdb_scan")
				if (err != nil) != (mode == "rollback") {
					t.Errorf("TMDB batch: %v", err)
				}
				if mode == "rollback" && (err == nil || !strings.Contains(err.Error(), "batch commit failed")) {
					t.Errorf("expected failure at SQLite COMMIT: %v", err)
				}
				if mode == "stop_before_ai" {
					cancel()
				}
				accepted = a.processGeminiQueue(ctx, queue, a.aiClient)
			}
			var queued []string
			for _, path := range queue {
				queued = append(queued, filepath.Base(path))
			}
			slices.Sort(queued)
			if mode == "mixed" || mode == "rollback" {
				delta := slices.IndexFunc(movies, func(movie storage.Movie) bool { return movie.Filename == dFile })
				alpha := slices.IndexFunc(movies, func(movie storage.Movie) bool { return movie.Filename == aFile })
				if delta < 0 || alpha <= delta {
					t.Errorf("expected Delta to complete before Alpha, got indices %d, %d", delta, alpha)
				}
			}
			rows, err := a.db.GetAllMovies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			stored := make(map[string]int)
			for _, row := range rows {
				stored[row.Filename] = row.TmdbID
				if row.Filename == cFile && (row.TitleUA != existing.TitleUA || row.Plot != existing.Plot ||
					row.NeedsReview != existing.NeedsReview || row.ReviewReason != existing.ReviewReason ||
					row.RecognitionSource != existing.RecognitionSource) {
					t.Errorf("confirmed metadata changed: %+v", row)
				}
			}
			wantQueue := []string{bFile}
			wantAI := 1
			wantRows := map[string]int{cFile: 900}
			switch mode {
			case "mixed":
				wantRows[aFile], wantRows[bFile], wantRows[dFile] = 101, 0, 104
			case "outage":
				wantQueue, wantAI = nil, 0
				wantRows[aFile], wantRows[bFile] = 0, 0
			case "rollback":
				wantRows[bFile] = 0
			case "stop_before_ai":
				wantAI = 0
			case "stop_before_next_batch":
				wantQueue = files
				for _, path := range queue[:min(len(queue), 10)] {
					wantRows[filepath.Base(path)] = 0
				}
			case "l2_outage":
				wantQueue, wantAI = nil, 0
				wantRows[bFile] = 0
			}
			mu.Lock()
			defer mu.Unlock()
			metrics := a.tmdbClient.RequestMetrics()
			aiMetrics := a.aiClient.CallMetrics()
			t.Logf("R4 App %s queue=%q wireAI=%v accepted=%v stored=%v search=%d details=%d ai_batches=%d", mode, queued, inputs, accepted, stored, metrics.SearchCalls, metrics.DetailsCalls, aiMetrics.RecognitionBatch)
			wantInput := []string{bFile}
			if mode == "stop_before_next_batch" {
				wantInput = make([]string, 0, 10)
				for _, path := range queue[:min(len(queue), 10)] {
					wantInput = append(wantInput, filepath.Base(path))
				}
				slices.Sort(wantInput)
				if ctx.Err() != context.Canceled {
					t.Error("the second batch barrier did not cancel the operation")
				}
			}
			if !slices.Equal(queued, wantQueue) || len(inputs) != wantAI || wantAI == 1 && !slices.Equal(inputs[0], wantInput) {
				t.Errorf("wrong AI eligibility or requests: queued=%q submitted=%v", queued, inputs)
			}
			if aiMetrics.RecognitionBatch != int64(wantAI) || aiMetrics.Groq != 0 || aiMetrics.Grok != 0 {
				t.Errorf("wrong AI cascade metrics: %+v", aiMetrics)
			}
			if len(stored) != len(wantRows) {
				t.Errorf("wrong row count: %v, want %v", stored, wantRows)
			}
			for file, id := range wantRows {
				if got, ok := stored[file]; !ok || got != id {
					t.Errorf("stored %s = %d (exists=%t), want %d", file, got, ok, id)
				}
			}
			if len(accepted) != 0 {
				t.Errorf("reported uncommitted or unverified identities: %v", accepted)
			}
			if mode == "ai_rollback" || mode == "mixed" {
				resolution, _, err := a.db.GetAIResolution(t.Context(), bFile, recognitionPipelineVersion)
				if err != nil || resolution != nil {
					t.Errorf("uncommitted/unverified L2 identity: %+v, %v", resolution, err)
				}
			}
			if mode == "mixed" {
				endpointMu.Lock()
				defer endpointMu.Unlock()
				if wire["/3/search/movie Echo"] != 1 || wire["/3/search/movie Cached Hypothesis"] != 0 {
					t.Errorf("canceled file retried or outage used L2: %v", wire)
				}
			}
		})
	}
}
