package tmdb

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"movielist-app/internal/utils"

	"github.com/xrash/smetrics"
)

// SearchCandidates returns at most five deterministic lightweight candidates.
func (c *Client) SearchCandidates(ctx context.Context, title string, year int, requestedType string) ([]TMDBCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return []TMDBCandidate{}, nil
	}
	types := []MediaType{MediaTypeMovie, MediaTypeTV}
	switch strings.ToLower(strings.TrimSpace(requestedType)) {
	case "movie":
		types = types[:1]
	case "tv":
		types = types[1:]
	case "", "auto":
	default:
		return nil, fmt.Errorf("invalid media_type %q", requestedType)
	}
	norm := normalizeForCompare(title)
	queryNorms := map[string]bool{norm: true}
	seen := make(map[string]bool)
	out := make([]TMDBCandidate, 0, 10)
	search := func(query, language string) error {
		for _, mediaType := range types {
			if err := ctx.Err(); err != nil {
				return err
			}
			searchURL := fmt.Sprintf("%s/search/%s?api_key=%s&query=%s&language=%s", baseURL, mediaType, c.apiKey, url.QueryEscape(query), language)
			var resp tmdbSearchResponse
			if err := c.doRequestWithRetry(ctx, searchURL, &resp); err != nil {
				return err
			}
			for _, result := range resp.Results {
				if err := ctx.Err(); err != nil {
					return err
				}
				key := fmt.Sprintf("%s:%d", mediaType, result.ID)
				if result.ID <= 0 || seen[key] {
					continue
				}
				seen[key] = true
				display, original := coalesce(result.Title, result.Name), coalesce(result.OriginalTitle, result.OriginalName)
				resultYear := 0
				date := coalesce(result.ReleaseDate, result.FirstAirDate)
				if len(date) >= 4 {
					resultYear, _ = strconv.Atoi(date[:4])
				}
				out = append(out, TMDBCandidate{TMDBID: result.ID, Title: display, OriginalTitle: original, Year: resultYear, MediaType: mediaType, Popularity: result.Popularity, Exact: queryNorms[normalizeForCompare(display)] || queryNorms[normalizeForCompare(original)]})
			}
		}
		return nil
	}
	if err := search(title, "en-US"); err != nil {
		return nil, err
	}
	hasYearMatch := func(items []TMDBCandidate) bool {
		if year == 0 {
			return true
		}
		for _, c := range items {
			if c.Year == 0 || abs(c.Year-year) <= 1 {
				return true
			}
		}
		return false
	}
	if len(out) == 0 || !hasYearMatch(out) {
		for _, transliterated := range transliterationVariants(title) {
			queryNorms[normalizeForCompare(transliterated)] = true
			if err := search(transliterated, "uk-UA"); err != nil {
				return nil, err
			}
			if len(out) == 0 || !hasYearMatch(out) {
				if err := search(transliterated, "ru-RU"); err != nil {
					return nil, err
				}
			}
			if len(out) == 0 || !hasYearMatch(out) {
				if err := search(transliterated, "en-US"); err != nil {
					return nil, err
				}
			}
			if len(out) > 0 && hasYearMatch(out) {
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Exact != b.Exact {
			return a.Exact
		}
		ady, bdy := abs(a.Year-year), abs(b.Year-year)
		if year > 0 && ady != bdy {
			return ady < bdy
		}
		if a.Popularity != b.Popularity {
			return a.Popularity > b.Popularity
		}
		if a.MediaType != b.MediaType {
			return a.MediaType == MediaTypeTV
		}
		return a.TMDBID < b.TMDBID
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out, nil
}

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

// tmdbSearchResponse is shared by typed /search/movie and /search/tv responses.
type tmdbSearchResponse struct {
	Results []tmdbSearchResult `json:"results"`
}

// scoredResult — кандидат з підрахованим балом
type scoredResult struct {
	result        tmdbSearchResult
	score         int
	identityScore int
	year          int
	matchedAlias  string
	alternatives  []string
}

// SearchExactTitle resolves an authoritative title supplied by the user.
// Filename-derived year/type are tie-breakers only and can never reject an exact title.
func (c *Client) SearchExactTitle(ctx context.Context, title string, year int, preferredType MediaType, strictType bool, originalFilename string) (*MovieInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}
	norm := normalizeForCompare(title)
	var best *scoredResult
	var exactCandidates []*scoredResult
	endpoints := []struct {
		name      string
		typeValue MediaType
	}{{"movie", MediaTypeMovie}, {"tv", MediaTypeTV}}
	if strictType {
		if preferredType == MediaTypeTV {
			endpoints = endpoints[1:]
		} else {
			endpoints = endpoints[:1]
		}
	}
	for _, ep := range endpoints {
		searchURL := fmt.Sprintf("%s/search/%s?api_key=%s&query=%s&language=en-US", baseURL, ep.name, c.apiKey, url.QueryEscape(title))
		var resp tmdbSearchResponse
		if err := c.doRequestWithRetry(ctx, searchURL, &resp); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		for index, result := range resp.Results {
			result.MediaType = string(ep.typeValue)
			score, resultYear, exact := manualCandidateScore(result, norm, year, preferredType)
			matchedAlias := ""
			if exact && normalizeForCompare(coalesce(result.OriginalTitle, result.OriginalName)) != norm {
				matchedAlias = coalesce(result.Title, result.Name)
			}
			if !exact && index < 3 {
				aliases, aliasErr := c.getAlternativeTitles(ctx, result.ID, ep.typeValue)
				if aliasErr == nil {
					for _, alias := range aliases {
						if normalizeForCompare(alias) == norm {
							exact = true
							matchedAlias = alias
							score, resultYear = manualAliasScore(result, year, preferredType)
							break
						}
					}
				}
			}
			if !exact {
				continue
			}
			identityScore := exactFilenameContextScore(originalFilename, result)
			score += identityScore
			candidate := &scoredResult{result: result, score: score, identityScore: identityScore, year: resultYear, matchedAlias: matchedAlias}
			exactCandidates = append(exactCandidates, candidate)
			if best == nil || candidate.score > best.score || (candidate.score == best.score && candidate.result.ID < best.result.ID) {
				best = candidate
			}
		}
	}
	if best == nil {
		return nil, nil
	}
	detailType := MediaType(best.result.MediaType)
	utils.LoggerWithTrace(ctx).Info("manual_exact_candidate_selected",
		slog.String("query", title), slog.Int("candidate_count", len(exactCandidates)),
		slog.Int("tmdb_id", best.result.ID), slog.String("preferred_media_type", string(preferredType)),
		slog.String("selected_media_type", string(detailType)), slog.Int("identity_score", best.identityScore),
		slog.Int("score", best.score), slog.Int("year", best.year))
	info, err := c.GetDetails(ctx, detailType, best.result.ID, originalFilename)
	if err == nil && info != nil {
		info.VerificationScore = deterministicVerificationScore(best.score)
		if detailType != preferredType {
			info.NeedsReview, info.ReviewReason = true, "media_type_conflict"
		} else if year > 0 && best.year > 0 && abs(best.year-year) > 1 {
			info.NeedsReview, info.ReviewReason = true, "year_conflict"
		} else if info.VerificationScore < ReviewVerificationThreshold {
			info.NeedsReview, info.ReviewReason = true, "low_verification_score"
		}
		info.SearchTitle = title
		info.MatchedAlias = best.matchedAlias
		sort.Slice(exactCandidates, func(i, j int) bool {
			if exactCandidates[i].score != exactCandidates[j].score {
				return exactCandidates[i].score > exactCandidates[j].score
			}
			return exactCandidates[i].result.ID < exactCandidates[j].result.ID
		})
		info.AmbiguousExact = len(exactCandidates) > 1 && exactCandidates[0].result.ID != exactCandidates[1].result.ID && exactCandidates[0].score-exactCandidates[1].score <= 10
	}
	return info, err
}

func manualAliasScore(result tmdbSearchResult, targetYear int, preferredType MediaType) (int, int) {
	// Every exact validated title has the same base authority.
	result.Title = ""
	result.Name = ""
	result.OriginalTitle = ""
	result.OriginalName = ""
	resultYear := 0
	date := coalesce(result.ReleaseDate, result.FirstAirDate)
	if len(date) >= 4 {
		resultYear, _ = strconv.Atoi(date[:4])
	}
	score := 1000 + popularityTieBreak(result.Popularity)
	if MediaType(result.MediaType) == preferredType {
		score += 30
	}
	if targetYear > 0 && resultYear > 0 {
		if diff := abs(targetYear - resultYear); diff == 0 {
			score += 100
		} else if diff == 1 {
			score += 50
		}
	}
	return score, resultYear
}

func manualCandidateScore(result tmdbSearchResult, normTitle string, targetYear int, preferredType MediaType) (int, int, bool) {
	display := normalizeForCompare(coalesce(result.Title, result.Name))
	original := normalizeForCompare(coalesce(result.OriginalTitle, result.OriginalName))
	if display != normTitle && original != normTitle {
		return 0, 0, false
	}
	resultYear := 0
	date := coalesce(result.ReleaseDate, result.FirstAirDate)
	if len(date) >= 4 {
		resultYear, _ = strconv.Atoi(date[:4])
	}
	score := 1000 + popularityTieBreak(result.Popularity)
	if MediaType(result.MediaType) == preferredType {
		score += 30
	}
	if targetYear > 0 && resultYear > 0 {
		switch diff := abs(targetYear - resultYear); diff {
		case 0:
			score += 100
		case 1:
			score += 50
		}
	}
	return score, resultYear, true
}

func popularityTieBreak(popularity float64) int {
	return min(int(popularity*10), 100)
}

// exactFilenameContextScore is only a tie-breaker between already exact title
// matches. A release-folder signature can therefore disambiguate homonyms but
// can never turn a fuzzy result into an accepted exact match.
func exactFilenameContextScore(originalFilename string, result tmdbSearchResult) int {
	if strings.TrimSpace(originalFilename) == "" {
		return 0
	}
	parsed := ParseFilename(originalFilename)
	contexts := generateTitleCandidates(parsed.CleanTitle, filepath.Base(originalFilename))
	contexts = append(contexts, cleanExactContextTitle(filepath.Base(originalFilename)))
	parent := filepath.Base(filepath.Dir(originalFilename))
	if parent != "." && !genericParentDirs[strings.ToLower(strings.TrimSpace(parent))] {
		parentParsed := ParseFilename(parent)
		contexts = append(contexts, generateTitleCandidates(parentParsed.CleanTitle, parent)...)
		contexts = append(contexts, cleanExactContextTitle(parent))
	}
	display := coalesce(result.Title, result.Name)
	original := coalesce(result.OriginalTitle, result.OriginalName)
	best := 0.0
	for _, contextTitle := range contexts {
		best = max(best,
			TitleSimilarity(contextTitle, display), TitleSimilarity(contextTitle, original),
			contextTokenSimilarity(contextTitle, display), contextTokenSimilarity(contextTitle, original))
	}
	if best < 0.72 {
		return 0
	}
	return int(best * 175)
}

func contextTokenSimilarity(contextTitle, candidateTitle string) float64 {
	contextTokens := strings.Fields(normalizeForCompare(contextTitle))
	candidateTokens := strings.Fields(normalizeForCompare(candidateTitle))
	if len(contextTokens) == 0 || len(candidateTokens) == 0 {
		return 0
	}
	total := 0.0
	for _, contextToken := range contextTokens {
		best := 0.0
		for _, candidateToken := range candidateTokens {
			best = max(best, TitleSimilarity(contextToken, candidateToken))
		}
		total += best
	}
	return total / float64(len(contextTokens))
}

func cleanExactContextTitle(value string) string {
	value = strings.TrimSuffix(filepath.Base(value), filepath.Ext(value))
	value = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(value)
	value = reExactContextNoise.ReplaceAllString(value, " ")
	return strings.TrimSpace(reSpaces.ReplaceAllString(value, " "))
}

var genericParentDirs = map[string]bool{
	"movies": true, "movie": true, "series": true, "films": true, "film": true,
	"video": true, "videos": true, "media": true,
	"downloads": true, "download": true, "temp": true, "tmp": true,
	"кино": true, "кіно": true, "фільми": true, "фільм": true,
	"серіали": true, "серіал": true, "відео": true,
}

// SearchWithFallbacks — каскадний пошук з fallback-стратегіями.
// Послідовність спроб залежить від ParsedFile (мова, рік, тип).
// Gemini-fallback сюди НЕ входить — він на рівні вище (client.go).
func (c *Client) SearchWithFallbacks(
	ctx context.Context,
	parsed ParsedFile,
	originalFilename string,
) (*MovieInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	attempts := buildAttempts(parsed, originalFilename, c.mediaRootPath())

	for _, a := range attempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		logger := utils.LoggerWithTrace(ctx).With(slog.String("component", "tmdb_search"))
		logger.Info("search_attempt",
			slog.String("label", a.label),
			slog.String("query", a.query),
			slog.Int("query_year", a.queryYear),
			slog.Int("target_year", a.targetYear),
		)

		info, err := c.searchAndFetch(ctx, a.query, a.queryYear, a.targetYear, a.mediaType, originalFilename)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			logger.Warn("search_failed", slog.String("label", a.label), slog.Any("error", err))
			continue
		}
		if info != nil {
			logger.Info("search_success", slog.String("label", a.label), slog.String("title", info.TitleEN))
			return info, nil
		}

		// Після провалу CYR-пошуку для варіанта "Папка" — EN cascade через латинську транслітерацію.
		// Інакше cached null по кириличному запиту блокує EN-пошук (напр. "Скарпетта" → "Scarpetta").
		if a.label == "Папка" && hasCyrillicChars(a.query) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			latinQuery := cyrillicToLatin(a.query)
			if latinQuery != a.query {
				logger.Info("folder_en_fallback",
					slog.String("cyrillic", a.query),
					slog.String("latin", latinQuery),
				)
				info, err = c.searchAndFetch(ctx, latinQuery, a.queryYear, a.targetYear, a.mediaType, originalFilename)
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					logger.Warn("folder_en_fallback_failed", slog.Any("error", err))
					continue
				}
				if info != nil {
					logger.Info("search_success", slog.String("label", a.label+" EN"), slog.String("title", info.TitleEN))
					return info, nil
				}
			}
		}
	}

	return nil, nil
}

