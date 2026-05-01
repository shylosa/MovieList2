package tmdb

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"movielist-app/internal/utils"
	"github.com/xrash/smetrics"
)

// tmdbSearchResult — один результат з /search/multi
type tmdbSearchResult struct {
	ID               int     `json:"id"`
	MediaType        string  `json:"media_type"`
	Title            string  `json:"title"` // для movie
	Name             string  `json:"name"`  // для tv
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
	attempts := buildAttempts(parsed, originalFilename)

	for _, a := range attempts {
		logger := utils.LoggerWithTrace(ctx).With(slog.String("component", "tmdb_search"))
		logger.Info("search_attempt",
			slog.String("label", a.label),
			slog.String("query", a.query),
			slog.Int("year", a.year),
		)

		info, err := c.searchAndFetch(ctx, a.query, a.year, a.mediaType, originalFilename)
		if err != nil {
			logger.Warn("search_failed", slog.String("label", a.label), slog.Any("error", err))
			continue
		}
		if info != nil {
			logger.Info("search_success", slog.String("label", a.label), slog.String("title", info.TitleEN))
			return info, nil
		}
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

// buildAttempts формує розумний список спроб пошуку, використовуючи кандидатів
func buildAttempts(parsed ParsedFile, originalFilename string) []searchAttempt {
	year := parsed.Year
	mt := parsed.MediaType

	if mt == MediaTypeMovie && isCyrillicSeries(originalFilename) {
		mt = MediaTypeTV
		slog.Info("media_type_changed_to_tv", slog.String("filename", originalFilename))
	}

	candidates := generateTitleCandidates(parsed.CleanTitle, originalFilename)
	var attempts []searchAttempt

	for i, title := range candidates {
		labelPrefix := "Кандидат"
		if i == 0 {
			labelPrefix = "Базовий"
		}

		if year > 0 {
			attempts = append(attempts, searchAttempt{title, year, mt, labelPrefix + "+рік"})
		}
		attempts = append(attempts, searchAttempt{title, 0, mt, labelPrefix + " без року"})
	}

	if len(candidates) > 0 {
		bestTitle := candidates[0]
		opposite := MediaTypeMovie
		if mt == MediaTypeMovie {
			opposite = MediaTypeTV
		}
		attempts = append(attempts, searchAttempt{bestTitle, 0, opposite, "Протилежний тип"})
	}

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

	// 🔴 ВИПРАВЛЕННЯ: Динамічний вибір мови індексу для TMDB
	langParam := "en-US"
	if hasCyrillicChars(query) {
		langParam = "ru-RU" // Відкриваємо доступ до кириличних індексів
	}

	searchURL := fmt.Sprintf(
		"%s/search/multi?api_key=%s&query=%s&language=%s",
		baseURL, c.apiKey, url.QueryEscape(query), langParam,
	)

	var resp tmdbSearchResponse
	if err := c.doRequestWithRetry(ctx, searchURL, &resp); err != nil {
		return nil, err
	}

	if len(resp.Results) == 0 {
		return nil, nil
	}

	best := c.rankResults(ctx, resp.Results, query, targetYear, preferredType)
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
func (c *Client) rankResults(
	ctx context.Context,
	results []tmdbSearchResult,
	query string,
	targetYear int,
	preferredType MediaType,
) *scoredResult {
	normQuery := normalizeForCompare(query)

	var best *scoredResult

	for i, res := range results {
		// Людей ігноруємо завжди
		if res.MediaType == "person" {
			continue
		}

		scored := c.scoreResult(ctx, res, i, normQuery, targetYear, preferredType)

		utils.LoggerWithTrace(ctx).Debug("candidate_evaluated",
			slog.String("title", coalesce(res.Title, res.Name)),
			slog.String("orig_title", coalesce(res.OriginalTitle, res.OriginalName)),
			slog.Int("year", scored.year),
			slog.String("lang", res.OriginalLanguage),
			slog.Int("score", scored.score),
		)

		if best == nil || scored.score > best.score {
			best = &scored
		}
	}

	if best == nil {
		return nil
	}

	// Динамічний поріг: пом'якшуємо вимоги, особливо для транслітерації
	threshold := ScoreThreshold - 30 // Даємо трохи більше свободи базовому пошуку

	if targetYear > 0 {
		if best.year == targetYear {
			// Якщо рік ідеально збігається, ми можемо довіряти fuzzy-збігу назви
			threshold = ScoreThreshold - 50
		} else {
			// Рік не збігається, але був у запиті — будьмо обережніші
			threshold = ScoreThreshold + 20
		}
	}

	if best.score < threshold {
		utils.LoggerWithTrace(ctx).Warn("best_candidate_rejected",
			slog.String("title", coalesce(best.result.Title, best.result.Name)),
			slog.Int("score", best.score),
			slog.Int("threshold", threshold),
		)
		return nil
	}

	return best
}

// scoreResult підраховує бал для одного результату пошуку
func (c *Client) scoreResult(
	ctx context.Context,
	res tmdbSearchResult,
	index int,
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

	// 💎 ДІАМАНТОВИЙ БОНУС (Оригінальна назва + Точний рік)
	if targetYear > 0 && resYear == targetYear && resOrig == normQuery {
		score += 300 // Величезний бонус, гарантує 1 місце
		utils.LoggerWithTrace(ctx).Info("diamond_match",
			slog.String("title", resOrig),
			slog.Int("year", resYear),
		)
	}

	// --- Збіг назви: точний → contains → fuzzy ---
	titleScore := matchScore(normQuery, resTitle, resOrig)

	// --- ПЕРЕВІРКА АЛІАСІВ (ПУНКТ 1) ---
	// Якщо базовий збіг низький, але це топовий результат TMDB — перевіряємо аліаси
	if titleScore < 100 && index < 3 {
		mediaType := MediaTypeMovie
		if res.MediaType == "tv" {
			mediaType = MediaTypeTV
		}

		alts, err := c.getAlternativeTitles(ctx, res.ID, mediaType)
		if err == nil {
			for _, alt := range alts {
				altNorm := normalizeForCompare(alt)
				altScore := fuzzyMatchScoreJW(normQuery, altNorm)
				if altScore > titleScore {
					titleScore = altScore
					if altScore >= 150 { // Ідеальний збіг в аліасах
						break
					}
				}
			}
		}
	}

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
	} else if targetYear == 0 && resYear > 0 {
		// ⚖️ БАЛАНСУВАННЯ: Якщо рік файлу невідомий, віддаємо перевагу сучасним релізам.
		if resYear >= 2000 {
			score += 15 // Бонус за сучасність
		} else if resYear < 1980 {
			score -= 30 // Штраф для дуже старих
		}
	}

	// --- Відповідність типу медіа ---
	if (preferredType == MediaTypeMovie && res.MediaType == "movie") ||
		(preferredType == MediaTypeTV && res.MediaType == "tv") {
		score += ScoreMediaTypeMatch
	}

	// --- Мова оригіналу ---
	queryIsCyrillic := hasCyrillicChars(normQuery)
	switch res.OriginalLanguage {
	case "uk":
		score += ScoreLangUA
	case "en":
		score += ScoreLangEN
	case "ru":
		if queryIsCyrillic {
			score += 10
		} else {
			if resYear > 0 && resYear < 2010 {
				score += ScoreLangRUOld // -300
			} else {
				score += ScoreLangRURecent // -50
			}
		}
	}

	// --- Popularity ---
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

	// ШТРАФ ЗА КОРОТКІ НАЗВИ: якщо запит < 4 символів, працює тільки Exact Match
	if len([]rune(normQuery)) < 4 {
		return 0
	}

	// Contains (запит є підрядком назви або навпаки)
	if strings.Contains(resTitle, normQuery) || strings.Contains(resOrig, normQuery) {
		return ScoreContainsMatch
	}
	if strings.Contains(normQuery, resTitle) && len([]rune(resTitle)) > 3 {
		return ScoreContainsMatch / 2
	}

	// Fuzzy: Jaro-Winkler
	fuzzyTitle := fuzzyMatchScoreJW(normQuery, resTitle)
	fuzzyOrig := fuzzyMatchScoreJW(normQuery, resOrig)
	fuzzy := fuzzyTitle
	if fuzzyOrig > fuzzy {
		fuzzy = fuzzyOrig
	}
	return fuzzy
}

// fuzzyMatchScoreJW використовує Jaro-Winkler для порівняння рядків.
func fuzzyMatchScoreJW(a, b string) int {
	if a == "" || b == "" {
		return 0
	}

	score := smetrics.JaroWinkler(a, b, 0.7, 4)

	if score > 0.95 {
		return 150
	} else if score > 0.90 {
		return 120
	} else if score > 0.85 {
		return 80
	}

	return 0
}

// --- helpers ---

// --- РОЗУМНИЙ ПАРСИНГ ТА ГЕНЕРАЦІЯ КАНДИДАТІВ ---

var (
	reQuality  = regexp.MustCompile(`(?i)\b(1080p|720p|2160p|4k|8k|HDRip|BDRip|WEB-DLRip|WEB-DL|WEBRip|HDTV|HDTVRip|CAMRip|TS|DVDScr|DVDRip|BluRay|HDRezka|Line|\d{3,4}Mb)\b`)
	reCodec    = regexp.MustCompile(`(?i)\b(x264|x265|h264|h265|HEVC|AV1|AVC)\b`)
	reAudio    = regexp.MustCompile(`(?i)\b(AAC|DTS|AC3|DDP5\.1|Atmos|Dub|UkrDub|RusDub|MVO|DUB|L1|L2)\b`)
	reRelease  = regexp.MustCompile(`(?i)(-?seleZen|-?ivanes|-?RG|-?NNMClub|\bUkr\b|\bRus\b|\bEng\b)`)
	reExt      = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|mov)$`)
	reBrackets = regexp.MustCompile(`\[.*?\]|\(.*?\)`)
	rePunct    = regexp.MustCompile(`[\._]`)
	reSpaces   = regexp.MustCompile(`\s{2,}`)
	reSeries   = regexp.MustCompile(`(?i)(\d{1,2}\s*сезон|сезон\s*\d{1,2}|серия\s*\d{1,3}|серии\s*\d{1,3}-\d{1,3}|Часть\s*\d)`)
)

// cleanString замінює крапки/підкреслення на пробіли і прибирає зайві пробіли
func cleanString(s string) string {
	s = rePunct.ReplaceAllString(s, " ")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// isCyrillicSeries перевіряє наявність специфічних маркерів серіалу
func isCyrillicSeries(filename string) bool {
	return reSeries.MatchString(filename)
}

// generateTitleCandidates формує кілька варіантів чистої назви для пошуку
func generateTitleCandidates(ptnTitle, filename string) []string {
	var candidates []string
	seen := make(map[string]bool)

	add := func(c string) {
		if c != "" && !seen[c] && len([]rune(c)) > 2 {
			candidates = append(candidates, c)
			seen[c] = true
		}
	}

	add(cleanString(ptnTitle))

	s := reExt.ReplaceAllString(filename, "")
	s = reQuality.ReplaceAllString(s, "")
	s = reCodec.ReplaceAllString(s, "")
	s = reAudio.ReplaceAllString(s, "")
	s = reRelease.ReplaceAllString(s, "")

	noBrackets := reBrackets.ReplaceAllString(s, "")
	noBrackets = reYear.ReplaceAllString(noBrackets, "")
	noBrackets = cleanString(noBrackets)
	add(noBrackets)

	if idx := strings.Index(noBrackets, " - "); idx > 0 {
		add(strings.TrimSpace(noBrackets[:idx]))
	}

	soft := reYear.ReplaceAllString(s, "")
	soft = cleanString(soft)
	add(soft)

	return candidates
}

// normalizeForCompare приводить рядок до нижнього регістру,
// замінює пунктуацію/пробіли на один пробіл
func normalizeForCompare(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
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

// hasCyrillicChars перевіряє, чи містить рядок кириличні символи
func hasCyrillicChars(s string) bool {
	for _, r := range s {
		if (r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') ||
			r == 'і' || r == 'ї' || r == 'є' || r == 'ґ' ||
			r == 'І' || r == 'Ї' || r == 'Є' || r == 'Ґ' ||
			r == 'ё' || r == 'Ё' || r == 'ы' || r == 'Ы' ||
			r == 'э' || r == 'Э' || r == 'ъ' || r == 'Ъ' ||
			r == 'ь' || r == 'Ь' {
			return true
		}
	}
	return false
}
