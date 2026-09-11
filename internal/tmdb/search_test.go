package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/time/rate"
)

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