// searchAttempt — одна спроба пошуку
type searchAttempt struct {
	query      string
	queryYear  int
	targetYear int
	mediaType  MediaType
	label      string
}

// buildAttempts формує розумний список спроб пошуку, використовуючи кандидатів
func buildAttempts(parsed ParsedFile, originalFilename, mediaRoot string) []searchAttempt {
	year := parsed.Year
	mt := parsed.MediaType

	if mt == MediaTypeMovie && isCyrillicSeries(originalFilename) {
		mt = MediaTypeTV
		slog.Info("media_type_changed_to_tv", slog.String("filename", originalFilename))
	}

	candidates := generateTitleCandidates(parsed.CleanTitle, filepath.Base(originalFilename))
	var attempts []searchAttempt

	for i, title := range candidates {
		labelPrefix := "Кандидат"
		if i == 0 {
			labelPrefix = "Базовий"
		}

		if year > 0 {
			attempts = append(attempts, searchAttempt{title, year, year, mt, labelPrefix + "+рік"})
		}
		attempts = append(attempts, searchAttempt{title, 0, year, mt, labelPrefix + " без року"})
	}

	if len(candidates) > 0 {
		bestTitle := candidates[0]
		opposite := MediaTypeMovie
		if mt == MediaTypeMovie {
			opposite = MediaTypeTV
		}
		attempts = append(attempts, searchAttempt{bestTitle, 0, year, opposite, "Протилежний тип"})
	}

	// 🟢 Додаємо фоллбек на батьківську папку з низьким пріоритетом.
	// Пропускаємо якщо ParentDir є коренем медіатеки або не несе інформації.
	if parsed.ParentDir != "" && parsed.ParentDir != "." && parsed.ParentDir != "/" &&
		!isScanRootParent(originalFilename, mediaRoot) &&
		!genericParentDirs[strings.ToLower(parsed.ParentDir)] {
		// Парсимо ім'я папки, щоб дістати рік і чисту назву
		dirParsed := ParseFilename(parsed.ParentDir)
		if dirParsed.CleanTitle != parsed.CleanTitle && len(dirParsed.CleanTitle) > 2 {
			attempts = append(attempts, searchAttempt{
				query:      dirParsed.CleanTitle,
				queryYear:  dirParsed.Year,
				targetYear: dirParsed.Year,
				mediaType:  dirParsed.MediaType,
				label:      "Папка",
			})
		}
	}

	return attempts
}

