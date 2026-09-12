package tmdb

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"movielist-app/internal/utils"
)

const maxCastMembers = 5

// Спільний тип для жанрів та акторів (вирішує проблему суворої типізації масивів)
type tmdbNamedItem struct {
	Name string `json:"name"`
}

// tmdbMovieDetails — відповідь TMDB для /movie/{id}
type tmdbMovieDetails struct {
	ID            int             `json:"id"`
	Title         string          `json:"title"`
	OriginalTitle string          `json:"original_title"`
	ReleaseDate   string          `json:"release_date"`
	Overview      string          `json:"overview"`
	PosterPath    string          `json:"poster_path"`
	Genres        []tmdbNamedItem `json:"genres"`
	VoteAverage   float64         `json:"vote_average"`
	VoteCount     int             `json:"vote_count"`
	Runtime       int             `json:"runtime"`
	Credits       struct {
		Cast []tmdbNamedItem `json:"cast"`
	} `json:"credits"`
}

// tmdbTVDetails — відповідь TMDB для /tv/{id}
type tmdbTVDetails struct {
	ID             int             `json:"id"`
	Name           string          `json:"name"`
	OriginalName   string          `json:"original_name"`
	FirstAirDate   string          `json:"first_air_date"`
	Overview       string          `json:"overview"`
	PosterPath     string          `json:"poster_path"`
	Genres         []tmdbNamedItem `json:"genres"`
	VoteAverage    float64         `json:"vote_average"`
	VoteCount      int             `json:"vote_count"`
	EpisodeRuntime []int           `json:"episode_run_time"`
	Credits        struct {
		Cast []tmdbNamedItem `json:"cast"`
	} `json:"credits"`
}

// getMovieDetails отримує повні деталі фільму з TMDB (Каскад: UA -> RU -> EN)
func (c *Client) getMovieDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	langs := []string{"uk-UA", "ru-RU", "en-US"}
	var finalInfo *MovieInfo
	titles := make(map[string]string, len(langs))
	plots := make(map[string]string, len(langs))

	for _, lang := range langs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		url := fmt.Sprintf("%s/movie/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
		var d tmdbMovieDetails

		if err := c.doRequestWithRetry(ctx, url, &d); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		titles[lang] = strings.TrimSpace(d.Title)
		plots[lang] = strings.TrimSpace(d.Overview)

		if finalInfo == nil {
			finalInfo = &MovieInfo{
				TMDBID:      d.ID,
				TitleUA:     d.Title,
				TitleEN:     d.OriginalTitle,
				Year:        extractYearFromDate(d.ReleaseDate),
				Plot:        d.Overview,
				Genres:      joinGenres(d.Genres),
				Cast:        joinCast(d.Credits.Cast),
				MediaType:   MediaTypeMovie,
				VoteAverage: d.VoteAverage,
				VoteCount:   d.VoteCount,
			}
			if d.PosterPath != "" {
				finalInfo.PosterURL = imageBaseURL + d.PosterPath
			}
		} else {
			// Якщо поточний опис порожній АБО він англійський, а новий - кириличний (RU) -> перезаписуємо!
			if finalInfo.Plot == "" || (!hasCyrillicChars(finalInfo.Plot) && hasCyrillicChars(d.Overview)) {
				finalInfo.Plot = d.Overview
			}

			// Те саме робимо для назви
			if finalInfo.TitleUA == "" || finalInfo.TitleUA == finalInfo.TitleEN || (!hasCyrillicChars(finalInfo.TitleUA) && hasCyrillicChars(d.Title)) {
				if d.Title != "" {
					finalInfo.TitleUA = d.Title
				}
			}

			if finalInfo.Genres == "" && len(d.Genres) > 0 {
				finalInfo.Genres = joinGenres(d.Genres)
			}
		}

		// Якщо зібрали якісні українські дані - можемо переривати цикл
		if utils.IsGoodUkrainian(titles["uk-UA"]) && plots["uk-UA"] != "" && utils.IsGoodUkrainian(plots["uk-UA"]) {
			utils.LoggerWithTrace(ctx).Debug("localization_selected",
				slog.String("language", lang),
				slog.String("title", finalInfo.TitleUA),
				slog.Int("movie_id", finalInfo.TMDBID),
			)
			break
		}
	}

	if finalInfo == nil {
		return nil, fmt.Errorf("не вдалося отримати деталі фільму %d", id)
	}
	finalInfo.TitleUA = preferredLocalizedText(titles["uk-UA"], titles["en-US"], titles["ru-RU"])
	finalInfo.Plot = preferredLocalizedText(plots["uk-UA"], plots["ru-RU"], plots["en-US"])

	if finalInfo.PosterURL != "" && originalFilename != "" {
		lp, err := c.DownloadPoster(ctx, finalInfo.PosterURL, fmt.Sprintf("%d_%s", finalInfo.TMDBID, originalFilename))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			utils.LoggerWithTrace(ctx).Warn("poster_download_failed", slog.Int("tmdb_id", finalInfo.TMDBID), slog.Any("error", err))
		}
		finalInfo.LocalPosterPath = lp
	}

	return finalInfo, nil
}

