package tmdb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/time/rate"
)

func TestRankResultsInfoLogHasSummaryWithoutCandidateDebugSpam(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(previous)
	c := &Client{}
	results := []tmdbSearchResult{
		{ID: 1, Title: "Dune", OriginalTitle: "Dune", ReleaseDate: "2021-01-01", MediaType: "movie"},
		{ID: 2, Title: "Dune", OriginalTitle: "Dune", ReleaseDate: "2024-01-01", MediaType: "movie"},
	}
	best := c.rankResults(context.Background(), results, "Dune", 2021, MediaTypeMovie)
	if best == nil {
		t.Fatal("expected winner")
	}
	logText := output.String()
	if strings.Contains(logText, "candidate_evaluated") || strings.Contains(logText, "best_candidate_rejected") {
		t.Fatalf("DEBUG candidate noise leaked at INFO: %s", logText)
	}
}

func TestMatchScore(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		resTitle string
		resOrig  string
		minScore int // Очікуємо бал >= вказаного
	}{
		{"Точний збіг", "rick and morty", "rick and morty", "rick and morty", ScoreExactMatch},
		{"Contains збіг", "batman", "the batman begins", "batman begins", ScoreContainsMatch},
		{"Штраф за короткі (<=3)", "it", "it follows", "it", ScoreExactMatch}, // Exact працює
		{"Відхилення коротких без exact", "it", "split", "split", 0},          // Fuzzy/Contains вимкнено для коротких
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchScore(tt.query, tt.resTitle, tt.resOrig)
			if got < tt.minScore {
				t.Errorf("matchScore() = %v, очікувано мінімум %v", got, tt.minScore)
			}
		})
	}
}

func TestScoreResult_DiamondMatch(t *testing.T) {
	c := &Client{
		client:      &http.Client{},
		rateLimiter: rate.NewLimiter(rate.Inf, 1),
	} // Порожній клієнт з ініціалізованими лімітерами для безпечного фейлу запитів
	ctx := context.Background()

	res := tmdbSearchResult{
		ID:            1,
		Title:         "Inception",
		OriginalTitle: "Inception",
		ReleaseDate:   "2010-07-15",
		MediaType:     "movie",
	}

	// 1. Перевіряємо Діамантовий Збіг (назва + точний рік)
	scored := c.scoreResult(ctx, res, 0, "inception", 2010, MediaTypeMovie)

	// ExactMatch(200) + Diamond(300) + YearExact(150) + TypeMatch(30) = 680
	if scored.score < 500 {
		t.Errorf("Diamond match failed, score is too low: %d", scored.score)
	}

	// 2. Перевіряємо жорстке відхилення сміття
	scoredTrash := c.scoreResult(ctx, res, 0, "batman", 2010, MediaTypeMovie)
	if scoredTrash.score != -1000 {
		t.Errorf("Сміттєвий результат не був жорстко відхилений. Score: %d", scoredTrash.score)
	}
}

func TestBuildAttemptsSkipsParentFolderAtScanRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Gluck.1925.mkv")
	parsed := ParseFilename(file)

	attempts := buildAttempts(parsed, file, root)

	for _, attempt := range attempts {
		if attempt.label == "Папка" {
			t.Fatalf("root folder fallback should be skipped, got query %q", attempt.query)
		}
	}
}

func TestBuildAttemptsFallbackPreservesTargetYear(t *testing.T) {
	parsed := ParsedFile{CleanTitle: "Master i Margarita", Year: 2023, MediaType: MediaTypeMovie}
	attempts := buildAttempts(parsed, "Master.i.Margarita.2023.mkv", "")
	var foundWithYear, foundWithoutYear bool
	for _, attempt := range attempts {
		if attempt.label == "Базовий+рік" {
			foundWithYear = attempt.queryYear == 2023 && attempt.targetYear == 2023
		}
		if attempt.label == "Базовий без року" {
			foundWithoutYear = attempt.queryYear == 0 && attempt.targetYear == 2023
		}
	}
	if !foundWithYear || !foundWithoutYear {
		t.Fatalf("year attempts incorrect: with=%v without=%v attempts=%+v", foundWithYear, foundWithoutYear, attempts)
	}
}