func isScanRootParent(filename, mediaRoot string) bool {
	if strings.TrimSpace(filename) == "" || strings.TrimSpace(mediaRoot) == "" {
		return false
	}

	filePath := filepath.Clean(filename)
	rootPath := filepath.Clean(mediaRoot)
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(rootPath, filePath)
	}

	parentPath := filepath.Clean(filepath.Dir(filePath))
	rel, err := filepath.Rel(rootPath, parentPath)
	if err != nil {
		return false
	}
	return rel == "."
}

// searchAndFetch виконує один запит до TMDB, ранжує результати,
// і якщо знайшов переможця — витягує повні деталі.
// 🔴 КАСКАД ПОШУКУ: Шукаємо в обох індексах (UA + RU) для кириличних запитів
func (c *Client) searchAndFetch(
	ctx context.Context,
	query string,
	queryYear int,
	targetYear int,
	preferredType MediaType,
	originalFilename string,
) (*MovieInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	// 🟢 Спочатку перевіряємо кеш пошуку (уніфікований формат з client.go)
	cacheKey := SearchCacheKey{
		query:      strings.ToLower(query),
		queryYear:  queryYear,
		targetYear: targetYear,
		mediaType:  preferredType,
	}
	if val, ok := c.searchCache.Load(cacheKey); ok {
		// safe to ignore: only *MovieInfo values are stored in searchCache.
		info, _ := val.(*MovieInfo)
		utils.LoggerWithTrace(ctx).Debug("search_cache_hit", slog.String("query", query))
		return info, nil
	}

	// 🔴 КАСКАД ПОШУКУ: Шукаємо в обох індексах для кириличних запитів
	langs := []string{"en-US"}
	if hasCyrillicChars(query) {
		// Для кириличних запитів пробуємо спочатку UA, потім RU
		langs = []string{"uk-UA", "ru-RU"}
	}

	var bestGlobal *scoredResult
	logger := utils.LoggerWithTrace(ctx).With(slog.String("component", "tmdb_search_cascade"))

LANG_LOOP:
	for _, langParam := range langs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		type searchEndpoint struct {
			name             string
			yearParam        string
			defaultMediaType string
		}

		var endpoints []searchEndpoint
		switch preferredType {
		case MediaTypeMovie:
			yearParam := ""
			if queryYear > 0 {
				yearParam = fmt.Sprintf("&year=%d", queryYear)
			}
			endpoints = []searchEndpoint{{"movie", yearParam, "movie"}}
		case MediaTypeTV:
			yearParam := ""
			if queryYear > 0 {
				yearParam = fmt.Sprintf("&first_air_date_year=%d", queryYear)
			}
			endpoints = []searchEndpoint{{"tv", yearParam, "tv"}}
		default:
			movieYear := ""
			tvYear := ""
			if queryYear > 0 {
				movieYear = fmt.Sprintf("&year=%d", queryYear)
				tvYear = fmt.Sprintf("&first_air_date_year=%d", queryYear)
			}
			endpoints = []searchEndpoint{
				{"movie", movieYear, "movie"},
				{"tv", tvYear, "tv"},
			}
		}

		for _, ep := range endpoints {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			searchURL := fmt.Sprintf(
				"%s/search/%s?api_key=%s&query=%s&language=%s%s",
				baseURL, ep.name, c.apiKey, url.QueryEscape(query), langParam, ep.yearParam,
			)

			logger.Debug("search_attempt",
				slog.String("lang", langParam),
				slog.String("endpoint", ep.name),
				slog.String("url", maskAPIKey(searchURL)),
			)

			var resp tmdbSearchResponse
			if err := c.doRequestWithRetry(ctx, searchURL, &resp); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				logger.Debug("search_failed_for_lang", slog.String("lang", langParam), slog.Any("error", err))
				continue
			}

			for i := range resp.Results {
				if resp.Results[i].MediaType == "" {
					if preferredType != "" {
						resp.Results[i].MediaType = string(preferredType)
					} else {
						resp.Results[i].MediaType = ep.defaultMediaType
					}
				}
			}

			if len(resp.Results) == 0 {
				logger.Debug("no_results_for_lang", slog.String("lang", langParam), slog.String("endpoint", ep.name))
				continue
			}

			bestForLang := c.rankResults(ctx, resp.Results, query, targetYear, preferredType)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if bestForLang != nil {
				logger.Debug("best_for_lang",
					slog.String("lang", langParam),
					slog.String("endpoint", ep.name),
					slog.String("title", coalesce(bestForLang.result.Title, bestForLang.result.Name)),
					slog.Int("score", bestForLang.score),
				)

				if bestGlobal == nil || bestForLang.score > bestGlobal.score {
					bestGlobal = bestForLang
				}
				// Якщо український індекс дав достатній результат — зупиняємо каскад (руський індекс не запитувати)
				if langParam == "uk-UA" && bestForLang.score >= 140 {
					logger.Info("uk_win_threshold_reached",
						slog.String("lang", langParam),
						slog.Int("score", bestForLang.score),
					)
					break LANG_LOOP
				}
			}
		}
	}

	if bestGlobal == nil {
		logger.Debug("no_best_candidate_found")
		c.searchCache.Store(cacheKey, (*MovieInfo)(nil)) // Кешуємо негативний результат
		return nil, nil
	}

	logger.Info("candidate_ranking_summary",
		slog.String("title", coalesce(bestGlobal.result.Title, bestGlobal.result.Name)),
		slog.Int("score", bestGlobal.score),
		slog.Any("alternatives", bestGlobal.alternatives),
	)

	// Визначаємо фінальний MediaType для запиту деталей
	detailType := MediaTypeMovie
	if bestGlobal.result.MediaType == "tv" {
		detailType = MediaTypeTV
	}

	info, err := c.GetDetails(ctx, detailType, bestGlobal.result.ID, originalFilename)

	if err == nil && info != nil {
		info.VerificationScore = deterministicVerificationScore(bestGlobal.score)
		if detailType != preferredType {
			info.NeedsReview, info.ReviewReason = true, "media_type_conflict"
		} else if targetYear > 0 && bestGlobal.year > 0 && abs(bestGlobal.year-targetYear) > 1 {
			info.NeedsReview, info.ReviewReason = true, "year_conflict"
		} else if info.VerificationScore < ReviewVerificationThreshold {
			info.NeedsReview, info.ReviewReason = true, "low_verification_score"
		}
		if !hasCyrillicChars(query) {
			info.SearchTitle = coalesce(bestGlobal.result.Title, bestGlobal.result.Name)
		}
		if bestGlobal.matchedAlias != "" {
			info.MatchedAlias = bestGlobal.matchedAlias
		}
		c.searchCache.Store(cacheKey, info)
	}
	return info, err
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
	var ranked []scoredResult

	for i, res := range results {
		if ctx.Err() != nil {
			return nil
		}
		// Людей ігноруємо завжди
		if res.MediaType == "person" {
			continue
		}

		scored := c.scoreResult(ctx, res, i, normQuery, targetYear, preferredType)
		if scored.score > -1000 {
			ranked = append(ranked, scored)
		}
		if ctx.Err() != nil {
			return nil
		}

		utils.LoggerWithTrace(ctx).Debug("candidate_evaluated",
			slog.String("title", coalesce(res.Title, res.Name)),
			slog.String("orig_title", coalesce(res.OriginalTitle, res.OriginalName)),
			slog.Int("year", scored.year),
			slog.String("lang", res.OriginalLanguage),
			slog.Int("score", scored.score),
		)

		// Мова та популярність є лише tie-breaker: вони не можуть перекрити
		// суттєво кращий збіг назви, року й типу.
		if best == nil || scored.identityScore > best.identityScore+IdentityTieWindow ||
			(abs(scored.identityScore-best.identityScore) <= IdentityTieWindow && scored.score > best.score) {
			best = &scored
		}
	}

	if best == nil {
		return nil
	}

	// Динамічний поріг: пом'якшуємо вимоги, особливо для транслітерації
	threshold := SearchThresholdDefault

	if targetYear > 0 {
		if best.year == targetYear {
			// Якщо рік ідеально збігається, ми можемо довіряти fuzzy-збігу назви
			threshold = SearchThresholdExactYear
		} else if abs(best.year-targetYear) == 1 {
			// Рік відрізняється на 1 (норма для релізів) - толерантний поріг
			threshold = SearchThresholdAdjacentYear
		} else {
			// Рік не збігається серйозно, але був у запиті — будьмо обережніші
			threshold = SearchThresholdConflictYear
		}
	}

	if best.score < threshold {
		utils.LoggerWithTrace(ctx).Debug("best_candidate_rejected",
			slog.String("title", coalesce(best.result.Title, best.result.Name)),
			slog.Int("score", best.score),
			slog.Int("threshold", threshold),
		)
		return nil
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].result.ID < ranked[j].result.ID
	})
	for _, candidate := range ranked {
		if candidate.result.ID == best.result.ID {
			continue
		}
		best.alternatives = append(best.alternatives, coalesce(candidate.result.Title, candidate.result.Name))
		if len(best.alternatives) == 2 {
			break
		}
	}

	return best
}