// getTVDetails отримує повні деталі серіалу з TMDB (Каскад: UA -> RU -> EN)
func (c *Client) getTVDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	langs := []string{"uk-UA", "ru-RU", "en-US"}
	var finalInfo *MovieInfo
	titles := make(map[string]string, len(langs))
	plots := make(map[string]string, len(langs))

	for _, lang := range langs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		url := fmt.Sprintf("%s/tv/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
		var d tmdbTVDetails

		if err := c.doRequestWithRetry(ctx, url, &d); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		titles[lang] = strings.TrimSpace(d.Name)
		plots[lang] = strings.TrimSpace(d.Overview)

		if finalInfo == nil {
			finalInfo = &MovieInfo{
				TMDBID:      d.ID,
				TitleUA:     d.Name,
				TitleEN:     d.OriginalName,
				Year:        extractYearFromDate(d.FirstAirDate),
				Plot:        d.Overview,
				Genres:      joinGenres(d.Genres),
				Cast:        joinCast(d.Credits.Cast),
				MediaType:   MediaTypeTV,
				VoteAverage: d.VoteAverage,
				VoteCount:   d.VoteCount,
			}
			if d.PosterPath != "" {
				finalInfo.PosterURL = imageBaseURL + d.PosterPath
			}
		} else {
			if finalInfo.Plot == "" || (!hasCyrillicChars(finalInfo.Plot) && hasCyrillicChars(d.Overview)) {
				finalInfo.Plot = d.Overview
			}
			if finalInfo.TitleUA == "" || finalInfo.TitleUA == finalInfo.TitleEN || (!hasCyrillicChars(finalInfo.TitleUA) && hasCyrillicChars(d.Name)) {
				if d.Name != "" {
					finalInfo.TitleUA = d.Name
				}
			}
			if finalInfo.Genres == "" && len(d.Genres) > 0 {
				finalInfo.Genres = joinGenres(d.Genres)
			}
		}

		if utils.IsGoodUkrainian(titles["uk-UA"]) && plots["uk-UA"] != "" && utils.IsGoodUkrainian(plots["uk-UA"]) {
			utils.LoggerWithTrace(ctx).Debug("localization_selected",
				slog.String("language", lang),
				slog.String("title", finalInfo.TitleUA),
				slog.Int("movie_id", finalInfo.TMDBID),
			)
			break
		}
	}

	if finalInfo == nil {
		return nil, fmt.Errorf("не вдалося отримати деталі серіалу %d", id)
	}
	finalInfo.TitleUA = preferredLocalizedText(titles["uk-UA"], titles["en-US"], titles["ru-RU"])
	finalInfo.Plot = preferredLocalizedText(plots["uk-UA"], plots["ru-RU"], plots["en-US"])

	if finalInfo.PosterURL != "" && originalFilename != "" {
		lp, err := c.DownloadPoster(ctx, finalInfo.PosterURL, fmt.Sprintf("%d_%s", finalInfo.TMDBID, originalFilename))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			utils.LoggerWithTrace(ctx).Warn("poster_download_failed", slog.Int("tmdb_id", finalInfo.TMDBID), slog.Any("error", err))
		}
		finalInfo.LocalPosterPath = lp
	}

	return finalInfo, nil
}

