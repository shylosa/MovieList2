package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

// R6: ai_resolutions — історична таблиця. Фікстури сідають й перевіряються
// сирим SQL, щоб цей файл компілювався і проти бази R5 (A, overlay), і проти R6 (B).

func seedHistoricalL2(t *testing.T, dir, filename, title string, year int, mediaType string, confidence float64, pipelineVersion int) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.ExecContext(t.Context(), `INSERT INTO ai_resolutions (original_filename, resolved_title, year, media_type, confidence, pipeline_version, provider, model, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'gemini', 'gemini-2.5-flash', '2026-01-01T00:00:00Z')`,
		filename, title, year, mediaType, confidence, pipelineVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

func countAIResolutions(t *testing.T, dir string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM ai_resolutions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func deleteMovieRow(t *testing.T, dir, filename string) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), "DELETE FROM movies WHERE filename = ?", filename); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

// r6WireFixture — синтетичний TMDB: search за query → body, details за path → body.
type r6WireFixture struct {
	mu      sync.Mutex
	wire    map[string]int
	search  map[string]string // query → results body
	details map[string]string // path → body
	fail500 map[string]bool   // query → HTTP 500
}

func newR6Wire() *r6WireFixture {
	return &r6WireFixture{wire: map[string]int{}, search: map[string]string{}, details: map[string]string{}, fail500: map[string]bool{}}
}