func deterministicVerificationScore(score int) float64 {
	if score <= 0 {
		return 0
	}
	if score >= ScoreThreshold {
		return 1
	}
	return float64(score) / float64(ScoreThreshold)
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
	yearScore := 0
	langScore := 0

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
	var matchedAlias string
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
					matchedAlias = alt
					if altScore >= 150 { // Ідеальний збіг в аліасах
						break
					}
				}
			}
		}
	}

	// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Якщо назва (або її аліаси) взагалі ніяк не метчиться із запитом — це сміття. Жорстко відхиляємо.
	if titleScore == 0 {
		utils.LoggerWithTrace(ctx).Debug("candidate_hard_rejected",
			slog.String("query", normQuery),
			slog.String("title", coalesce(res.Title, res.Name)),
			slog.String("orig_title", coalesce(res.OriginalTitle, res.OriginalName)),
			slog.Int("year", resYear),
			slog.String("reason", "titleScore==0"),
		)
		return scoredResult{result: res, score: -1000, year: resYear}
	}

	score += titleScore

	// --- Збіг року ---
	if targetYear > 0 && resYear > 0 {
		switch diff := abs(targetYear - resYear); {
		case diff == 0:
			yearScore = ScoreYearExact
		case diff == 1:
			yearScore = ScoreYearDiffOne
		default:
			yearScore = ScoreYearDiffTooFar // від'ємне
		}
	} else if targetYear == 0 && resYear > 0 {
		// ⚖️ БАЛАНСУВАННЯ: Якщо рік файлу невідомий, віддаємо перевагу сучасним релізам.
		if resYear >= 2000 {
			yearScore = 15 // Бонус за сучасність
		} else if resYear < 1980 {
			yearScore = -30 // Штраф для дуже старих
		}
	}
	score += yearScore

	// --- Відповідність типу медіа ---
	if (preferredType == MediaTypeMovie && res.MediaType == "movie") ||
		(preferredType == MediaTypeTV && res.MediaType == "tv") {
		score += ScoreMediaTypeMatch
	}
	identityScore := score

	// --- Мова оригіналу ---
	queryIsCyrillic := hasCyrillicChars(normQuery)
	switch res.OriginalLanguage {
	case "uk":
		langScore = ScoreLangUA
	case "en":
		langScore = ScoreLangEN
	case "ru":
		if queryIsCyrillic {
			langScore = 10
		} else {
			if resYear > 0 && resYear < 2010 {
				langScore = ScoreLangRUOld // -300
			} else {
				langScore = ScoreLangRURecent // -50
			}
		}
	}
	score += langScore

	// --- Popularity ---
	popBonus := int(res.Popularity / 5)
	if popBonus > ScorePopularityLimit {
		popBonus = ScorePopularityLimit
	}
	score += popBonus

	utils.LoggerWithTrace(ctx).Debug("candidate_score_breakdown",
		slog.String("query", normQuery),
		slog.String("title", coalesce(res.Title, res.Name)),
		slog.Int("titleScore", titleScore),
		slog.Int("yearScore", yearScore),
		slog.Int("langScore", langScore),
		slog.Int("popBonus", popBonus),
		slog.Int("identityScore", identityScore),
		slog.Int("finalScore", score),
	)

	return scoredResult{result: res, score: score, identityScore: identityScore, year: resYear, matchedAlias: matchedAlias}
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
	return max(fuzzyTitle, fuzzyOrig)
}