func TestSearchCacheSeparatesQueryYearFromTargetYear(t *testing.T) {
	withAPIYear := SearchCacheKey{query: "enemy", queryYear: 2013, targetYear: 2013, mediaType: MediaTypeMovie}
	withoutAPIYear := SearchCacheKey{query: "enemy", queryYear: 0, targetYear: 2013, mediaType: MediaTypeMovie}
	if withAPIYear == withoutAPIYear {
		t.Fatal("year-filtered and no-year fallback must not share a cache entry")
	}
}

func TestManualCandidateUsesPopularityToResolveExactTitleCollision(t *testing.T) {
	norm := normalizeForCompare("Enemy")
	correct, _, ok := manualCandidateScore(tmdbSearchResult{
		ID: 181886, Title: "Enemy", OriginalTitle: "Enemy", ReleaseDate: "2014-03-14", MediaType: "movie", Popularity: 25,
	}, norm, 2013, MediaTypeMovie)
	if !ok {
		t.Fatal("exact original title was not accepted")
	}
	wrong, _, ok := manualCandidateScore(tmdbSearchResult{
		ID: 103663, Title: "Enemy", OriginalTitle: "Shatru", ReleaseDate: "2013-08-23", MediaType: "movie", Popularity: 2,
	}, norm, 2013, MediaTypeMovie)
	if !ok || correct <= wrong {
		t.Fatalf("authoritative title chose localized collision: correct=%d wrong=%d", correct, wrong)
	}
}

