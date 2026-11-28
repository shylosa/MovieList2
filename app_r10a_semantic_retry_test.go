package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

// The same fixture runs against R9-B via a Go overlay of the base sources.
func TestR10ASemanticRetryAB(t *testing.T) {
	baseline := os.Getenv("MOVIELIST_R10A_BASELINE") == "1"
	for _, scenario := range []struct {
		name, first, rejection, stop string
		size, firstAccepted          int
		secondAccepted               int
	}{
		{name: "all_confirmed", first: "found", size: 3, firstAccepted: 3},
		{name: "empty", first: "empty", rejection: "missing_english_title", size: 1, secondAccepted: 1},
		{name: "low_confidence", first: "low", rejection: "low_confidence", size: 1, secondAccepted: 1},
		{name: "status", first: "status", rejection: "unacceptable_title_or_status", size: 1, secondAccepted: 1},
		{name: "year", first: "year", rejection: "year_mismatch", size: 1, secondAccepted: 1},
		{name: "type", first: "type", rejection: "media_type_mismatch", size: 1, secondAccepted: 1},
		{name: "jw", first: "jw", rejection: "title_similarity_below_threshold", size: 1, secondAccepted: 1},
		{name: "no_match", first: "miss", rejection: "tmdb_no_match", size: 1, secondAccepted: 1},
		{name: "tmdb_error", first: "error", size: 1},
		{name: "tmdb_outage", first: "outage", size: 2},
		{name: "provider_error", first: "provider", size: 1},
		{name: "request_id_conflict", first: "conflict", rejection: "missing_or_conflicting_response", size: 1, secondAccepted: 1},
		{name: "missing", first: "missing", rejection: "missing_or_conflicting_response", size: 1, secondAccepted: 1},
		{name: "8_plus_2", first: "empty", rejection: "missing_english_title", size: 10, firstAccepted: 8, secondAccepted: 2},
		{name: "all_unverified", first: "empty", rejection: "missing_english_title", size: 3, secondAccepted: 3},
		{name: "no_unverified_mixed", first: "error", size: 3, firstAccepted: 2},
		{name: "stop_before", first: "empty", stop: "before", size: 1},
		{name: "stop_inference", first: "empty", rejection: "missing_english_title", stop: "inference", size: 1},
		{name: "stop_verification", first: "empty", rejection: "missing_english_title", stop: "verification", size: 2, firstAccepted: 1},
		{name: "rollback", first: "empty", rejection: "missing_english_title", size: 2, firstAccepted: 1},
		{name: "confirmed", first: "empty", size: 1},
		{name: "confirmed_before_retry", first: "empty", size: 1},
		{name: "confirmed_during_retry", first: "empty", rejection: "missing_english_title", size: 1},
		{name: "same_basename", first: "empty", rejection: "missing_english_title", size: 2, secondAccepted: 2},
		{name: "same_identity", first: "empty", rejection: "missing_english_title", size: 2, secondAccepted: 2},
		{name: "fix_selected", first: "empty", size: 1},
		{name: "guarding_stars", first: "empty", rejection: "missing_english_title", size: 2, firstAccepted: 1, secondAccepted: 1},
		{name: "other_work", first: "empty", rejection: "missing_english_title", size: 1},
		{name: "semantic_provider_error", first: "empty", rejection: "missing_english_title", size: 2, firstAccepted: 1},
		{name: "semantic_no_content", first: "empty", rejection: "missing_english_title", size: 2, firstAccepted: 1},
		{name: "semantic_missing", first: "empty", rejection: "missing_english_title", size: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a, dir := newTestAppDB(t, nil)
			a.cfg.MediaFolderPath = dir
			a.cfg.PostersDir = t.TempDir()
			a.cfg.TMDBAPIKey, a.cfg.GeminiAPIKey = "tmdb-fixture", "ai-fixture"
			const primary, alternate = "gemini-r10-flash", "gemini-r10-pro"
			a.cfg.GeminiModels = []string{primary, alternate}
			a.tmdbClient = tmdb.NewClient(a.cfg)
			t.Cleanup(a.tmdbClient.Close)
			a.aiClient = ai.NewClient(a.cfg)

			type work struct {
				id                      int
				title, translated, year string
			}
			paths := make([]string, scenario.size)
			works := make([]work, scenario.size)
			oracle := make(map[string]int)
			relative := make(map[string]int)
			for i := range paths {
				title := fmt.Sprintf("Назва фільму %02d", i)
				works[i] = work{9000 + i, fmt.Sprintf("Verified Work %02d", i), title, "2024"}
				paths[i] = filepath.Join(dir, title+".2024.mkv")
				switch scenario.name {
				case "guarding_stars":
					if i == 1 {
						works[i] = work{1551025, "Guarding Stars", "Охрана звёзд", "2026"}
						paths[i] = filepath.Join(dir, "Охрана звёзд.2026.mkv")
					}
				case "same_basename":
					works[i].translated = "Однакова назва"
					paths[i] = filepath.Join(dir, fmt.Sprintf("Release %d", i), "Однакова назва.2024.mkv")
				case "same_identity":
					works[i] = work{9000, "Verified Work", "Спільна назва", "2024"}
					paths[i] = filepath.Join(dir, fmt.Sprintf("Release %d", i), "Спільна назва.2024.mkv")
					if i == 1 {
						paths[i] = filepath.Join(dir, "Release 1", "Спільна назва.2024.1080p.mkv")
					}
				}
				key := a.getFileIdentifier(paths[i])
				relative[key] = i
				oracle[key] = works[i].id
			}

			const confirmedID = 8888
			confirm := func() {
				if err := a.db.SaveMovie(t.Context(), storage.Movie{
					Filename: a.getFileIdentifier(paths[0]), TmdbID: confirmedID,
					MediaType: "tv", TitleEN: "Manual confirmation", RecognitionSource: "manual",
				}); err != nil {
					t.Error(err)
				}
			}
			if scenario.name == "confirmed" {
				confirm()
			}
			if scenario.name == "rollback" {
				raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				_, err = raw.ExecContext(ctx, `
					CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY);
					CREATE TABLE fixture_guard(id INTEGER REFERENCES fixture_parent(id) DEFERRABLE INITIALLY DEFERRED);
					CREATE TRIGGER fixture_abort AFTER INSERT ON movies
					BEGIN INSERT INTO fixture_guard VALUES (1); END;`)
				if err != nil {
					t.Fatal(err)
				}
			}

			type wireCall struct {
				Purpose, Model string
				Files          []int
			}
			var mu sync.Mutex
			var calls []wireCall
			var httpCounts [4]int // search, details, aliases, poster
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Contents []struct{ Parts []struct{ Text string } }
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "fixture", 400)
					return
				}
				prompt := req.Contents[0].Parts[0].Text
				_, jsonLine, ok := strings.Cut(prompt, "do not re-guess them):\n")
				var input []struct {
					ai.FileRecognitionContext
					PreviousRejection string `json:"previous_rejection"`
				}
				if !ok || json.Unmarshal([]byte(strings.SplitN(jsonLine, "\n", 2)[0]), &input) != nil {
					t.Error("expected recognition contexts in prompt")
					http.Error(w, "fixture", 400)
					return
				}
				model, _, _ := strings.Cut(filepath.Base(r.URL.Path), ":")
				semantic := strings.Contains(prompt, "previous_rejection")
				purpose := "primary"
				if semantic {
					purpose = "semantic"
				} else if model == alternate {
					purpose = "technical"
				} else if scenario.first == "missing" {
					mu.Lock()
					if len(calls) > 0 {
						purpose = "missing"
					}
					mu.Unlock()
				}
				call := wireCall{Purpose: purpose, Model: model}
				var rows []ai.RecognizedTitle
				for _, item := range input {
					index := -1
					for i, path := range paths {
						sameParent := item.ParentDir == filepath.Base(filepath.Dir(path))
						if item.OriginalFile == filepath.Base(path) && (scenario.name != "same_basename" || sameParent || semantic) {
							if semantic && scenario.name == "same_basename" {
								// Request-ID slots are tracked from the initial batch below.
								continue
							}
							index = i
							break
						}
					}
					if semantic && scenario.name == "same_basename" {
						mu.Lock()
						slot, _ := strconv.Atoi(strings.TrimPrefix(item.RequestID, "recognition-"))
						index = calls[0].Files[slot]
						mu.Unlock()
					}
					if index < 0 {
						t.Error("unexpected file in recognition batch")
						continue
					}
					call.Files = append(call.Files, index)
					year, _ := strconv.Atoi(works[index].year)
					row := ai.RecognizedTitle{ID: item.ID, RequestID: item.RequestID, OriginalFile: item.OriginalFile,
						ENTitle: works[index].title, Year: &year, MediaType: "movie", Confidence: 0.98, Status: "resolved"}
					if semantic {
						if !strings.Contains(item.PreviousRejection, scenario.rejection) || item.ParentDir != "" || item.Year != year || item.MediaType != "movie" {
							t.Errorf("unexpected semantic hint for slot %s", item.RequestID)
						}
						if item.RequestID != fmt.Sprintf("recognition-%d", item.ID) || strings.Contains(prompt, dir) {
							t.Error("semantic retry lost correlation or exposed a full path")
						}
						row.ID = -1 // Deliberately misleading legacy ID.
						if scenario.name == "other_work" {
							row.ENTitle = "Plausible Unrelated Movie"
						}
						if scenario.name == "semantic_missing" {
							continue
						}
					} else if index >= scenario.firstAccepted {
						switch scenario.first {
						case "empty":
							row.ENTitle, row.Confidence, row.Status = "", 0, "unresolved"
						case "low":
							row.Confidence = 0.2
						case "status":
							row.Status = "ambiguous"
						case "year", "type", "jw", "miss", "error":
							row.ENTitle = "First Hypothesis"
						case "missing":
							continue
						case "conflict":
							conflicting := row
							conflicting.ENTitle = "Conflicting Hypothesis"
							rows = append(rows, conflicting)
						}
					}
					rows = append(rows, row)
				}
				slices.Reverse(rows)
				mu.Lock()
				calls = append(calls, call)
				mu.Unlock()
				if !semantic && scenario.name == "confirmed_before_retry" {
					confirm()
				}
				if semantic && scenario.stop == "inference" {
					cancel()
				}
				if scenario.first == "provider" || (semantic && scenario.name == "semantic_provider_error") {
					http.Error(w, `{"error":{"code":503,"message":"fixture unavailable"}}`, 503)
					return
				}
				if semantic && scenario.name == "semantic_no_content" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"candidates":[{"finishReason":"SAFETY"}]}`)
					return
				}
				if rows == nil {
					rows = []ai.RecognizedTitle{}
				}
				payload, _ := json.Marshal(rows)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, payload)
			}))
			t.Cleanup(server.Close)
			t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)

			const wrongID = 9900
			a.tmdbClient.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				path, query := r.URL.Path, r.URL.Query().Get("query")
				status, body := 200, `{"results":[]}`
				count := 1
				switch {
				case strings.HasPrefix(path, "/3/search/"):
					count = 0
					switch {
					case scenario.first == "outage":
						status = 404
					case query == "First Hypothesis":
						switch scenario.first {
						case "error":
							status = 404
						case "year", "jw", "miss":
							if path == "/3/search/movie" {
								title := query
								if scenario.first == "miss" {
									title = "Unrelated Result"
								}
								body = fmt.Sprintf(`{"results":[{"id":%d,"title":%q,"original_title":%q,"release_date":"2010-01-01"}]}`, wrongID, title, title)
							}
						case "type":
							if path == "/3/search/tv" {
								body = fmt.Sprintf(`{"results":[{"id":%d,"name":%q,"original_name":%q,"first_air_date":"2024-01-01"}]}`, wrongID, query, query)
							}
						}
					case path == "/3/search/movie":
						for _, w := range append(slices.Clone(works), work{wrongID, "Plausible Unrelated Movie", "Зовсім інша стрічка", "2024"}) {
							if query == w.title {
								body = fmt.Sprintf(`{"results":[{"id":%d,"title":%q,"original_title":%q,"release_date":"%s-01-01"}]}`, w.id, w.title, w.title, w.year)
								break
							}
						}
					}
				case strings.HasSuffix(path, "/alternative_titles"):
					count, body = 2, `{"titles":[],"results":[]}`
				case strings.HasPrefix(path, "/t/p/"):
					count, body = 3, "fixture poster"
				case path == fmt.Sprintf("/3/tv/%d", wrongID):
					body = fmt.Sprintf(`{"id":%d,"name":"Український серіал","original_name":"First Hypothesis Series","first_air_date":"2024-01-01","overview":"Якісний український опис."}`, wrongID)
				case path == fmt.Sprintf("/3/movie/%d", wrongID):
					title, year := "First Hypothesis", "2010"
					switch scenario.first {
					case "jw":
						title, year = "Another Story", "2024"
					case "empty":
						title, year = "Plausible Unrelated Movie", "2024"
					}
					body = fmt.Sprintf(`{"id":%d,"title":"Інша художня стрічка","original_title":%q,"release_date":"%s-01-01","overview":"Якісний український опис."}`, wrongID, title, year)
				default:
					for i, w := range works {
						if path == fmt.Sprintf("/3/movie/%d", w.id) {
							if scenario.stop == "verification" && i >= scenario.firstAccepted {
								cancel()
							}
							if scenario.name == "confirmed_during_retry" {
								confirm()
							}
							body = fmt.Sprintf(`{"id":%d,"title":%q,"original_title":%q,"release_date":"%s-01-01","overview":"Якісний український опис.","poster_path":"/test.jpg"}`, w.id, w.translated, w.title, w.year)
							break
						}
					}
				}
				mu.Lock()
				httpCounts[count]++
				mu.Unlock()
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			if scenario.stop == "before" {
				a.eventEmitter = func(_ context.Context, event string, args ...interface{}) {
					if event == "log-message" && strings.Contains(fmt.Sprint(args...), "Gemini не розпізнав") {
						cancel()
					}
				}
			}
			var log bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
			defer slog.SetDefault(logger)
			accepted := -1
			if scenario.name == "fix_selected" {
				a.FixSelected([]FixRequest{{Filename: filepath.Base(paths[0])}})
			} else {
				accepted = a.recognizeAutomaticScan(ctx, paths).aiAccepted
			}
			stored, err := a.db.GetAllMovies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			correct, wrong, unresolved := 0, 0, 0
			states := make([]int, len(paths))
			for i := range states {
				states[i] = -1 // Missing row.
			}
			protect := scenario.name == "confirmed" || scenario.name == "confirmed_before_retry" ||
				(!baseline && scenario.name == "confirmed_during_retry")
			if protect {
				oracle[a.getFileIdentifier(paths[0])] = confirmedID
			}
			for _, m := range stored {
				i, ok := relative[m.Filename]
				if !ok {
					t.Fatalf("unexpected persisted filename %q", m.Filename)
				}
				states[i] = m.TmdbID
				switch {
				case m.TmdbID == 0:
					unresolved++
					if !m.NeedsReview || m.ReviewReason != "unresolved" {
						t.Errorf("invalid unresolved state: %+v", m)
					}
				case m.TmdbID != oracle[m.Filename]:
					wrong++
				default:
					correct++
					if m.TmdbID == confirmedID && (m.MediaType != "tv" || m.RecognitionSource != "manual" || m.TitleEN != "Manual confirmation") {
						t.Errorf("changed confirmed metadata: %+v", m)
					}
				}
			}
			want := scenario.firstAccepted
			if !baseline {
				want += scenario.secondAccepted
			}
			stop := scenario.stop == "before" || (!baseline && scenario.stop != "")
			if stop || scenario.name == "rollback" {
				want = 0
			}
			wantAccepted := want
			if protect {
				want++
			}
			wantRows := len(paths)
			if stop || scenario.name == "rollback" {
				wantRows = 0
			}
			if correct != want || wrong != 0 || len(stored) != wantRows || (accepted != -1 && accepted != wantAccepted) {
				t.Errorf("correct/wrong/rows/accepted=%d/%d/%d/%d, want %d/0/%d/%d", correct, wrong, len(stored), accepted, want, wantRows, wantAccepted)
			}
			if (ctx.Err() != nil) != stop {
				t.Errorf("unexpected STOP state: %v", ctx.Err())
			}

			mu.Lock()
			defer mu.Unlock()
			var semanticCalls, missingCalls, technicalCalls int
			for _, call := range calls {
				slices.Sort(call.Files)
				switch call.Purpose {
				case "semantic":
					semanticCalls++
					var subset []int
					for i := scenario.firstAccepted; i < scenario.size; i++ {
						subset = append(subset, i)
					}
					if !slices.Equal(call.Files, subset) {
						t.Errorf("semantic files %v, want %v", call.Files, subset)
					}
					if scenario.first != "missing" && scenario.first != "conflict" && call.Model != alternate {
						t.Errorf("semantic model %q, want %q", call.Model, alternate)
					}
				case "missing":
					missingCalls++
				case "technical":
					technicalCalls++
				}
			}
			wantSemantic := 0
			if !baseline && scenario.rejection != "" {
				wantSemantic = 1
			}
			wantMissing, wantTechnical, wantPrimary := 0, 0, 1
			switch scenario.first {
			case "missing":
				wantMissing = 1
			case "provider":
				wantTechnical = 1
			case "outage":
				wantPrimary = 0
			}
			if semanticCalls != wantSemantic || technicalCalls != wantTechnical || missingCalls != wantMissing ||
				len(calls) != wantPrimary+wantSemantic+wantTechnical+wantMissing {
				t.Errorf("unexpected wire inference: %+v", calls)
			}
			commits := strings.Count(log.String(), `"msg":"batch_save_success"`)
			commitErrors := strings.Count(log.String(), "batch commit failed")
			if scenario.first == "provider" && len(stored) > 0 {
				// The failed-provider branch writes placeholders directly.
				commits++
			}
			wantCommits := 1
			if stop || scenario.name == "rollback" || scenario.name == "confirmed" ||
				(!baseline && scenario.name == "confirmed_before_retry") {
				wantCommits = 0
			}
			if commits != wantCommits || (commitErrors == 1) != (scenario.name == "rollback") {
				t.Errorf("commits=%d, commit errors=%d, want commits=%d", commits, commitErrors, wantCommits)
			}
			t.Logf("R10A %s correct=%d false=%d unresolved=%d rows=%v batches=%d calls=%v search/details/alias/poster=%v commits=%d rollback=%d stop=%t protected=%t",
				scenario.name, correct, wrong, unresolved, states, a.aiClient.CallMetrics().RecognitionBatch,
				calls, httpCounts, commits, commitErrors, stop, protect)
		})
	}
}