func (f *r6WireFixture) roundTrip(r *http.Request) (*http.Response, error) {
	path, query := r.URL.Path, r.URL.Query().Get("query")
	key := path + " query=" + query
	f.mu.Lock()
	f.wire[key]++
	f.mu.Unlock()
	if f.fail500[query] {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("fixture outage"))}, nil
	}
	if body, ok := f.details[path]; ok {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	body, ok := f.search[query]
	if !ok {
		body = `{"results":[]}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (f *r6WireFixture) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wire[key]
}

// r6AIProvider — синтетичний Gemini: на кожен файл у тілі повертає hypothesis із per-mode картою.
type r6AIProvider struct {
	mu     sync.Mutex
	inputs [][]string
	byFile map[string]string // basename → en_title
}

func (p *r6AIProvider) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		for file, title := range p.byFile {
			if strings.Contains(string(body), file) {
				i := len(submitted)
				submitted = append(submitted, file)
				response = append(response, map[string]any{
					"id": i, "request_id": fmt.Sprintf("recognition-%d", i), "original_file": file,
					"en_title": title, "year": 2024, "media_type": "movie", "status": "resolved", "confidence": 0.98,
				})
			}
		}
		// детермінований порядок: за порядком входу byFile не гарантується, тому сортуємо submitted
		slices.Sort(submitted)
		slices.SortFunc(response, func(a, b map[string]any) int {
			return strings.Compare(fmt.Sprint(a["original_file"]), fmt.Sprint(b["original_file"]))
		})
		p.mu.Lock()
		p.inputs = append(p.inputs, submitted)
		p.mu.Unlock()
		reply, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, reply)
	}
}

func setupR6App(t *testing.T, seed []storage.Movie, wire *r6WireFixture, aiProv *r6AIProvider) (*App, string, context.CancelFunc) {
	t.Helper()
	_, cancel := context.WithCancel(t.Context())
	a, dir := newTestAppDB(t, seed)
	a.cfg.MediaFolderPath = dir
	a.cfg.PostersDir = t.TempDir()
	a.cfg.TMDBAPIKey = "fixture"
	a.cfg.GeminiAPIKey = "fixture"
	a.cfg.GeminiModels = []string{"gemini-2.5-flash"}
	a.tmdbClient = tmdb.NewClient(a.cfg)
	t.Cleanup(func() { a.tmdbClient.Close() })
	a.tmdbClient.SetTransport(roundTripFunc(wire.roundTrip))
	a.aiClient = ai.NewClient(a.cfg)
	a.aiModelsCache = a.cfg.GeminiModels
	server := httptest.NewServer(aiProv.handler(t))
	t.Cleanup(server.Close)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)
	return a, dir, cancel
}

// TestR6AutomaticScanWithoutL2 — R6-A: автоматичний шлях A/B.
// B-очікування: NO_MATCH → AI → TMDB-верифікація → commit; ai_resolutions не читається й не пишеться.
func TestR6AutomaticScanWithoutL2(t *testing.T) {
	const alphaFile, charlieFile = "Alpha.2024.mkv", "Charlie.2024.mkv"

	for _, mode := range []string{
		"fresh_no_l2", "l2_hit_divergent_ai", "l2_hit_same_as_ai", "l2_stale_pipeline",
		"l2_low_confidence", "l2_wrong_year", "l2_wrong_type", "confirmed_protected",
		"tmdb_outage", "stop_before_ai", "commit_rollback",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			wire := newR6Wire()
			// Базовий filename-пошук завжди чистий промах, крім окремих режимів.
			wire.search["Alpha Hypothesis"] = `{"results":[{"id":201,"title":"Alpha Hypothesis","original_title":"Alpha Hypothesis","release_date":"2024-01-01"}]}`
			wire.details["/3/movie/201"] = `{"id":201,"title":"Українська назва","original_title":"Alpha Hypothesis","release_date":"2024-01-01","overview":"Український опис."}`
			wire.search["Cached Film"] = `{"results":[{"id":555,"title":"Cached Film","original_title":"Cached Film","release_date":"2024-01-01"}]}`
			wire.details["/3/movie/555"] = `{"id":555,"title":"Кешована назва","original_title":"Cached Film","release_date":"2024-01-01","overview":"Кешований опис."}`
			wire.search["Cached Show"] = `{"results":[{"id":777,"name":"Cached Show","original_name":"Cached Show","first_air_date":"2024-01-01"}]}`
			wire.details["/3/tv/777"] = `{"id":777,"name":"Серіал","original_name":"Cached Show","first_air_date":"2024-01-01","overview":"Опис серіалу."}`

			aiProv := &r6AIProvider{byFile: map[string]string{alphaFile: "Alpha Hypothesis", charlieFile: "Charlie Hypothesis"}}

			var seed []storage.Movie
			file := alphaFile
			l2Seed := ""
			l2Year := 2024
			l2Type := "movie"
			l2Conf := 0.99
			l2PV := 25
			switch mode {
			case "l2_hit_divergent_ai":
				l2Seed = "Cached Film"
				aiProv.byFile[alphaFile] = "Fresh Hypothesis" // TMDB такого не знає → unresolved у B
			case "l2_hit_same_as_ai":
				l2Seed = "Cached Film"
				aiProv.byFile[alphaFile] = "Cached Film"
			case "l2_stale_pipeline":
				l2Seed, l2PV = "Cached Film", 24
			case "l2_low_confidence":
				l2Seed, l2Conf = "Cached Film", 0.10
			case "l2_wrong_year":
				l2Seed, l2Year = "Cached Film", 1999
			case "l2_wrong_type":
				l2Seed, l2Type = "Cached Show", "tv"
			case "confirmed_protected":
				file = charlieFile
				l2Seed = "Cached Film"
				seed = []storage.Movie{{Filename: charlieFile, TmdbID: 900, MediaType: "movie",
					TitleUA: "Підтверджена назва", TitleEN: "Confirmed", Plot: "Підтверджений опис.",
					RecognitionSource: "manual_id", NeedsReview: true, ReviewReason: "fixture", VoteCount: 1}}
				wire.fail500["Charlie"] = true
			case "tmdb_outage":
				l2Seed = "Cached Film"
				wire.fail500["Alpha"] = true
			case "commit_rollback":
				l2Seed = "Cached Film"
			}

			a, dir, cancel2 := setupR6App(t, seed, wire, aiProv)
			defer cancel2()
			if l2Seed != "" {
				seedHistoricalL2(t, dir, file, l2Seed, l2Year, l2Type, l2Conf, l2PV)
			}
			if mode == "commit_rollback" {
				raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = raw.ExecContext(ctx, `
					CREATE TABLE r6_parent(id INTEGER PRIMARY KEY);
					CREATE TABLE r6_guard(id INTEGER REFERENCES r6_parent(id) DEFERRABLE INITIALLY DEFERRED);
					CREATE TRIGGER r6_abort AFTER INSERT ON movies WHEN NEW.filename = 'Alpha.2024.mkv'
						BEGIN INSERT INTO r6_guard VALUES (1); END;`)
				if err != nil {
					t.Fatal(err)
				}
				if err := raw.Close(); err != nil {
					t.Fatal(err)
				}
			}
			l2Before := countAIResolutions(t, dir)

			path := filepath.Join(dir, file)
			movies, queue, _ := a.processScanResults(ctx, a.runTMDBScan(ctx, []string{path}))
			saveErr := a.saveRecognitionBatch(ctx, movies, "tmdb_scan")
			if mode == "stop_before_ai" {
				cancel()
			}
			accepted := a.processGeminiQueue(ctx, queue, a.aiClient)

			rows, err := a.db.GetAllMovies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			stored := make(map[string]storage.Movie)
			for _, m := range rows {
				stored[m.Filename] = m
			}
			aiMetrics := a.aiClient.CallMetrics()
			tmdbMetrics := a.tmdbClient.RequestMetrics()
			l2After := countAIResolutions(t, dir)
			cachedSearched := wire.count("/3/search/movie query=Cached Film") + wire.count("/3/search/tv query=Cached Show")
			t.Logf("R6A %s stored=%v queue=%v accepted=%v saveErr=%v aiBatches=%d aiInputs=%v searchHTTP=%d detailsHTTP=%d l2 %d→%d cachedTitleSearched=%d",
				mode, map[string]int{file: stored[file].TmdbID}, queue, accepted, saveErr != nil,
				aiMetrics.RecognitionBatch, aiProv.inputs, tmdbMetrics.SearchCalls, tmdbMetrics.DetailsCalls, l2Before, l2After, cachedSearched)

			// R6 інваріанти B: жодних читань/записів L2 у робочій логіці.
			// (Якщо свіжа AI-гіпотеза випадково збігається з L2-назвою, пошук цієї назви —
			// це верифікація нової гіпотези, а не читання кешу; тому режим із збігом звільнено.)
			if cachedSearched != 0 && aiProv.byFile[file] != l2Seed {
				t.Errorf("R6: історична L2-назва пішла в TMDB-пошук (%d разів)", cachedSearched)
			}
			if l2After != l2Before {
				t.Errorf("R6: ai_resolutions змінено робочою логікою: %d → %d", l2Before, l2After)
			}

			// Очікування B per mode.
			switch mode {
			case "fresh_no_l2":
				if stored[file].TmdbID != 201 || aiMetrics.RecognitionBatch != 1 || len(accepted) != 1 {
					t.Errorf("fresh: stored=%d ai=%d accepted=%v", stored[file].TmdbID, aiMetrics.RecognitionBatch, accepted)
				}
			case "l2_hit_divergent_ai":
				// B: свіжа AI-гіпотеза не верифікується → unresolved, але AI витрачено.
				if stored[file].TmdbID != 0 || aiMetrics.RecognitionBatch != 1 || len(queue) != 1 {
					t.Errorf("divergent: stored=%d ai=%d queue=%v", stored[file].TmdbID, aiMetrics.RecognitionBatch, queue)
				}
			case "l2_hit_same_as_ai":
				// Та сама identity, що й L2 у A, але ціною одного AI inference у B.
				if stored[file].TmdbID != 555 || aiMetrics.RecognitionBatch != 1 {
					t.Errorf("same_as_ai: stored=%d ai=%d", stored[file].TmdbID, aiMetrics.RecognitionBatch)
				}
			case "l2_stale_pipeline", "l2_low_confidence", "l2_wrong_year":
				if stored[file].TmdbID != 201 || aiMetrics.RecognitionBatch != 1 {
					t.Errorf("%s: stored=%d ai=%d", mode, stored[file].TmdbID, aiMetrics.RecognitionBatch)
				}
			case "l2_wrong_type":
				// B: AI-гіпотеза movie верифікується як movie:201; історичний tv-хвіст ігнорується.
				if stored[file].TmdbID != 201 || stored[file].MediaType != "movie" || aiMetrics.RecognitionBatch != 1 {
					t.Errorf("wrong_type: stored=%+v ai=%d", stored[file], aiMetrics.RecognitionBatch)
				}
			case "confirmed_protected":
				m := stored[file]
				if m.TmdbID != 900 || m.TitleUA != "Підтверджена назва" || m.Plot != "Підтверджений опис." ||
					!m.NeedsReview || m.ReviewReason != "fixture" || m.RecognitionSource != "manual_id" {
					t.Errorf("confirmed row damaged: %+v", m)
				}
				if aiMetrics.RecognitionBatch != 0 || len(queue) != 0 {
					t.Errorf("outage path must not reach AI: ai=%d queue=%v", aiMetrics.RecognitionBatch, queue)
				}
			case "tmdb_outage":
				if stored[file].TmdbID != 0 || aiMetrics.RecognitionBatch != 0 || len(queue) != 0 {
					t.Errorf("outage: stored=%d ai=%d queue=%v", stored[file].TmdbID, aiMetrics.RecognitionBatch, queue)
				}
			case "stop_before_ai":
				if len(queue) != 1 || aiMetrics.RecognitionBatch != 0 || len(accepted) != 0 {
					t.Errorf("stop: queue=%v ai=%d accepted=%v", queue, aiMetrics.RecognitionBatch, accepted)
				}
				if ctx.Err() != context.Canceled {
					t.Error("cancellation barrier did not fire")
				}
			case "commit_rollback":
				if _, ok := stored[file]; ok {
					t.Errorf("rolled-back row must not be committed: %+v", stored[file])
				}
				if len(accepted) != 0 {
					t.Errorf("uncommitted AI identity reported accepted: %v", accepted)
				}
			}
		})
	}
}

// TestR6RescanReplaySameDB — №18: повторний scan тієї самої тимчасової SQLite.
// A (R5): після втрати рядка movies історичний L2 відтворює identity без AI.
// B (R6): тем самий результат через свіжу AI-гіпотезу + TMDB-верифікацію, L2 не читається.
func TestR6RescanReplaySameDB(t *testing.T) {
	ctx := context.Background()
	wire := newR6Wire()
	wire.search["Alpha Hypothesis"] = `{"results":[{"id":201,"title":"Alpha Hypothesis","original_title":"Alpha Hypothesis","release_date":"2024-01-01"}]}`
	wire.details["/3/movie/201"] = `{"id":201,"title":"Українська назва","original_title":"Alpha Hypothesis","release_date":"2024-01-01","overview":"Український опис."}`
	aiProv := &r6AIProvider{byFile: map[string]string{"Alpha.2024.mkv": "Alpha Hypothesis"}}

	a, dir, cancel := setupR6App(t, nil, wire, aiProv)
	defer cancel()
	a.diskFileScanner = func(context.Context) ([]string, error) {
		return []string{filepath.Join(dir, "Alpha.2024.mkv")}, nil
	}

	a.RunScan()
	a.wg.Wait()
	first, err := a.db.GetMovieByFilename(ctx, "Alpha.2024.mkv")
	if err != nil || first == nil || first.TmdbID != 201 {
		t.Fatalf("first scan: %+v err=%v", first, err)
	}
	aiAfterFirst := a.aiClient.CallMetrics().RecognitionBatch
	l2AfterFirst := countAIResolutions(t, dir)

	// Імітуємо втрату рядка каталогу (файл повернувся на диск): L2 у A залишається.
	deleteMovieRow(t, dir, "Alpha.2024.mkv")

	a.RunScan()
	a.wg.Wait()
	second, err := a.db.GetMovieByFilename(ctx, "Alpha.2024.mkv")
	if err != nil || second == nil || second.TmdbID != 201 || second.MediaType != "movie" {
		t.Fatalf("rescan: %+v err=%v", second, err)
	}
	aiAfterSecond := a.aiClient.CallMetrics().RecognitionBatch // RunScan скидає метрики на старті
	l2AfterSecond := countAIResolutions(t, dir)
	cachedSearched := wire.count("/3/search/movie query=Cached Film")
	t.Logf("R6A rescan_replay firstID=%d aiAfterFirst=%d l2AfterFirst=%d | rescan ID=%d aiSecondRun=%d l2AfterSecond=%d cachedSearched=%d searchHTTP=%d detailsHTTP=%d",
		first.TmdbID, aiAfterFirst, l2AfterFirst, second.TmdbID, aiAfterSecond, l2AfterSecond, cachedSearched,
		a.tmdbClient.RequestMetrics().SearchCalls, a.tmdbClient.RequestMetrics().DetailsCalls)

	// R6 інваріанти B: повторний scan не читає й не пише L2; identity відновлено через AI+TMDB.
	if l2AfterFirst != 0 || l2AfterSecond != 0 {
		t.Errorf("R6: L2 written during scans: %d → %d", l2AfterFirst, l2AfterSecond)
	}
	if aiAfterSecond != 1 {
		t.Errorf("R6: rescan should spend exactly one fresh AI batch, got %d", aiAfterSecond)
	}
}

// r6CandidateFixture — опис фікстури R6-B.
type r6CandidateFixture struct {
	name        string
	current     *storage.Movie
	seedL2      string // порожній — без історичного L2
	l2PV        int
	manualTitle string
	// results — відповідь сервера за query (порожній масив, якщо немає ключа).
	results map[string]string
	// wantQueries — очікувана послідовність app-level запитів без L2 (всі виконуються, якщо сервер порожній).
	wantQueries []string
	wantIDs     []int
}

// TestR6CandidateSearchWithoutL2 — R6-B: джерела запитів редактора кандидатів.
// B: лише manual / stored_title (TitleEN, TitleUA) / filename / parent; історичний L2 не читається.
func TestR6CandidateSearchWithoutL2(t *testing.T) {
	const filename = "Dir/Name.2024.mkv"
	confirmed := &storage.Movie{Filename: filename, TmdbID: 100, MediaType: "movie", TitleEN: "Alpha", TitleUA: "Альфа", Year: "2024"}
	review := &storage.Movie{Filename: filename, TmdbID: 100, MediaType: "movie", TitleEN: "Alpha", TitleUA: "Альфа", Year: "2024", NeedsReview: true, ReviewReason: "fixture"}
	unresolved := &storage.Movie{Filename: filename, TmdbID: 0, TitleUA: "Unresolved: " + filename, TitleEN: "Unresolved: " + filename, NeedsReview: true, ReviewReason: "unresolved"}

	empty := map[string]string{}
	alphaHit := map[string]string{"Alpha": `{"results":[{"id":201,"title":"Alpha","original_title":"Alpha","release_date":"2024-01-01","popularity":10}]}`}
	manualHit := map[string]string{"Manual Title": `{"results":[{"id":202,"title":"Manual Title","original_title":"Manual Title","release_date":"2024-01-01","popularity":20}]}`}
	l2Hit := map[string]string{"Ghost Alias": `{"results":[{"id":999,"title":"Ghost Alias","original_title":"Ghost Alias","release_date":"2024-01-01"}]}`}
	fiveCap := map[string]string{"Name": `{"results":[{"id":301,"title":"Name 301","release_date":"2024-01-01","popularity":50},{"id":302,"title":"Name 302","release_date":"2024-01-01","popularity":40},{"id":303,"title":"Name 303","release_date":"2024-01-01","popularity":30},{"id":304,"title":"Name 304","release_date":"2024-01-01","popularity":20},{"id":305,"title":"Name 305","release_date":"2024-01-01","popularity":10},{"id":306,"title":"Name 306","release_date":"2024-01-01","popularity":5}]}`}

	fixtures := []r6CandidateFixture{
		{name: "l2_equals_titleEN", current: confirmed, seedL2: "Alpha", l2PV: 25, results: empty, wantQueries: []string{"Alpha", "Альфа", "Name", "Dir"}, wantIDs: nil},
		{name: "l2_equals_titleUA", current: confirmed, seedL2: "Альфа", l2PV: 25, results: empty, wantQueries: []string{"Alpha", "Альфа", "Name", "Dir"}, wantIDs: nil},
		{name: "l2_different_title", current: confirmed, seedL2: "Ghost Alias", l2PV: 25, results: empty, wantQueries: []string{"Alpha", "Альфа", "Name", "Dir"}, wantIDs: nil},
		{name: "l2_wrong_title", current: confirmed, seedL2: "Totally Wrong", l2PV: 25, results: empty, wantQueries: []string{"Alpha", "Альфа", "Name", "Dir"}, wantIDs: nil},
		{name: "l2_other_type_title", current: confirmed, seedL2: "Some Show", l2PV: 25, results: empty, wantQueries: []string{"Alpha", "Альфа", "Name", "Dir"}, wantIDs: nil},
		{name: "l2_stale_pipeline", current: confirmed, seedL2: "Ghost Alias", l2PV: 24, results: empty, wantQueries: []string{"Alpha", "Альфа", "Name", "Dir"}, wantIDs: nil},
		{name: "no_l2", current: confirmed, results: empty, wantQueries: []string{"Alpha", "Альфа", "Name", "Dir"}, wantIDs: nil},
		{name: "unresolved_no_l2", current: unresolved, results: empty, wantQueries: []string{"Name", "Dir"}, wantIDs: nil},
		{name: "unresolved_with_l2_only_source", current: unresolved, seedL2: "Ghost Alias", l2PV: 25, results: l2Hit, wantQueries: []string{"Name", "Dir"}, wantIDs: nil},
		{name: "manual_title_first", current: unresolved, seedL2: "Ghost Alias", l2PV: 25, manualTitle: "Manual Title", results: manualHit, wantQueries: []string{"Manual Title"}, wantIDs: []int{202}},
		{name: "confirmed_needs_review_current_first", current: review, seedL2: "Ghost Alias", l2PV: 25, results: alphaHit, wantQueries: []string{"Alpha"}, wantIDs: []int{100, 201}},
		{name: "five_cap_deterministic", current: nil, seedL2: "Ghost Alias", l2PV: 25, results: fiveCap, wantQueries: []string{"Name"}, wantIDs: []int{301, 302, 303, 304, 305}},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			ctx := context.Background()
			var mu sync.Mutex
			var gotQueries []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				query := r.URL.Query().Get("query")
				mu.Lock()
				if len(gotQueries) == 0 || gotQueries[len(gotQueries)-1] != query {
					gotQueries = append(gotQueries, query)
				}
				mu.Unlock()
				// Хіти лише на movie-endpoint, щоб movie/tv не дублювали кандидата.
				if body, ok := fx.results[query]; ok && r.URL.Path == "/3/search/movie" {
					io.WriteString(w, body)
					return
				}
				io.WriteString(w, `{"results":[]}`)
			}))
			defer server.Close()
			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}

			dbDir := t.TempDir()
			db, err := storage.New(filepath.Join(dbDir, "movies.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.InitSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if fx.current != nil {
				if err := db.SaveMoviesBatch(ctx, []storage.Movie{*fx.current}); err != nil {
					t.Fatal(err)
				}
			}
			if fx.seedL2 != "" {
				seedHistoricalL2(t, dbDir, filename, fx.seedL2, 2024, "movie", 0.99, fx.l2PV)
			}

			cfg := &config.Config{TMDBAPIKey: "test", PostersDir: t.TempDir()}
			client := tmdb.NewClient(cfg)
			defer client.Close()
			client.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
			app := NewApp()
			app.ctx, app.db, app.tmdbClient = ctx, db, client

			got, err := app.SearchTMDBCandidates(CandidateSearchRequest{Filename: filename, Title: fx.manualTitle, MediaType: "auto"})
			if err != nil {
				t.Fatal(err)
			}
			var ids []int
			for _, c := range got {
				ids = append(ids, c.TMDBID)
			}
			mu.Lock()
			rawQueries := append([]string(nil), gotQueries...)
			mu.Unlock()

			// Фільтруємо транспортний шум транслітерацій: залишаємо лише app-level запити з очікуваного набору.
			allowed := map[string]bool{}
			for _, q := range fx.wantQueries {
				allowed[q] = true
			}
			var queries []string
			for _, q := range rawQueries {
				if allowed[q] {
					queries = append(queries, q)
				}
			}
			t.Logf("R6B %s rawQueries=%v filtered=%v candidates=%v l2Rows=%d", fx.name, rawQueries, queries, ids, countAIResolutions(t, dbDir))

			// R6-B інваріант: L2-only назва не є запитом.
			if fx.seedL2 != "" && !allowed[fx.seedL2] {
				for _, q := range rawQueries {
					if q == fx.seedL2 {
						t.Errorf("R6-B: історична L2-назва %q пішла в пошук кандидатів: %v", q, rawQueries)
					}
				}
			}
			if !slices.Equal(queries, fx.wantQueries) {
				t.Errorf("queries=%v, want %v (raw=%v)", queries, fx.wantQueries, rawQueries)
			}
			if !slices.Equal(ids, fx.wantIDs) {
				t.Errorf("candidate ids=%v, want %v", ids, fx.wantIDs)
			}
			if len(got) > 5 {
				t.Errorf("candidate cap violated: %d", len(got))
			}
		})
	}
}
