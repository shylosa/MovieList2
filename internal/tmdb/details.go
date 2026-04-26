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

// containsCyrillic перевіряє чи в тексті є кирилиця (українська або російська)
func containsCyrillic(s string) bool {
	return strings.ContainsAny(s, "абвгдеёжзийклмнопрстуфхцчшщъыьэюяАБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯіІїЇєЄґҐ")
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
				TMDBID:  d.ID,
				TitleUA: d.Title,
				TitleEN: d.OriginalTitle,
				Year:    extractYearFromDate(d.ReleaseDate),
				Plot:    d.Overview,
				Genres:  joinNames(d.Genres),
				Cast:    joinNames(d.Credits.Cast),
			}
			if d.PosterPath != "" {
				finalInfo.PosterURL = imageBaseURL + d.PosterPath
			}
		} else {
			// Якщо поточний опис порожній АБО він англійський, а новий - кириличний (RU) -> перезаписуємо!
			if finalInfo.Plot == "" || (!containsCyrillic(finalInfo.Plot) && containsCyrillic(d.Overview)) {
				finalInfo.Plot = d.Overview
			}

			// Те саме робимо для назви
			if finalInfo.TitleUA == "" || finalInfo.TitleUA == finalInfo.TitleEN || (!containsCyrillic(finalInfo.TitleUA) && containsCyrillic(d.Title)) {
				if d.Title != "" {
					finalInfo.TitleUA = d.Title
				}
			}

			if finalInfo.Genres == "" && len(d.Genres) > 0 {
				finalInfo.Genres = joinNames(d.Genres)
			}
		}

		// Якщо зібрали кириличні дані - можемо переривати цикл
		if containsCyrillic(finalInfo.Plot) && containsCyrillic(finalInfo.TitleUA) && finalInfo.Genres != "" {
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
				TMDBID:  d.ID,
				TitleUA: d.Name,
				TitleEN: d.OriginalName,
				Year:    extractYearFromDate(d.FirstAirDate),
				Plot:    d.Overview,
				Genres:  joinNames(d.Genres),
				Cast:    joinNames(d.Credits.Cast),
			}
			if d.PosterPath != "" {
				finalInfo.PosterURL = imageBaseURL + d.PosterPath
			}
		} else {
			if finalInfo.Plot == "" || (!containsCyrillic(finalInfo.Plot) && containsCyrillic(d.Overview)) {
				finalInfo.Plot = d.Overview
			}
			if finalInfo.TitleUA == "" || finalInfo.TitleUA == finalInfo.TitleEN || (!containsCyrillic(finalInfo.TitleUA) && containsCyrillic(d.Name)) {
				if d.Name != "" {
					finalInfo.TitleUA = d.Name
				}
			}
			if finalInfo.Genres == "" && len(d.Genres) > 0 {
				finalInfo.Genres = joinNames(d.Genres)
			}
		}

		if containsCyrillic(finalInfo.Plot) && containsCyrillic(finalInfo.TitleUA) && finalInfo.Genres != "" {
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

// extractYearFromDate витягує рік з рядка формату "2021-10-15"
func extractYearFromDate(date string) string {
	if len(date) >= 4 {
		return date[:4]
	}
	return ""
}

func joinNames(items []tmdbNamedItem) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Name != "" {
			names = append(names, item.Name)
		}
	}
	return strings.Join(names, ", ")
}

// joinCast збирає перших maxCastMembers акторів через кому
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
