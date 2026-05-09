package tmdb

import (
	"context"
	"fmt"
	"strings"
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
	Credits       struct {
		Cast []tmdbNamedItem `json:"cast"`
	} `json:"credits"`
}

// tmdbTVDetails — відповідь TMDB для /tv/{id}
type tmdbTVDetails struct {
	ID           int             `json:"id"`
	Name         string          `json:"name"`
	OriginalName string          `json:"original_name"`
	FirstAirDate string          `json:"first_air_date"`
	Overview     string          `json:"overview"`
	PosterPath   string          `json:"poster_path"`
	Genres       []tmdbNamedItem `json:"genres"`
	Credits      struct {
		Cast []tmdbNamedItem `json:"cast"`
	} `json:"credits"`
}

// getMovieDetails отримує повні деталі фільму з TMDB (Каскад: UA -> RU -> EN)
func (c *Client) getMovieDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	langs := []string{"uk-UA", "ru-RU", "en-US"}
	var finalInfo *MovieInfo

	for _, lang := range langs {
		url := fmt.Sprintf("%s/movie/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
		var d tmdbMovieDetails

		if err := c.doRequestWithRetry(ctx, url, &d); err != nil {
			continue
		}

		if finalInfo == nil {
			finalInfo = &MovieInfo{
				TMDBID:    d.ID,
				TitleUA:   d.Title,
				TitleEN:   d.OriginalTitle,
				Year:      extractYearFromDate(d.ReleaseDate),
				Plot:      d.Overview,
				Genres:    joinGenres(d.Genres),
				Cast:      joinCast(d.Credits.Cast),
				MediaType: MediaTypeMovie,
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
		if isGoodUkrainian(finalInfo.TitleUA) && isGoodUkrainian(finalInfo.Plot) {
			break
		}
	}

	if finalInfo == nil {
		return nil, fmt.Errorf("не вдалося отримати деталі фільму %d", id)
	}

	if finalInfo.PosterURL != "" && originalFilename != "" {
		lp, _ := c.DownloadPoster(ctx, finalInfo.PosterURL, fmt.Sprintf("%d_%s", finalInfo.TMDBID, originalFilename))
		finalInfo.LocalPosterPath = lp
	}

	return finalInfo, nil
}

// getTVDetails отримує повні деталі серіалу з TMDB (Каскад: UA -> RU -> EN)
func (c *Client) getTVDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	langs := []string{"uk-UA", "ru-RU", "en-US"}
	var finalInfo *MovieInfo

	for _, lang := range langs {
		url := fmt.Sprintf("%s/tv/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
		var d tmdbTVDetails

		if err := c.doRequestWithRetry(ctx, url, &d); err != nil {
			continue
		}

		if finalInfo == nil {
			finalInfo = &MovieInfo{
				TMDBID:    d.ID,
				TitleUA:   d.Name,
				TitleEN:   d.OriginalName,
				Year:      extractYearFromDate(d.FirstAirDate),
				Plot:      d.Overview,
				Genres:    joinGenres(d.Genres),
				Cast:      joinCast(d.Credits.Cast),
				MediaType: MediaTypeTV,
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

		if isGoodUkrainian(finalInfo.TitleUA) && isGoodUkrainian(finalInfo.Plot) {
			break
		}
	}

	if finalInfo == nil {
		return nil, fmt.Errorf("не вдалося отримати деталі серіалу %d", id)
	}

	if finalInfo.PosterURL != "" && originalFilename != "" {
		lp, _ := c.DownloadPoster(ctx, finalInfo.PosterURL, fmt.Sprintf("%d_%s", finalInfo.TMDBID, originalFilename))
		finalInfo.LocalPosterPath = lp
	}

	return finalInfo, nil
}

// GetDetails — публічний диспетчер: викликає movie або tv залежно від типу.
// originalFilename використовується тільки для іменування файлу постера.
func (c *Client) GetDetails(ctx context.Context, mediaType MediaType, id int, originalFilename string) (*MovieInfo, error) {
	switch mediaType {
	case MediaTypeTV:
		return c.getTVDetails(ctx, id, originalFilename)
	default:
		// Невідомий тип → вважаємо фільмом (безпечний дефолт)
		return c.getMovieDetails(ctx, id, originalFilename)
	}
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
