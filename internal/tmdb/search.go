package tmdb

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
)

// tmdbSearchResult — один результат з /search/multi
type tmdbSearchResult struct {
	ID               int     `json:"id"`
	MediaType        string  `json:"media_type"`
	Title            string  `json:"title"`          // для movie
	Name             string  `json:"name"`           // для tv
	OriginalTitle    string  `json:"original_title"`
	OriginalName     string  `json:"original_name"`
	ReleaseDate      string  `json:"release_date"`   // для movie
	FirstAirDate     string  `json:"first_air_date"` // для tv
	Popularity       float64 `json:"popularity"`
	OriginalLanguage string  `json:"original_language"`
}

// tmdbSearchResponse — відповідь /search/multi
type tmdbSearchResponse struct {
	Results []tmdbSearchResult `json:"results"`
}

// scoredResult — кандидат з підрахованим балом
type scoredResult struct {
	result tmdbSearchResult
	score  int
	year   int
}

// SearchWithFallbacks — каскадний пошук з fallback-стратегіями.
// Послідовність спроб залежить від ParsedFile (мова, рік, тип).
// Gemini-fallback сюди НЕ входить — він на рівні вище (client.go).
func (c *Client) SearchWithFallbacks(
	ctx context.Context,
	parsed ParsedFile,
	originalFilename string,
) (*MovieInfo, error) {
	attempts := buildAttempts(parsed)

	for _, a := range attempts {
		log.Printf("[TMDB] 🔍 Спроба '%s': query='%s' year=%d type=%s",
			a.label, a.query, a.year, a.mediaType)

		info, err := c.searchAndFetch(ctx, a.query, a.year, a.mediaType, originalFilename)
		if err != nil {
			log.Printf("[TMDB] ⚠️ '%s': помилка запиту: %v", a.label, err)
			continue
		}
		if info != nil {
			log.Printf("[TMDB] ✅ '%s': знайдено '%s' (%s)", a.label, info.TitleEN, info.Year)
			return info, nil
		}
		log.Printf("[TMDB] ❌ '%s': результат не пройшов поріг", a.label)
	}

	return nil, nil
}

// searchAttempt — одна спроба пошуку
type searchAttempt struct {
	query     string
	year      int
	mediaType MediaType
	label     string
}

// buildAttempts формує впорядкований список спроб пошуку
func buildAttempts(parsed ParsedFile) []searchAttempt {
	title := parsed.CleanTitle
	year := parsed.Year
	mt := parsed.MediaType

	var attempts []searchAttempt

	// Спроба 1: точний запит з роком (якщо рік є)
	if year > 0 {
		attempts = append(attempts, searchAttempt{title, year, mt, "точний+рік"})
	}

	// Спроба 2: запит без року
	attempts = append(attempts, searchAttempt{title, 0, mt, "без року"})

	// Спроба 3: протилежний media_type без року
	// (серіал без маркера S01 потрапить як movie — дамо шанс знайти як tv і навпаки)
	opposite := MediaTypeMovie
	if mt == MediaTypeMovie {
		opposite = MediaTypeTV
	}
	attempts = append(attempts, searchAttempt{title, 0, opposite, "протилежний тип"})

	return attempts
}

// searchAndFetch виконує один запит до TMDB, ранжує результати,
// і якщо знайшов переможця — витягує повні деталі
func (c *Client) searchAndFetch(
	ctx context.Context,
	query string,
	targetYear int,
	preferredType MediaType,
	originalFilename string,
) (*MovieInfo, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	searchURL := fmt.Sprintf(
		"%s/search/multi?api_key=%s&query=%s&language=uk-UA",
		baseURL, c.apiKey, url.QueryEscape(query),
	)

	var resp tmdbSearchResponse
	if err := c.doRequestWithRetry(ctx, searchURL, &resp); err != nil {
		return nil, err
	}

	if len(resp.Results) == 0 {
		return nil, nil
	}

	best := rankResults(resp.Results, query, targetYear, preferredType)
	if best == nil {
		return nil, nil
	}

	// Визначаємо фінальний MediaType для запиту деталей
	detailType := MediaTypeMovie
	if best.result.MediaType == "tv" {
		detailType = MediaTypeTV
	}

	return c.GetDetails(ctx, detailType, best.result.ID, originalFilename)
}

// rankResults ранжує результати пошуку і повертає найкращого кандидата.
// Повертає nil якщо жоден кандидат не набрав достатньо балів.
func rankResults(
	results []tmdbSearchResult,
	query string,
	targetYear int,
	preferredType MediaType,
) *scoredResult {
	normQuery := normalizeForCompare(query)

	var best *scoredResult

	for _, res := range results {
		// Людей ігноруємо завжди
		if res.MediaType == "person" {
			continue
		}

		scored := scoreResult(res, normQuery, targetYear, preferredType)

		log.Printf("[TMDB] 🧮 Кандидат: '%s' / '%s' (%d) lang=%s score=%d",
			coalesce(res.Title, res.Name),
			coalesce(res.OriginalTitle, res.OriginalName),
			scored.year,
			res.OriginalLanguage,
			scored.score,
		)

		if best == nil || scored.score > best.score {
			best = &scored
		}
	}

	if best == nil {
		return nil
	}

	// Динамічний поріг: якщо шукали з роком — вимагаємо більшої впевненості
	threshold := ScoreThreshold
	if targetYear > 0 {
		threshold = ScoreThreshold + 50
	}

	if best.score < threshold {
		log.Printf("[TMDB] 🚫 Найкращий '%s' набрав %d < порогу %d — відхилено",
			coalesce(best.result.Title, best.result.Name),
			best.score,
			threshold,
		)
		return nil
	}

	return best
}

