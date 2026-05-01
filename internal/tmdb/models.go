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
	CleanTitle   string
	Year         int // 0 якщо не знайдено
	MediaType    MediaType
	TitleLang    TitleLanguage
}

// MovieInfo — фінальний результат після верифікації через TMDB
type MovieInfo struct {
	TMDBID          int
	TitleUA         string
	TitleEN         string
	Year            string
	Genres          string
	Plot            string
	Cast            string
	PosterURL       string
	LocalPosterPath string
	MediaType       MediaType
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
	ScoreThreshold = 200
)
