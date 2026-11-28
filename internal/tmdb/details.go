package tmdb

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"movielist-app/internal/utils"
)

const maxCastMembers = 5

var errDetailsIdentity = errors.New("TMDB details identity mismatch")

type detailsIdentity struct {
	ID            int       `json:"id"`
	MediaType     MediaType `json:"media_type"`
	Title         *string   `json:"title"`
	OriginalTitle *string   `json:"original_title"`
	Name          *string   `json:"name"`
	OriginalName  *string   `json:"original_name"`
}

func (d detailsIdentity) verify(kind MediaType, id int) error {
	if d.ID != id || d.ID <= 0 {
		return fmt.Errorf("%w: requested %s:%d, received id %d", errDetailsIdentity, kind, id, d.ID)
	}
	if d.MediaType != "" && d.MediaType != kind {
		return fmt.Errorf("%w: requested %s:%d, received a different media_type", errDetailsIdentity, kind, id)
	}
	// Details usually omit media_type. Use the typed title fields as evidence
	// of the response shape, including fields present with empty strings.
	movie := d.Title != nil || d.OriginalTitle != nil
	tv := d.Name != nil || d.OriginalName != nil
	if (kind == MediaTypeMovie && (!movie || tv)) || (kind == MediaTypeTV && (!tv || movie)) {
		return fmt.Errorf("%w: requested %s:%d, response has incompatible title fields", errDetailsIdentity, kind, id)
	}
	return nil
}

func detailsTitle(title *string) string {
	if title == nil {
		return ""
	}
	return *title
}

// Спільний тип для жанрів та акторів (вирішує проблему суворої типізації масивів)
type tmdbNamedItem struct {
	Name string `json:"name"`
}

// tmdbMovieDetails — відповідь TMDB для /movie/{id}
type tmdbMovieDetails struct {
	detailsIdentity
	ReleaseDate string          `json:"release_date"`
	Overview    string          `json:"overview"`
	PosterPath  string          `json:"poster_path"`
	Genres      []tmdbNamedItem `json:"genres"`
	VoteAverage float64         `json:"vote_average"`
	VoteCount   int             `json:"vote_count"`
	Runtime     int             `json:"runtime"`
	Credits     struct {
		Cast []tmdbNamedItem `json:"cast"`
	} `json:"credits"`
}