func TestSearchExactTitleStrictTVSkipsMovieEndpoint(t *testing.T) {
	movieCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/movie"):
			movieCalled = true
			w.Write([]byte(`{"results":[]}`))
		case strings.HasSuffix(r.URL.Path, "/search/tv"):
			w.Write([]byte(`{"results":[{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27","popularity":35}]}`))
		case strings.HasSuffix(r.URL.Path, "/tv/62476"):
			w.Write([]byte(`{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27"}`))
		case strings.Contains(r.URL.Path, "/movie/802663"):
			w.Write([]byte(`{"id":802663,"title":"The Bureau","original_title":"The Bureau","release_date":"2020-01-01"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1), postersDir: t.TempDir()}
	info, err := c.SearchExactTitle(context.Background(), "The Bureau", 2015, MediaTypeTV, true, "Bureau.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if movieCalled || info == nil || info.TMDBID != 62476 || info.MediaType != MediaTypeTV {
		t.Fatalf("movieCalled=%v info=%+v", movieCalled, info)
	}
}

func TestFetchFromParsedIMDbBypassesTitleCache(t *testing.T) {
	findCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/find/tt1234567"):
			findCalled = true
			w.Write([]byte(`{"movie_results":[{"id":2}],"tv_results":[]}`))
		case strings.HasSuffix(r.URL.Path, "/movie/2"):
			w.Write([]byte(`{"id":2,"title":"Правильний фільм","original_title":"Correct Movie","release_date":"2020-01-01"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1), postersDir: t.TempDir(), apiKey: "test"}
	key := SearchCacheKey{query: "collision", queryYear: 2020, targetYear: 2020, mediaType: MediaTypeMovie}
	c.searchCache.Store(key, &MovieInfo{TMDBID: 1, TitleEN: "Wrong Cached Movie", MediaType: MediaTypeMovie})
	got, err := c.FetchFromParsed(context.Background(), ParsedFile{CleanTitle: "Collision", Year: 2020, MediaType: MediaTypeMovie, TitleLang: TitleLangLatin, IMDBID: "tt1234567"}, "Collision.tt1234567.2020.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if !findCalled || got == nil || got.TMDBID != 2 {
		t.Fatalf("IMDb lookup did not bypass title cache: findCalled=%v info=%+v", findCalled, got)
	}
}

func TestSetMediaRootUpdatesFolderFallbackContext(t *testing.T) {
	c := &Client{mediaRoot: "old"}
	c.SetMediaRoot("new")
	if got := c.mediaRootPath(); got != "new" {
		t.Fatalf("media root = %q; want new", got)
	}
}

func TestSearchCandidatesAutoTypedRankedAndLimited(t *testing.T) {
	var detailsCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/movie"):
			w.Write([]byte(`{"results":[{"id":802663,"title":"The Bureau","original_title":"The Bureau","release_date":"2015-01-01","popularity":1},{"id":802663,"title":"The Bureau","release_date":"2015-01-01"},{"id":3,"title":"Bureau 3"},{"id":4,"title":"Bureau 4"},{"id":5,"title":"Bureau 5"}]}`))
		case strings.HasSuffix(r.URL.Path, "/search/tv"):
			w.Write([]byte(`{"results":[{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27","popularity":35},{"id":6,"name":"Bureau 6"}]}`))
		default:
			detailsCalls++
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	got, err := c.SearchCandidates(context.Background(), "The Bureau", 2015, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0].TMDBID != 62476 || got[0].MediaType != MediaTypeTV {
		t.Fatalf("candidates=%+v", got)
	}
	if detailsCalls != 0 {
		t.Fatalf("preview made %d details calls", detailsCalls)
	}
	seen := map[string]bool{}
	for _, item := range got {
		key := fmt.Sprintf("%s:%d", item.MediaType, item.TMDBID)
		if seen[key] {
			t.Fatalf("duplicate %s", key)
		}
		seen[key] = true
	}
}

func TestSearchCandidatesRetriesTransliteratedFilenameWhenDirectSearchIsEmpty(t *testing.T) {
	queries := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query().Get("query")
		queries = append(queries, query)
		if query == latinToCyrillic("Ebigejl") && strings.HasSuffix(r.URL.Path, "/search/movie") {
			w.Write([]byte(`{"results":[{"id":1111873,"title":"Ебіґейл","original_title":"Abigail","release_date":"2024-04-18","popularity":50}]}`))
			return
		}
		w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	got, err := c.SearchCandidates(context.Background(), "Ebigejl", 2024, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TMDBID != 1111873 {
		t.Fatalf("candidates=%+v queries=%v", got, queries)
	}
	if len(queries) != 4 || queries[0] != "Ebigejl" || queries[2] != latinToCyrillic("Ebigejl") {
		t.Fatalf("queries=%v", queries)
	}
}

func TestLatinToCyrillicSuffixCorrections(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Zhertva obstoyatelstv", "Жертва обстоятельств"},
		{"Dokazatelstvo", "Доказательство"},
		{"Uchitelskiy", "Учительский"},
		{"Vrag", "Враг"},
	}
	for _, tt := range tests {
		got := latinToCyrillic(tt.input)
		if got != tt.expected {
			t.Errorf("latinToCyrillic(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSearchCandidatesTransliteratedFallsBackToRussianWhenUkrainianIsEmpty(t *testing.T) {
	languages := make([]string, 0, 6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lang := r.URL.Query().Get("language")
		languages = append(languages, lang)
		if lang == "ru-RU" && strings.HasSuffix(r.URL.Path, "/search/movie") {
			w.Write([]byte(`{"results":[{"id":1284186,"title":"Жертва обстоятельств","original_title":"Sacrifice","release_date":"2026-04-09","popularity":10}]}`))
			return
		}
		w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	got, err := c.SearchCandidates(context.Background(), "Zhertva obstoyatelstv", 2025, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TMDBID != 1284186 {
		t.Fatalf("expected candidate 1284186, got=%+v", got)
	}
	hasRu := false
	for _, lang := range languages {
		if lang == "ru-RU" {
			hasRu = true
			break
		}
	}
	if !hasRu {
		t.Fatalf("expected ru-RU fallback in languages, got: %v", languages)
	}
}

func TestSearchCandidatesProductionTransliterations(t *testing.T) {
	tests := []struct {
		title string
		year  int
		id    int
		cyr   string
		name  string
	}{
		{"Tretyi lishnyi", 2012, 72105, "Третий лишний", "Ted"},
		{"Dorozhnoe prikljuchenie", 2000, 9285, "Дорожное приключение", "Road Trip"},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("query") == tt.cyr && strings.HasSuffix(r.URL.Path, "/search/movie") {
					fmt.Fprintf(w, `{"results":[{"id":%d,"title":%q,"original_title":%q,"release_date":%q}]}`, tt.id, tt.cyr, tt.name, fmt.Sprintf("%d-01-01", tt.year))
					return
				}
				w.Write([]byte(`{"results":[]}`))
			}))
			defer server.Close()
			c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
			got, err := c.SearchCandidates(context.Background(), tt.title, tt.year, "auto")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].TMDBID != tt.id || !got[0].Exact {
				t.Fatalf("candidates=%+v", got)
			}
		})
	}
}