// scoreResult підраховує бал для одного результату пошуку
func scoreResult(
	res tmdbSearchResult,
	normQuery string,
	targetYear int,
	preferredType MediaType,
) scoredResult {
	score := 0

	// --- Рік результату ---
	dateStr := coalesce(res.ReleaseDate, res.FirstAirDate)
	resYear := 0
	if len(dateStr) >= 4 {
		resYear, _ = strconv.Atoi(dateStr[:4])
	}

	// --- Нормалізовані назви ---
	resTitle := normalizeForCompare(coalesce(res.Title, res.Name))
	resOrig := normalizeForCompare(coalesce(res.OriginalTitle, res.OriginalName))

	// --- Збіг назви: точний → contains → fuzzy ---
	titleScore := matchScore(normQuery, resTitle, resOrig)
	score += titleScore

	// --- Збіг року ---
	if targetYear > 0 && resYear > 0 {
		switch diff := abs(targetYear - resYear); {
		case diff == 0:
			score += ScoreYearExact
		case diff == 1:
			score += ScoreYearDiffOne
		default:
			score += ScoreYearDiffTooFar // від'ємне
		}
	}

	// --- Відповідність типу медіа ---
	if (preferredType == MediaTypeMovie && res.MediaType == "movie") ||
		(preferredType == MediaTypeTV && res.MediaType == "tv") {
		score += ScoreMediaTypeMatch
	}

	// --- Мова оригіналу ---
	switch res.OriginalLanguage {
	case "uk":
		score += ScoreLangUA
	case "en":
		score += ScoreLangEN
	case "ru":
		if resYear > 0 && resYear < 2010 {
			score += ScoreLangRUOld    // -300
		} else {
			score += ScoreLangRURecent // -50
		}
	}

	// --- Popularity (обмежений вплив, щоб не домінував над назвою) ---
	popBonus := int(res.Popularity / 5)
	if popBonus > ScorePopularityLimit {
		popBonus = ScorePopularityLimit
	}
	score += popBonus

	return scoredResult{result: res, score: score, year: resYear}
}

// matchScore повертає бал за збіг запиту з назвами результату.
// Ієрархія: точний збіг > contains > fuzzy > 0
func matchScore(normQuery, resTitle, resOrig string) int {
	// Точний збіг
	if resTitle == normQuery || resOrig == normQuery {
		return ScoreExactMatch
	}

	// Contains (запит є підрядком назви або навпаки)
	if strings.Contains(resTitle, normQuery) || strings.Contains(resOrig, normQuery) {
		return ScoreContainsMatch
	}
	if strings.Contains(normQuery, resTitle) && len([]rune(resTitle)) > 3 {
		return ScoreContainsMatch / 2
	}

	// Fuzzy: толерантність до 1-2 символів різниці
	// Перевіряємо обидві назви, беремо кращий результат
	fuzzyTitle := fuzzyMatchScore(normQuery, resTitle)
	fuzzyOrig := fuzzyMatchScore(normQuery, resOrig)
	fuzzy := fuzzyTitle
	if fuzzyOrig > fuzzy {
		fuzzy = fuzzyOrig
	}
	return fuzzy
}

// fuzzyMatchScore повертає бал схожості через відстань Левенштейна.
// Повертає 0 якщо рядки занадто різні.
func fuzzyMatchScore(a, b string) int {
	if a == "" || b == "" {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)

	// Якщо довжини сильно різняться — навіть не рахуємо
	diff := la - lb
	if diff < 0 {
		diff = -diff
	}
	maxLen := la
	if lb > maxLen {
		maxLen = lb
	}
	if diff > 3 || maxLen < 4 {
		return 0
	}

	d := levenshtein(ra, rb)
	switch {
	case d == 1:
		return 130 // "Ella" vs "Elle" — одна буква
	case d == 2 && maxLen > 8:
		return 70 // дві букви у довгому рядку
	default:
		return 0
	}
}

// levenshtein — класична відстань редагування (Wagner–Fischer)
func levenshtein(a, b []rune) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Оптимізація: тільки два рядки замість матриці la×lb
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1]
			} else {
				curr[j] = 1 + minOf3(prev[j], curr[j-1], prev[j-1])
			}
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// --- helpers ---

// normalizeForCompare приводить рядок до нижнього регістру,
// замінює пунктуацію/пробіли на один пробіл
func normalizeForCompare(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		isAlphaNum := (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			(r >= 'а' && r <= 'я') ||
			(r >= 'А' && r <= 'Я') ||
			r == 'і' || r == 'ї' || r == 'є' || r == 'ґ' ||
			r == 'І' || r == 'Ї' || r == 'Є' || r == 'Ґ'

		if isAlphaNum {
			b.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			b.WriteRune(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// coalesce повертає перший непорожній рядок
func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// abs — абсолютне значення int
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// minOf3 — мінімум з трьох int
func minOf3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