// tmdbTVDetails — відповідь TMDB для /tv/{id}
type tmdbTVDetails struct {
	detailsIdentity
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

// A failed decode can populate some fields. Each HTTP retry must supply its
// own identity and metadata, even if a previous body was partially decoded.
func (d *tmdbMovieDetails) UnmarshalJSON(data []byte) error {
	type payload tmdbMovieDetails
	*d = tmdbMovieDetails{}
	return json.Unmarshal(data, (*payload)(d))
}

func (d *tmdbTVDetails) UnmarshalJSON(data []byte) error {
	type payload tmdbTVDetails
	*d = tmdbTVDetails{}
	return json.Unmarshal(data, (*payload)(d))
}

func (c *Client) loadMovieDetails(ctx context.Context, url string, id int) (tmdbMovieDetails, error) {
	var d tmdbMovieDetails
	if err := c.doRequestWithRetry(ctx, url, &d); err != nil {
		return d, err
	}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	return d, d.verify(MediaTypeMovie, id)
}

func (c *Client) loadTVDetails(ctx context.Context, url string, id int) (tmdbTVDetails, error) {
	var d tmdbTVDetails
	if err := c.doRequestWithRetry(ctx, url, &d); err != nil {
		return d, err
	}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	return d, d.verify(MediaTypeTV, id)
}

// getMovieDetails отримує повні деталі фільму з TMDB (Каскад: UA -> RU -> EN)
func (c *Client) getMovieDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	langs := []string{"uk-UA", "ru-RU", "en-US"}
	var finalInfo *MovieInfo
	var firstErr error
	titles := make(map[string]string, len(langs))
	plots := make(map[string]string, len(langs))

	for _, lang := range langs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		url := fmt.Sprintf("%s/movie/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
		d, err := c.loadMovieDetails(ctx, url, id)
		if err != nil {
			if canceled := searchCancellation(ctx, err); canceled != nil {
				return nil, canceled
			}
			if errors.Is(err, errDetailsIdentity) {
				return nil, err
			}
			firstErr = cmp.Or(firstErr, err)
			utils.LoggerWithTrace(ctx).Warn("tmdb_details_language_failed", slog.String("language", lang), slog.Any("error", err))
			continue
		}
		title := detailsTitle(d.Title)
		titles[lang] = strings.TrimSpace(title)
		plots[lang] = strings.TrimSpace(d.Overview)

		if finalInfo == nil {
			finalInfo = &MovieInfo{
				TMDBID:      d.ID,
				TitleUA:     title,
				TitleEN:     detailsTitle(d.OriginalTitle),
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
			if finalInfo.Plot == "" || (!utils.HasCyrillic(finalInfo.Plot) && utils.HasCyrillic(d.Overview)) {
				finalInfo.Plot = d.Overview
			}

			// Те саме робимо для назви
			if finalInfo.TitleUA == "" || finalInfo.TitleUA == finalInfo.TitleEN || (!utils.HasCyrillic(finalInfo.TitleUA) && utils.HasCyrillic(title)) {
				if title != "" {
					finalInfo.TitleUA = title
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
		return nil, fmt.Errorf("не вдалося отримати деталі фільму %d: %w", id, firstErr)
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

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return finalInfo, nil
}

// getTVDetails отримує повні деталі серіалу з TMDB (Каскад: UA -> RU -> EN)
func (c *Client) getTVDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	langs := []string{"uk-UA", "ru-RU", "en-US"}
	var finalInfo *MovieInfo
	var firstErr error
	titles := make(map[string]string, len(langs))
	plots := make(map[string]string, len(langs))

	for _, lang := range langs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		url := fmt.Sprintf("%s/tv/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
		d, err := c.loadTVDetails(ctx, url, id)
		if err != nil {
			if canceled := searchCancellation(ctx, err); canceled != nil {
				return nil, canceled
			}
			if errors.Is(err, errDetailsIdentity) {
				return nil, err
			}
			firstErr = cmp.Or(firstErr, err)
			utils.LoggerWithTrace(ctx).Warn("tmdb_details_language_failed", slog.String("language", lang), slog.Any("error", err))
			continue
		}
		title := detailsTitle(d.Name)
		titles[lang] = strings.TrimSpace(title)
		plots[lang] = strings.TrimSpace(d.Overview)

		if finalInfo == nil {
			finalInfo = &MovieInfo{
				TMDBID:      d.ID,
				TitleUA:     title,
				TitleEN:     detailsTitle(d.OriginalName),
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
			if finalInfo.Plot == "" || (!utils.HasCyrillic(finalInfo.Plot) && utils.HasCyrillic(d.Overview)) {
				finalInfo.Plot = d.Overview
			}
			if finalInfo.TitleUA == "" || finalInfo.TitleUA == finalInfo.TitleEN || (!utils.HasCyrillic(finalInfo.TitleUA) && utils.HasCyrillic(title)) {
				if title != "" {
					finalInfo.TitleUA = title
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
		return nil, fmt.Errorf("не вдалося отримати деталі серіалу %d: %w", id, firstErr)
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

	if err := ctx.Err(); err != nil {
		return nil, err
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

// loadTypedDetails — єдине завантаження повних деталей через typed movie/tv
// loaders. Кожен виклик звертається до TMDB; результат ніде не кешується.
func (c *Client) loadTypedDetails(ctx context.Context, mediaType MediaType, id int) (*MovieInfo, error) {
	if mediaType == MediaTypeTV {
		return c.getTVDetails(ctx, id, "")
	}
	return c.getMovieDetails(ctx, id, "")
}

// GetDetails — публічний диспетчер: викликає movie або tv залежно від типу.
// originalFilename використовується тільки для іменування файлу постера.
// Одночасні запити тієї самої identity об'єднує singleflight; повторний
// незалежний виклик завжди отримує свіжу відповідь TMDB.
func (c *Client) GetDetails(ctx context.Context, mediaType MediaType, id int, originalFilename string) (*MovieInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id <= 0 || (mediaType != MediaTypeMovie && mediaType != MediaTypeTV) {
		return nil, fmt.Errorf("valid TMDB identity required")
	}
	key := fmt.Sprintf("%s:%d", mediaType, id)
	result := c.detailsGroup.DoChan(key, func() (any, error) {
		return c.loadTypedDetails(ctx, mediaType, id)
	})
	var info *MovieInfo
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case loaded := <-result:
		if loaded.Err != nil || loaded.Val == nil {
			return nil, loaded.Err
		}
		// Кожен читач отримує власну копію: LocalPosterPath залежить від файла.
		copy := *(loaded.Val.(*MovieInfo))
		info = &copy
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return info, nil
}

// RefreshDetails only fetches the stored typed identity from TMDB.
// No filename parsing, search or type inference is allowed on this path.
func (c *Client) RefreshDetails(ctx context.Context, mediaType MediaType, id int) (*MovieInfo, error) {
	if id <= 0 || (mediaType != MediaTypeMovie && mediaType != MediaTypeTV) {
		return nil, fmt.Errorf("valid TMDB identity required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.loadTypedDetails(ctx, mediaType, id)
}

// GetCandidateDetails returns on-demand preview data without credits, aliases,
// or a local poster download. Кожне відкриття preview — свіжий запит до TMDB.
func (c *Client) GetCandidateDetails(ctx context.Context, mediaType MediaType, id int) (*CandidateDetails, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id <= 0 || (mediaType != MediaTypeMovie && mediaType != MediaTypeTV) {
		return nil, fmt.Errorf("invalid candidate identity")
	}
	requestURL := fmt.Sprintf("%s/%s/%d?api_key=%s&language=uk-UA", baseURL, mediaType, id, c.apiKey)
	result := &CandidateDetails{MediaType: mediaType}
	if mediaType == MediaTypeMovie {
		d, err := c.loadMovieDetails(ctx, requestURL, id)
		if err != nil {
			return nil, err
		}
		result.TMDBID = d.ID
		result.Title, result.OriginalTitle, result.ReleaseDate, result.Runtime = detailsTitle(d.Title), detailsTitle(d.OriginalTitle), d.ReleaseDate, d.Runtime
		result.Genres, result.Overview, result.VoteAverage, result.VoteCount = joinGenres(d.Genres), d.Overview, d.VoteAverage, d.VoteCount
		if d.PosterPath != "" {
			result.PosterURL = imageBaseURL + d.PosterPath
		}
		result.ShortFilm = result.Runtime > 0 && result.Runtime <= 40
	} else {
		d, err := c.loadTVDetails(ctx, requestURL, id)
		if err != nil {
			return nil, err
		}
		result.TMDBID = d.ID
		result.Title, result.OriginalTitle, result.ReleaseDate = detailsTitle(d.Name), detailsTitle(d.OriginalName), d.FirstAirDate
		if len(d.EpisodeRuntime) > 0 {
			result.Runtime = d.EpisodeRuntime[0]
		}
		result.Genres, result.Overview, result.VoteAverage, result.VoteCount = joinGenres(d.Genres), d.Overview, d.VoteAverage, d.VoteCount
		if d.PosterPath != "" {
			result.PosterURL = imageBaseURL + d.PosterPath
		}
	}
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

// HasLocalizedGenres also accepts known Ukrainian labels without distinctive
// alphabet markers (for example, "Драма"). Unknown Latin labels are rejected.
func HasLocalizedGenres(genres string) bool {
	if strings.TrimSpace(genres) == "" {
		return false
	}
	for _, genre := range strings.Split(genres, ",") {
		genre = strings.TrimSpace(genre)
		valid := utils.IsGoodUkrainian(genre)
		for _, localized := range genreTranslations {
			if genre == localized {
				valid = true
				break
			}
		}
		if !valid {
			return false
		}
	}
	return true
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
