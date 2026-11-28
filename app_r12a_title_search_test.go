package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

type r12aFixture struct {
	name, filename, parent, variant string
	works                           []r10bWork // Search results and details, independent of truth.
	truth                           r10bIdentity
	resultA, resultB                r10bIdentity
	primary, fault                  string
	ai, extraSearch                 int // AI HTTP on a clean miss, extra search HTTP on B.
	repeat, confirmed, poster       bool
}

func r12aFixtures() []r12aFixture {
	movie := r10bMovie(1551025)
	tv := r10bIdentity{"tv", 1551025}
	guarding := r10bWork{id: movie, year: 2026, original: "Guarding Stars", localized: "Охорона зірок"}
	meaning := r10bWork{id: r10bMovie(12001), year: 2026, original: "Все и ничего", localized: "Усе й нічого"}
	other := r10bWork{id: r10bMovie(12002), year: 2026, original: meaning.original, localized: meaning.localized}
	return []r12aFixture{
		{name: "guarding_stars", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", works: []r10bWork{guarding}, truth: movie, resultB: movie, ai: 2, extraSearch: 1, poster: true},
		{name: "guarding_miss", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", truth: movie, ai: 2, extraSearch: 4, repeat: true},
		{name: "without_yo", filename: "Охрана звезд.2026.mkv", ai: 2},
		{name: "primary_success", filename: "Охрана звёзд.2026.mkv", primary: "Охрана звёзд", variant: "Охрана звезд", works: []r10bWork{guarding}, truth: movie, resultA: movie, resultB: movie, repeat: true},
		{name: "multiple_upper_mixed", filename: "Ёлка и ёЖ.2026.mkv", variant: "Елка и еЖ", ai: 2, extraSearch: 4},
		{name: "unknown_year", filename: "Охрана звёзд.1080p.mkv", variant: "Охрана звезд", ai: 2, extraSearch: 2},
		{name: "typed_tv_same_numeric_id", filename: "Охрана звёзд.2026.S01E01.mkv", parent: "Movies", variant: "Охрана звезд", works: []r10bWork{guarding, {id: tv, year: 2026, original: "TV Guarding Stars", localized: "Серіал про зірки"}}, truth: tv, resultB: tv, ai: 2, extraSearch: 1},
		{name: "meaning_collision", filename: "Всё и ничего.2026.mkv", variant: "Все и ничего", works: []r10bWork{meaning}, truth: r10bMovie(12003), resultB: meaning.id, ai: 2, extraSearch: 1},
		{name: "meaning_original", filename: "Все и ничего.2026.mkv", primary: "Все и ничего", works: []r10bWork{meaning}, truth: meaning.id, resultA: meaning.id, resultB: meaning.id},
		{name: "ambiguous_same_year", filename: "Всё и ничего.2026.mkv", variant: "Все и ничего", works: []r10bWork{meaning, other}, truth: other.id, resultB: meaning.id, ai: 2, extraSearch: 1},
		{name: "details_wrong_id", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", works: []r10bWork{guarding}, truth: movie, fault: "id", ai: 2, extraSearch: 4},
		{name: "details_wrong_type", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", works: []r10bWork{guarding}, truth: movie, fault: "type", ai: 2, extraSearch: 4},
		{name: "details_wrong_year_without_query_year", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", works: []r10bWork{guarding}, truth: movie, fault: "year", ai: 2, extraSearch: 3},
		{name: "alias_lookup", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", works: []r10bWork{{id: r10bMovie(12005), year: 2026, original: "Other Planet"}}, ai: 2, extraSearch: 4},
		{name: "partial_http_error", filename: "Охрана звёзд.2026.mkv", primary: "Охрана звёзд", variant: "Охрана звезд", fault: "500", extraSearch: 4},
		{name: "outage", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", fault: "outage", extraSearch: 4},
		{name: "canceled", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", fault: "cancel"},
		{name: "stop_between_attempts", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", fault: "stop"},
		{name: "confirmed", filename: "Охрана звёзд.2026.mkv", variant: "Охрана звезд", truth: movie, resultA: movie, resultB: movie, confirmed: true},
		{name: "named_parent", filename: "Охрана звёзд.2026.mkv", parent: "Далека планета", variant: "Охрана звезд", ai: 2, extraSearch: 4},
		{name: "parent_already_tried_variant", filename: "Охрана звёзд.2026.mkv", parent: "Охрана звезд.2026", variant: "Охрана звезд", ai: 2, extraSearch: 2},
		{name: "generic_parent", filename: "Охрана звёзд.2026.mkv", parent: "Фильмы", variant: "Охрана звезд", ai: 2, extraSearch: 4},
		{name: "grouped_parent_with_yo", filename: "Signal.2026.S01E01.mkv", parent: "Звёздный путь.2026", ai: 2},
		{name: "latin_transliteration", filename: "Ella Makkej.2026.mkv", ai: 2},
		{name: "imdb_first", filename: "Охрана звёзд.2026.tt1234567.mkv", variant: "Охрана звезд", works: []r10bWork{guarding}, truth: movie, resultA: movie, resultB: movie},
	}
}

type r12aObservation struct {
	Name, Group, SearchTitle                            string
	Truth                                               string
	Correct, False, Unresolved, Absent, Review          int
	Search, Details, Aliases, Posters, Find, AI, Commit int
	VariantTitles, VariantAttempts                      int
	Runs                                                int
	Rows                                                []storage.Movie
	Wire                                                []string
}

func TestR12ATitleSearchAB(t *testing.T) {
	variant := os.Getenv("MOVIELIST_R12A_VARIANT")
	if variant == "" {
		t.Skip("Run testdata/r12a/compare.ps1 to compare the baseline and the experimental overlay")
	}
	if variant != "A" && variant != "B" {
		t.Fatal("unexpected R12-A variant", variant)
	}
	for _, f := range r12aFixtures() {
		t.Run(f.name, func(t *testing.T) {
			r12aRunFixture(t, f, variant == "B")
		})
	}
	t.Run("parallel_similar_files", func(t *testing.T) {
		r12aParallelSimilarFiles(t, variant == "B")
	})
}

func r12aParallelSimilarFiles(t *testing.T, useVariant bool) {
	a, dir := newTestAppDB(t, nil)
	a.cfg.MediaFolderPath, a.cfg.PostersDir = dir, t.TempDir()
	a.cfg.TMDBAPIKey = "fixture"
	a.tmdbClient = tmdb.NewClient(a.cfg)
	t.Cleanup(a.tmdbClient.Close)
	var f r12aFixture
	for _, item := range r12aFixtures() {
		if item.name == "meaning_original" {
			f = item
		}
	}
	obs := r12aObservation{}
	var mu sync.Mutex
	handler := r12aTMDB(t, f, &obs, nil, nil)
	rec := &r9bRecorder{}
	a.tmdbClient.SetTransport(rec.roundTrip(func(r *http.Request) r9bResp {
		mu.Lock()
		defer mu.Unlock()
		return handler(r)
	}))
	source := "Всё и ничего.2026.mkv"
	paths := []string{filepath.Join(dir, source), filepath.Join(dir, f.filename)}
	outcomes := make(map[string]int)
	for res := range a.runTMDBScan(t.Context(), paths) {
		outcomes[res.fname] = 0
		if res.tmdbFailed {
			t.Errorf("unexpected TMDB error for %q", res.fname)
		}
		if res.info != nil {
			outcomes[res.fname] = res.info.TMDBID
		}
	}
	wantSource, wantDetails := 0, 1
	if useVariant {
		wantSource, wantDetails = f.truth.id, 2 // The separate filename oracle marks this as a false positive.
	}
	if len(outcomes) != 2 || outcomes[source] != wantSource || outcomes[f.filename] != f.truth.id || obs.Details != wantDetails {
		t.Fatalf("parallel file outcomes=%v, details=%d, want %d/%d, details=%d", outcomes, obs.Details, wantSource, f.truth.id, wantDetails)
	}
}

func r12aRunFixture(t *testing.T, f r12aFixture, useVariant bool) {
	want := f.resultA
	if useVariant {
		want = f.resultB
	}
	a, dir := newTestAppDB(t, nil)
	a.cfg.MediaFolderPath, a.cfg.PostersDir = dir, t.TempDir()
	a.cfg.TMDBAPIKey, a.cfg.GeminiAPIKey = "fixture", "fixture"
	a.cfg.GeminiModels = []string{"gemini-r12a-flash", "gemini-r12a-pro"}
	a.tmdbClient, a.aiClient = tmdb.NewClient(a.cfg), ai.NewClient(a.cfg)
	t.Cleanup(a.tmdbClient.Close)
	path := filepath.Join(dir, f.parent, f.filename)
	key := a.getFileIdentifier(path)
	if f.confirmed {
		if err := a.db.SaveMoviesBatch(t.Context(), []storage.Movie{{Filename: key, MediaType: f.truth.kind, TmdbID: f.truth.id, TitleEN: "Previously confirmed"}}); err != nil {
			t.Fatal(err)
		}
	}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var primary, semantic atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "gemini-r12a-flash:generateContent":
			primary.Add(1)
		case "gemini-r12a-pro:generateContent":
			semantic.Add(1)
		default:
			t.Errorf("unexpected Gemini path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(r.URL.Path, "-pro:") && !strings.Contains(string(body), "low_confidence") {
			t.Error("semantic retry lost the primary rejection reason")
		}
		row := map[string]any{"id": 0, "request_id": "recognition-0", "original_file": f.filename, "en_title": "Low Confidence Guess", "media_type": "movie", "status": "resolved", "confidence": 0.2}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, r10bJSON([]map[string]any{row}))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a.scanCancel = cancel
	obs := r12aObservation{Name: f.name, Group: "with_yo", Truth: f.truth.String()}
	if !strings.ContainsAny(f.filename, "ёЁ") {
		obs.Group = "without_yo"
	}
	handler := r12aTMDB(t, f, &obs, cancel, a.StopScan)
	rec := &r9bRecorder{}
	a.tmdbClient.SetTransport(rec.roundTrip(handler))
	run := func() {
		files := a.filterUnprocessed(ctx, []string{path})
		if len(files) == 0 {
			return
		}
		obs.Runs++
		scan := a.recognizeAutomaticScan(ctx, files)
		wantAccepted := 0
		if want.id > 0 {
			wantAccepted = 1
		}
		if scan.tmdbAccepted != wantAccepted || scan.aiAccepted != 0 {
			t.Errorf("unexpected coordinator result %+v", scan)
		}
	}
	run()
	if f.repeat {
		run()
	}

	rows, err := a.db.GetAllMovies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	obs.Rows = rows
	if f.fault == "cancel" || f.fault == "stop" {
		if len(rows) != 0 || ctx.Err() != context.Canceled || a.db.GetState(t.Context(), "last_scan_at") != "" {
			t.Fatal("cancellation failed or changed SQLite")
		}
		obs.Absent = 1
	} else {
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		row := &rows[0]
		if row.LocalPosterPath != "" {
			row.LocalPosterPath = filepath.Base(row.LocalPosterPath)
		}
		got := r10bIdentity{row.MediaType, row.TmdbID}
		if row.Filename != key || got != want {
			t.Fatalf("row %q/%s, want %q/%s; parsed=%+v; wire=%v", row.Filename, got, key, want, tmdb.ParseFilename(path), rec.snapshot())
		}
		switch {
		case got.id == 0:
			obs.Unresolved = 1
			if !row.NeedsReview || row.ReviewReason != "unresolved" {
				t.Errorf("wrong unresolved review state: %+v", row)
			}
		case got == f.truth:
			obs.Correct = 1
		default:
			obs.False = 1
		}
		if row.NeedsReview {
			obs.Review = 1
		}
		if f.name == "ambiguous_same_year" && useVariant && (!row.NeedsReview || row.ReviewReason != "ambiguous_exact") {
			t.Errorf("ambiguous identity lost its review flag: %+v", row)
		}
		if f.name == "meaning_collision" && useVariant && (row.NeedsReview || row.ReviewReason != "") {
			t.Errorf("expected to reproduce a false positive without a review flag: %+v", row)
		}
	}
	obs.AI = int(primary.Load() + semantic.Load())
	seen := make(map[string]bool)
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Message string `json:"msg"`
			Trace   string `json:"trace_id"`
			Label   string `json:"label"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Message == "batch_save_success" {
			obs.Commit++
		}
		if entry.Message == "search_attempt" && strings.HasPrefix(entry.Label, "ё→е") {
			seen[entry.Trace] = true
			obs.VariantAttempts++
		}
	}
	obs.VariantTitles = len(seen)
	obs.Wire = rec.snapshot()
	// Probe the same fixture separately for SearchTitle, which SQLite does not store.
	// The accounting above refers only to the original coordinator runs.
	if want.id > 0 && !f.confirmed {
		probe := tmdb.NewClient(a.cfg)
		defer probe.Close()
		probeRec := &r9bRecorder{}
		probe.SetTransport(probeRec.roundTrip(r12aTMDB(t, f, &r12aObservation{}, cancel, a.StopScan)))
		info, err := probe.FetchFromFilename(t.Context(), path)
		if err != nil || info == nil || info.TMDBID != want.id || string(info.MediaType) != want.kind {
			t.Fatalf("SearchTitle probe failed: %+v, %v", info, err)
		}
		obs.SearchTitle = info.SearchTitle
	}

	wantAI := f.ai * obs.Runs
	if want.id > 0 || (useVariant && (f.fault == "id" || f.fault == "type")) {
		wantAI = 0
	}
	if obs.AI != wantAI || int(primary.Load()) != wantAI/2 || int(semantic.Load()) != wantAI/2 {
		t.Errorf("Gemini primary/semantic = %d/%d, want %d/%d", primary.Load(), semantic.Load(), wantAI/2, wantAI/2)
	}
	if wantCommits := obs.Runs - obs.Absent; obs.Commit != wantCommits {
		t.Errorf("commits = %d, want %d", obs.Commit, wantCommits)
	}
	if obs.Absent != 0 && obs.Search != map[string]int{"cancel": 1, "stop": 2}[f.fault] {
		t.Errorf("TMDB continued after %s: %v", f.fault, obs.Wire)
	}
	r12aCheckVariantRequests(t, f, obs, useVariant)
	t.Logf("R12A %s", r10bJSON(obs))
}

func r12aTMDB(t *testing.T, f r12aFixture, obs *r12aObservation, cancel, stop func()) func(*http.Request) r9bResp {
	empty := r9bOK(`{"results":[]}`)
	return func(r *http.Request) r9bResp {
		p, params := r.URL.Path, r.URL.Query()
		switch {
		case strings.HasPrefix(p, "/3/find/"):
			obs.Find++
			return r9bOK(`{"movie_results":[{"id":1551025}],"tv_results":[]}`)
		case strings.HasPrefix(p, "/3/search/"):
			obs.Search++
			query := params.Get("query")
			kind := filepath.Base(p)
			if f.fault == "cancel" || (f.fault == "stop" && obs.Search == 2) {
				if f.fault == "cancel" {
					cancel()
				} else {
					stop()
				}
			}
			if f.fault == "outage" {
				return r9bResp{status: 404}
			}
			if f.fault == "500" && query == f.primary && params.Get("year") != "" && params.Get("language") == "uk-UA" {
				return r9bOutage()
			}
			if query != f.primary && query != f.variant || query == "" {
				return empty
			}
			if f.fault == "year" && params.Get("year") != "" {
				return empty
			}
			results := make([]map[string]any, 0, len(f.works))
			for _, w := range f.works {
				if w.id.kind != kind {
					continue
				}
				title := query
				if f.name == "alias_lookup" {
					title = w.original
				}
				results = append(results, w.payload(title))
			}
			return r9bOK(string(r10bJSON(map[string]any{"results": results})))
		case strings.HasSuffix(p, "/alternative_titles"):
			obs.Aliases++
			return r9bOK(`{"titles":[],"results":[]}`)
		case strings.HasSuffix(p, "/r12a.jpg"):
			obs.Posters++
			return r9bResp{status: 200, body: "fixture-poster"}
		}
		obs.Details++
		for _, w := range f.works {
			if p != fmt.Sprintf("/3/%s/%d", w.id.kind, w.id.id) {
				continue
			}
			body := w.payload(w.localized)
			body["overview"] = "Український опис цієї історії."
			if f.poster {
				body["poster_path"] = "/r12a.jpg"
			}
			switch f.fault {
			case "id":
				body["id"] = w.id.id + 1
			case "type":
				body["media_type"] = "tv"
			case "year":
				body["release_date"] = "2012-01-01"
			}
			return r9bOK(string(r10bJSON(body)))
		}
		t.Errorf("unexpected TMDB endpoint %s", p)
		return r9bResp{status: 404}
	}
}

func r12aCheckVariantRequests(t *testing.T, f r12aFixture, o r12aObservation, useVariant bool) {
	t.Helper()
	if f.variant == "" {
		if o.VariantTitles != 0 || o.VariantAttempts != 0 {
			t.Error("unexpected additional title")
		}
		return
	}
	var requests []string
	for _, call := range o.Wire {
		if !strings.HasPrefix(call, "GET /3/search/") {
			continue
		}
		endpoint, rawQuery, _ := strings.Cut(call, "?")
		params, err := url.ParseQuery(rawQuery)
		if err != nil {
			t.Fatal(err)
		}
		if params.Get("query") == f.variant {
			requests = append(requests, call)
			kind := "movie"
			if f.truth.kind == "tv" {
				kind = "tv"
			}
			if endpoint != "GET /3/search/"+kind || (params.Get("language") != "uk-UA" && params.Get("language") != "ru-RU") {
				t.Errorf("unexpected variant endpoint/language: %s", call)
			}
			if f.name == "unknown_year" && (params.Has("year") || params.Has("first_air_date_year")) {
				t.Errorf("year parameter for file with unknown year: %s", call)
			}
		}
	}
	want := 0
	if f.name == "parent_already_tried_variant" {
		want = 2
	}
	if useVariant {
		want += f.extraSearch * o.Runs
	}
	if len(requests) != want {
		t.Errorf("variant search HTTP: got %v, want %d", requests, want)
	}
	if useVariant && f.extraSearch > 0 {
		limit := 2
		if f.name == "unknown_year" || f.name == "parent_already_tried_variant" {
			limit = 1
		}
		if o.VariantTitles != o.Runs || o.VariantAttempts > limit*o.Runs {
			t.Errorf("variant budget exceeded or no variant claimed: %d titles, %d attempts for %d runs", o.VariantTitles, o.VariantAttempts, o.Runs)
		}
	} else if o.VariantTitles != 0 || o.VariantAttempts != 0 {
		t.Error("unexpected additional title")
	}
	if o.Runs == 1 && len(requests) > 1 {
		for i := range requests {
			if slices.Contains(requests[:i], requests[i]) {
				t.Errorf("duplicate search attempt %s", requests[i])
			}
		}
	}
}
