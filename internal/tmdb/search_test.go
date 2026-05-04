package tmdb

import (
	"context"
	"testing"
)

func TestMatchScore(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		resTitle  string
		resOrig   string
		minScore  int // Очікуємо бал >= вказаного
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
	c := &Client{} // Порожній клієнт, нам потрібна лише логіка
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