func preferredLocalizedText(ukrainian, firstFallback, secondFallback string) string {
	if !needsInternetTranslation(ukrainian) {
		return ukrainian
	}
	if firstFallback != "" {
		return firstFallback
	}
	return secondFallback
}

func needsInternetTranslation(text string) bool {
	language := utils.DetectTextLanguage(text)
	return language == utils.LanguageUnknown || language == utils.LanguageEnglish || language == utils.LanguageRussian
}

// GetDetails — публічний диспетчер: викликає movie або tv залежно від типу.
// originalFilename використовується тільки для іменування файлу постера.
func (c *Client) GetDetails(ctx context.Context, mediaType MediaType, id int, originalFilename string) (*MovieInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mediaType != MediaTypeTV {
		mediaType = MediaTypeMovie
	}
	key := fmt.Sprintf("%s:%d", mediaType, id)
	var info *MovieInfo
	if cached, ok := c.detailsCache.Load(key); ok {
		copy := *(cached.(*MovieInfo))
		info = &copy
		c.cacheHits.Add(1)
	} else {
		var err error
		if mediaType == MediaTypeTV {
			info, err = c.getTVDetails(ctx, id, "")
		} else {
			info, err = c.getMovieDetails(ctx, id, "")
		}
		if err != nil || info == nil {
			return info, err
		}
		copy := *info
		copy.LocalPosterPath = ""
		c.detailsCache.Store(key, &copy)
	}
	if info.PosterURL != "" && originalFilename != "" {
		lp, err := c.DownloadPoster(ctx, info.PosterURL, fmt.Sprintf("%d_%s", info.TMDBID, originalFilename))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			utils.LoggerWithTrace(ctx).Warn("poster_download_failed", slog.Int("tmdb_id", info.TMDBID), slog.Any("error", err))
		}
		info.LocalPosterPath = lp
	}
	return info, nil
}

// GetCandidateDetails returns on-demand preview data without credits, aliases,
// or a local poster download.
func (c *Client) GetCandidateDetails(ctx context.Context, mediaType MediaType, id int) (*CandidateDetails, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id <= 0 || (mediaType != MediaTypeMovie && mediaType != MediaTypeTV) {
		return nil, fmt.Errorf("invalid candidate identity")
	}
	key := fmt.Sprintf("candidate:%s:%d", mediaType, id)
	if cached, ok := c.candidateDetailsCache.Load(key); ok {
		copy := *(cached.(*CandidateDetails))
		c.cacheHits.Add(1)
		return &copy, nil
	}
	requestURL := fmt.Sprintf("%s/%s/%d?api_key=%s&language=uk-UA", baseURL, mediaType, id, c.apiKey)
	result := &CandidateDetails{TMDBID: id, MediaType: mediaType}
	if mediaType == MediaTypeMovie {
		var d tmdbMovieDetails
		if err := c.doRequestWithRetry(ctx, requestURL, &d); err != nil {
			return nil, err
		}
		if d.ID <= 0 {
			return nil, ErrNotFound
		}
		result.Title, result.OriginalTitle, result.ReleaseDate, result.Runtime = d.Title, d.OriginalTitle, d.ReleaseDate, d.Runtime
		result.Genres, result.Overview, result.VoteAverage, result.VoteCount = joinGenres(d.Genres), d.Overview, d.VoteAverage, d.VoteCount
		if d.PosterPath != "" {
			result.PosterURL = imageBaseURL + d.PosterPath
		}
		result.ShortFilm = result.Runtime > 0 && result.Runtime <= 40
	} else {
		var d tmdbTVDetails
		if err := c.doRequestWithRetry(ctx, requestURL, &d); err != nil {
			return nil, err
		}
		if d.ID <= 0 {
			return nil, ErrNotFound
		}
		result.Title, result.OriginalTitle, result.ReleaseDate = d.Name, d.OriginalName, d.FirstAirDate
		if len(d.EpisodeRuntime) > 0 {
			result.Runtime = d.EpisodeRuntime[0]
		}
		result.Genres, result.Overview, result.VoteAverage, result.VoteCount = joinGenres(d.Genres), d.Overview, d.VoteAverage, d.VoteCount
		if d.PosterPath != "" {
			result.PosterURL = imageBaseURL + d.PosterPath
		}
	}
	copy := *result
	c.candidateDetailsCache.Store(key, &copy)
	return result, nil
}