func TestGenerateTitleCandidatesContextualPartOne(t *testing.T) {
	got := generateTitleCandidates("Dorozhnoe prikljuchenie 1", "Dorozhnoe.prikljuchenie.1.2000.XviD.HDTVRip.avi")
	found := false
	for _, candidate := range got {
		if candidate == "Dorozhnoe prikljuchenie" {
			found = true
		}
		if strings.Contains(strings.ToLower(candidate), "xvid") {
			t.Fatalf("release noise leaked: %v", got)
		}
	}
	if !found {
		t.Fatalf("missing contextual no-part candidate: %v", got)
	}
	for _, fixture := range []string{"Formula.1.2025.mkv", "District.9.2009.mkv", "1917.2019.mkv"} {
		parsed := ParseFilename(fixture)
		if parsed.CleanTitle == "" || (fixture != "1917.2019.mkv" && !strings.Contains(parsed.CleanTitle, strings.Fields(strings.ReplaceAll(fixture, ".", " "))[1])) {
			t.Fatalf("numeric title damaged: %s => %+v", fixture, parsed)
		}
	}
}

func TestGenerateTitleCandidatesRemovesProductionReleaseGroups(t *testing.T) {
	tests := []struct {
		parsed   string
		filename string
		want     string
		noise    []string
	}{
		{
			parsed:   "Tretyi lishnyi",
			filename: "Tretyi_lishnyi_2012_BDRip_AVO_[TC]_by_Dalemake.avi",
			want:     "Tretyi lishnyi",
			noise:    []string{"bdrip", "avo", "tc", "by", "dalemake"},
		},
		{
			parsed:   "Obrazcovyj Samec",
			filename: "Obrazcovyj.Samec.2001.RUS.BDRip.XviD.AC3.-HELLYWOOD.avi",
			want:     "Obrazcovyj Samec",
			noise:    []string{"rus", "bdrip", "xvid", "ac3", "hellywood"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			candidates := generateTitleCandidates(tt.parsed, tt.filename)
			if len(candidates) == 0 || candidates[0] != tt.want {
				t.Fatalf("candidates=%v want first=%q", candidates, tt.want)
			}
			for _, candidate := range candidates {
				lower := strings.ToLower(candidate)
				for _, noise := range tt.noise {
					if strings.Contains(lower, noise) {
						t.Fatalf("release noise %q leaked into %q; candidates=%v", noise, candidate, candidates)
					}
				}
			}
		})
	}
}