// TitleSimilarity повертає Jaro-Winkler схожість нормалізованих назв (0.0–1.0).
func TitleSimilarity(a, b string) float64 {
	a = normalizeForCompare(a)
	b = normalizeForCompare(b)
	if a == "" || b == "" {
		return 0
	}
	return smetrics.JaroWinkler(a, b, 0.7, 4)
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
	reQuality           = regexp.MustCompile(`(?i)\b(1080p|720p|2160p|4k|8k|HDRip|BDRip|WEB-DLRip|WEB-DL|WEBRip|HDTV|HDTVRip|CAMRip|TS|DVDScr|DVDRip|BluRay|HDRezka|Line|\d{3,4}Mb)\b`)
	reCodec             = regexp.MustCompile(`(?i)\b(x264|x265|h264|h265|HEVC|AV1|AVC|XviD)\b`)
	reAudio             = regexp.MustCompile(`(?i)\b(AAC|DTS|AC3|DDP5\.1|Atmos|Dub|UkrDub|RusDub|MVO|DUB|AVO|L1|L2)\b`)
	reRelease           = regexp.MustCompile(`(?i)(-?seleZen|-?ivanes|-?RG[[:alnum:]]*|-?NNMClub|\bUkr\b|\bRus\b|\bEng\b|\[TC\])`)
	reReleaseByMarker   = regexp.MustCompile(`(?i)(?:^|[ ._-])by[ ._-]+`)
	reReleaseGroupTail  = regexp.MustCompile(`(?i)[._\s]+-[[:alnum:]][[:alnum:]_-]*$`)
	reTerminalPartOne   = regexp.MustCompile(`(?:^|\s)1$`)
	reExt               = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|mov)$`)
	reBrackets          = regexp.MustCompile(`\[.*?\]|\(.*?\)`)
	rePunct             = regexp.MustCompile(`[\._]`)
	reSpaces            = regexp.MustCompile(`\s{2,}`)
	reSeries            = regexp.MustCompile(`(?i)(\d{1,2}\s*сезон|сезон\s*\d{1,2}|серия\s*\d{1,3}|серии\s*\d{1,3}-\d{1,3}|Часть\s*\d)`)
	reExactContextNoise = regexp.MustCompile(`(?i)\b(?:mkv|mp4|avi|mov|HDTVRip|HDTV|WEB-DLRip|WEB-DL|WEBRip|HDRip|BDRip|BluRay|DVDRip|GeneralFilm|1080p|720p|2160p|x26[45]|h26[45]|HEVC|\d{1,3})\b`)
)

// cleanString замінює крапки/підкреслення на пробіли і прибирає зайві пробіли
func cleanString(s string) string {
	s = rePunct.ReplaceAllString(s, " ")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func hasReleaseEvidence(s string) bool {
	cleaned := cleanString(s)
	return reYear.MatchString(cleaned) && (reQuality.MatchString(cleaned) || reCodec.MatchString(cleaned) || reAudio.MatchString(cleaned) || reRelease.MatchString(cleaned))
}

func trimReleaseByTail(s string) string {
	matches := reReleaseByMarker.FindAllStringIndex(s, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		prefix := s[:matches[i][0]]
		if hasReleaseEvidence(prefix) {
			return prefix
		}
	}
	return s
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
	s = trimReleaseByTail(s)
	if hasReleaseEvidence(s) {
		s = reReleaseGroupTail.ReplaceAllString(s, "")
	}
	// Normalize dot/underscore separators before word-boundary tag regexps.
	// In regexp, '_' is a word character and would otherwise keep tags such
	// as _BDRip_AVO_ from matching their bounded forms.
	s = cleanString(s)
	s = reQuality.ReplaceAllString(s, "")
	s = reCodec.ReplaceAllString(s, "")
	s = reAudio.ReplaceAllString(s, "")
	s = reRelease.ReplaceAllString(s, "")

	noBrackets := reBrackets.ReplaceAllString(s, "")
	noBrackets = reYear.ReplaceAllString(noBrackets, "")
	noBrackets = cleanString(noBrackets)
	add(noBrackets)
	if reYear.MatchString(filename) && (reQuality.MatchString(filename) || reCodec.MatchString(filename) || reAudio.MatchString(filename) || reRelease.MatchString(filename)) {
		add(strings.TrimSpace(reTerminalPartOne.ReplaceAllString(noBrackets, "")))
	}

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
			unicode.Is(unicode.Cyrillic, r)

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
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