// --- helpers ---

var genreTranslations = map[string]string{
	"Action":              "Бойовик",
	"Adventure":           "Пригоди",
	"Animation":           "Анімація",
	"Comedy":              "Комедія",
	"Crime":               "Кримінал",
	"Documentary":         "Документальний",
	"Drama":               "Драма",
	"Family":              "Сімейний",
	"Fantasy":             "Фентезі",
	"History":             "Історія",
	"Horror":              "Жахи",
	"Music":               "Музика",
	"Mystery":             "Детектив",
	"Romance":             "Мелодрама",
	"Science Fiction":     "Фантастика",
	"Sci-Fi & Fantasy":    "Фантастика",
	"TV Movie":            "Телефільм",
	"Thriller":            "Трилер",
	"War":                 "Військовий",
	"Western":             "Вестерн",
	"Action & Adventure":  "Бойовик і пригоди",
	"Kids":                "Дитячий",
	"News":                "Новини",
	"Reality":             "Реаліті-шоу",
	"Soap":                "Мильна опера",
	"Talk":                "Ток-шоу",
	"War & Politics":      "Війна і політика",
	"боевик":              "Бойовик",
	"приключения":         "Пригоди",
	"мультфильм":          "Анімація",
	"комедия":             "Комедія",
	"криминал":            "Кримінал",
	"документальный":      "Документальний",
	"драма":               "Драма",
	"семейный":            "Сімейний",
	"фэнтези":             "Фентезі",
	"история":             "Історія",
	"ужасы":               "Жахи",
	"музыка":              "Музика",
	"детектив":            "Детектив",
	"мелодрама":           "Мелодрама",
	"фантастика":          "Фантастика",
	"телевизионный фильм": "Телефільм",
	"триллер":             "Трилер",
	"военный":             "Військовий",
	"вестерн":             "Вестерн",
	"ток-шоу":             "Ток-шоу",
	"новости":             "Новини",
}

func translateGenre(name string) string {
	lowerName := strings.ToLower(name)
	if val, ok := genreTranslations[name]; ok {
		return val
	}
	if val, ok := genreTranslations[lowerName]; ok {
		return val
	}
	return name
}

func extractYearFromDate(date string) string {
	if len(date) >= 4 {
		return date[:4]
	}
	return ""
}

func joinGenres(items []tmdbNamedItem) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Name != "" {
			names = append(names, translateGenre(item.Name))
		}
	}
	return strings.Join(names, ", ")
}

func joinCast(cast []tmdbNamedItem) string {
	names := make([]string, 0, maxCastMembers)
	for i, member := range cast {
		if i >= maxCastMembers {
			break
		}
		if member.Name != "" {
			names = append(names, member.Name)
		}
	}
	return strings.Join(names, ", ")
}