func TestGenerateTitleCandidatesPreservesTitleByAndHyphenSuffix(t *testing.T) {
	for _, fixture := range []struct{ parsed, filename, forbidden string }{
		{"Stand by Me", "Stand.by.Me.1986.mkv", "Stand"},
		{"Written By", "Written.By.2016.mkv", "Written"},
		{"Spider-Man", "Spider-Man.2002.mkv", "Spider"},
	} {
		candidates := generateTitleCandidates(fixture.parsed, fixture.filename)
		if len(candidates) == 0 || candidates[0] != fixture.parsed {
			t.Fatalf("%s => %v", fixture.filename, candidates)
		}
		for _, candidate := range candidates {
			if candidate == fixture.forbidden {
				t.Fatalf("title suffix was treated as release group: %s => %v", fixture.filename, candidates)
			}
		}
	}
}

func TestPipelineLatinResolvesProductionTransliterationsWithoutAI(t *testing.T) {
	tests := []struct {
		filename, query, title string
		id, year               int
	}{
		{"Tretyi_lishnyi_2012_BDRip_AVO_[TC]_by_Dalemake.avi", "Третий лишний", "Третий лишний", 72105, 2012},
		{"Dorozhnoe.prikljuchenie.1.2000.XviD.HDTVRip.avi", "Дорожное приключение", "Дорожное приключение", 9285, 2000},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/search/movie") {
					if r.URL.Query().Get("query") == tt.query {
						fmt.Fprintf(w, `{"results":[{"id":%d,"title":%q,"original_title":"Original","release_date":"%s-01-01","original_language":"en","popularity":20}]}`, tt.id, tt.title, r.URL.Query().Get("year"))
						return
					}
					w.Write([]byte(`{"results":[]}`))
					return
				}
				if strings.Contains(r.URL.Path, fmt.Sprintf("/movie/%d", tt.id)) {
					fmt.Fprintf(w, `{"id":%d,"title":%q,"original_title":"Original","release_date":"%d-01-01","overview":"Український опис історії.","genres":[],"credits":{"cast":[]}}`, tt.id, tt.title, tt.year)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1), postersDir: t.TempDir()}
			parsed := ParseFilename(tt.filename)
			info, err := c.pipelineLatin(context.Background(), parsed, tt.filename)
			if err != nil {
				t.Fatal(err)
			}
			if info == nil || info.TMDBID != tt.id {
				t.Fatalf("info=%+v", info)
			}
		})
	}
}

func TestManualCandidateYearIsTieBreakerNotRejection(t *testing.T) {
	score, year, ok := manualCandidateScore(tmdbSearchResult{
		ID: 1, Title: "Daniel's Gotta Die", OriginalTitle: "Daniel's Gotta Die", ReleaseDate: "2025-01-01", MediaType: "movie",
	}, normalizeForCompare("Daniel's Gotta Die"), 2022, MediaTypeMovie)
	if !ok || score <= 0 || year != 2025 {
		t.Fatalf("exact manual title rejected by year: score=%d year=%d ok=%v", score, year, ok)
	}
}

func TestManualCandidateAcceptsLocalizedEnglishTitle(t *testing.T) {
	_, _, ok := manualCandidateScore(tmdbSearchResult{
		ID: 89844, Name: "The Bureau", OriginalName: "Le Bureau des légendes", FirstAirDate: "2015-04-27", MediaType: "tv",
	}, normalizeForCompare("The Bureau"), 2015, MediaTypeTV)
	if !ok {
		t.Fatal("exact validated English search title was rejected")
	}
}

func TestManualCandidateAutoPrefersPopularSeriesForTheBureau(t *testing.T) {
	norm := normalizeForCompare("The Bureau")
	movieScore, _, _ := manualCandidateScore(tmdbSearchResult{
		ID: 1, Title: "The Bureau", OriginalTitle: "The Bureau", ReleaseDate: "2015-01-01", MediaType: "movie", Popularity: 1,
	}, norm, 2015, MediaTypeMovie)
	seriesScore, _, _ := manualCandidateScore(tmdbSearchResult{
		ID: 62476, Name: "The Bureau", OriginalName: "Le Bureau des légendes", FirstAirDate: "2015-04-27", MediaType: "tv", Popularity: 35,
	}, norm, 2015, MediaTypeMovie)
	if seriesScore <= movieScore {
		t.Fatalf("Auto would prefer obscure movie: movie=%d series=%d", movieScore, seriesScore)
	}
}

