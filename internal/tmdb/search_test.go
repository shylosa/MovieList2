package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
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
