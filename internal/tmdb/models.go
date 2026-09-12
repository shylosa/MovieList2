package tmdb

import "regexp"

// reYear — спільна регулярка для пошуку року в назвах
var reYear = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)

// MediaType — тип медіаконтенту
type MediaType string

const (
	MediaTypeMovie MediaType = "movie"
	MediaTypeTV    MediaType = "tv"
)

// TitleLanguage — мова назви у імені файлу
type TitleLanguage string

const (
	TitleLangLatin    TitleLanguage = "latin"
	TitleLangCyrillic TitleLanguage = "cyrillic"
)

// ParsedFile — результат парсингу імені файлу
type ParsedFile struct {
	OriginalName string
	ParentDir    string // назва батьківської папки
	CleanTitle   string
	Year         int // 0 якщо не знайдено
	MediaType    MediaType
	TitleLang    TitleLanguage
	IMDBID       string // IMDb ID (tt1234567) якщо знайдено
}

// MovieInfo — фінальний результат після верифікації через TMDB
type MovieInfo struct {
	TMDBID          int
	TitleUA         string
	TitleEN         string // Це насправді OriginalTitle (за базою)
	SearchTitle     string // Знайдена локалізована назва (для TitleSimilarity)
	MatchedAlias    string // Назва-аліас, яка дала найкращий бал при пошуку
	Year            string
	Genres          string
	Plot            string
	Cast            string
	PosterURL       string
	LocalPosterPath string
	MediaType       MediaType
	AmbiguousExact  bool
	VoteAverage     float64
	VoteCount       int
}

type CandidateDetails struct {
	TMDBID        int       `json:"tmdb_id"`
	MediaType     MediaType `json:"media_type"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"original_title"`
	ReleaseDate   string    `json:"release_date"`
	Runtime       int       `json:"runtime"`
	Genres        string    `json:"genres"`
	Overview      string    `json:"overview"`
	PosterURL     string    `json:"poster_url"`
	VoteAverage   float64   `json:"vote_average"`
	VoteCount     int       `json:"vote_count"`
	ShortFilm     bool      `json:"short_film"`
}

// TMDBCandidate is a lightweight search preview. It deliberately contains no
// poster or details payload, so displaying choices costs only typed searches.
type TMDBCandidate struct {
	TMDBID        int       `json:"tmdb_id"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"original_title"`
	Year          int       `json:"year"`
	MediaType     MediaType `json:"media_type"`
	Popularity    float64   `json:"popularity"`
	Exact         bool      `json:"-"`
}

// Scoring — ваги для ранжування результатів пошуку
const (
	ScoreExactMatch     = 200
	ScoreContainsMatch  = 70
	ScoreYearExact      = 150
	ScoreYearDiffOne    = 80
	ScoreYearDiffTooFar = -400
	ScoreMediaTypeMatch = 30
	ScoreLangUA         = 20
	ScoreLangEN         = 10
	ScoreLangRURecent   = -50  // ru, рік >= 2010
	ScoreLangRUOld      = -300 // ru, рік < 2010

	ScorePopularityLimit = 50 // max бонус від popularity

	// Мінімальний поріг для прийняття результату
	ScoreThreshold              = 200
	ReviewVerificationThreshold = 0.90
)