func TestSearchExactTitleUsesReleaseFolderToDisambiguateTheBureau(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/movie"):
			w.Write([]byte(`{"results":[{"id":802663,"title":"The Bureau","original_title":"The Bureau","release_date":"2020-01-01","popularity":999}]}`))
		case strings.HasSuffix(r.URL.Path, "/search/tv"):
			w.Write([]byte(`{"results":[{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27","popularity":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/tv/62476"):
			w.Write([]byte(`{"id":62476,"name":"The Bureau","original_name":"Le Bureau des légendes","first_air_date":"2015-04-27"}`))
		case strings.Contains(r.URL.Path, "/movie/802663"):
			w.Write([]byte(`{"id":802663,"title":"The Bureau","original_title":"The Bureau","release_date":"2020-01-01"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1), postersDir: t.TempDir()}
	filename := filepath.Join("Bjuro legend.HDTVRip.GeneralFilm", "Bjuro legend.08.HDTVRip.GeneralFilm.avi")
	info, err := c.SearchExactTitle(context.Background(), "The Bureau", 0, MediaTypeMovie, false, filename)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.TMDBID != 62476 || info.MediaType != MediaTypeTV {
		movieContext := exactFilenameContextScore(filename, tmdbSearchResult{Title: "The Bureau", OriginalTitle: "The Bureau"})
		tvContext := exactFilenameContextScore(filename, tmdbSearchResult{Name: "The Bureau", OriginalName: "Le Bureau des légendes"})
		t.Fatalf("release context selected %+v; want TV 62476 (movie_context=%d tv_context=%d similarity=%.3f)", info, movieContext, tvContext, TitleSimilarity("Bjuro legend", "Le Bureau des légendes"))
	}
}

func TestPopularityTieBreakIsBounded(t *testing.T) {
	if got := popularityTieBreak(999); got != 100 {
		t.Fatalf("popularity tie-break=%d", got)
	}
}

func TestSearchExactTitleUsesTypedEndpointsAndVerifiedDetails(t *testing.T) {
	var movieSearch, tvSearch bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/movie"):
			movieSearch = true
			w.Write([]byte(`{"results":[{"id":103663,"title":"Enemy","original_title":"Shatru","release_date":"2013-08-23","popularity":2},{"id":181886,"title":"Enemy","original_title":"Enemy","release_date":"2014-03-14","popularity":25}]}`))
		case strings.HasSuffix(r.URL.Path, "/search/tv"):
			tvSearch = true
			w.Write([]byte(`{"results":[]}`))
		case strings.HasSuffix(r.URL.Path, "/movie/181886"):
			w.Write([]byte(`{"id":181886,"title":"Enemy","original_title":"Enemy","release_date":"2014-03-14"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1), postersDir: t.TempDir()}
	info, err := c.SearchExactTitle(context.Background(), "Enemy", 2013, MediaTypeMovie, false, "Enemy.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.TMDBID != 181886 {
		t.Fatalf("resolved info = %+v; want verified TMDB 181886", info)
	}
	if !movieSearch || !tvSearch {
		t.Fatalf("typed endpoints used: movie=%v tv=%v", movieSearch, tvSearch)
	}
}

func TestSearchExactTitleDoesNotFallBackToFuzzyCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"The Enemy Within","original_title":"The Enemy Within","release_date":"2013-01-01"}]}`))
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &searchRewriteTransport{serverURL: server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	info, err := c.SearchExactTitle(context.Background(), "Enemy", 2013, MediaTypeMovie, false, "file.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Fatalf("fuzzy candidate was accepted: %+v", info)
	}
}

type searchRewriteTransport struct{ serverURL string }

func (t *searchRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base, _ := url.Parse(t.serverURL)
	clone := req.Clone(req.Context())
	clone.URL.Scheme = base.Scheme
	clone.URL.Host = base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestRankResultsFallbackStillUsesTargetYear(t *testing.T) {
	c := &Client{client: &http.Client{}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	results := []tmdbSearchResult{
		{ID: 1, Title: "Master i Margarita", OriginalTitle: "Master i Margarita", ReleaseDate: "2025-01-01", MediaType: "movie", OriginalLanguage: "uk"},
		{ID: 2, Title: "Master i Margarita", OriginalTitle: "Master i Margarita", ReleaseDate: "2024-01-01", MediaType: "movie", OriginalLanguage: "en"},
	}
	best := c.rankResults(context.Background(), results, "Master i Margarita", 2023, MediaTypeMovie)
	if best == nil || best.result.ID != 2 {
		t.Fatalf("expected year-compatible candidate 2, got %+v", best)
	}
}

func TestRankResultsLanguageIsOnlyTieBreaker(t *testing.T) {
	c := &Client{client: &http.Client{}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}

	// A large identity gap must win even when the weaker candidate has a better language score.
	results := []tmdbSearchResult{
		{ID: 1, Title: "Enemy", OriginalTitle: "Enemy", ReleaseDate: "2014-01-01", MediaType: "movie", OriginalLanguage: "ru"},
		{ID: 2, Title: "Enemy", OriginalTitle: "Enemy", ReleaseDate: "2020-01-01", MediaType: "movie", OriginalLanguage: "uk", Popularity: 1000},
	}
	best := c.rankResults(context.Background(), results, "Enemy", 2013, MediaTypeMovie)
	if best == nil || best.result.ID != 1 {
		t.Fatalf("identity winner was overridden by language/popularity: %+v", best)
	}

	// With equal identity quality, localization preference is allowed to break the tie.
	tied := []tmdbSearchResult{
		{ID: 3, Title: "Dune", OriginalTitle: "Dune", ReleaseDate: "2021-01-01", MediaType: "movie", OriginalLanguage: "en"},
		{ID: 4, Title: "Dune", OriginalTitle: "Dune", ReleaseDate: "2021-01-01", MediaType: "movie", OriginalLanguage: "uk"},
	}
	best = c.rankResults(context.Background(), tied, "Dune", 2021, MediaTypeMovie)
	if best == nil || best.result.ID != 4 {
		t.Fatalf("language did not break equal identity tie: %+v", best)
	}
}

func TestBuildAttemptsKeepsParentFolderForNestedRelease(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "The Matrix 1999", "sample.mkv")
	parsed := ParseFilename(file)

	attempts := buildAttempts(parsed, file, root)

	for _, attempt := range attempts {
		if attempt.label == "Папка" {
			return
		}
	}
	t.Fatal("nested release folder fallback should be present")
}

func TestBuildAttemptsSkipsGenericParentFolders(t *testing.T) {
	root := t.TempDir()
	genericNames := []string{"series", "films", "downloads", "кино", "movies", "video"}
	for _, generic := range genericNames {
		file := filepath.Join(root, generic, "Dune.mkv")
		parsed := ParseFilename(file)
		attempts := buildAttempts(parsed, file, root)
		for _, attempt := range attempts {
			if attempt.label == "Папка" {
				t.Errorf("generic folder %q should be skipped, got attempt with query %q", generic, attempt.query)
			}
		}
	}
}

func TestDownloadPosterPreservesFlattenedPathPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("poster"))
	}))
	defer server.Close()

	c := &Client{
		client:     server.Client(),
		postersDir: t.TempDir(),
	}

	path, err := c.DownloadPoster(context.Background(), server.URL, "12345_SeriesName/episode.mkv")
	if err != nil {
		t.Fatalf("DownloadPoster() error = %v", err)
	}

	got := filepath.Base(path)
	if !strings.HasPrefix(got, "12345_SeriesName_episode_mkv") {
		t.Fatalf("poster filename = %q, want flattened path prefix", got)
	}
}
