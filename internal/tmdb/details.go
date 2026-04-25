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

// getMovieDetails отримує повні деталі фільму з TMDB
// getMovieDetails отримує повні деталі фільму з TMDB (UA -> RU -> EN)
func (c *Client) getMovieDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	// 1. Початковий запит УКРАЇНСЬКОЮ (з акторами)
	urlUA := fmt.Sprintf("%s/movie/%d?api_key=%s&language=uk-UA&append_to_response=credits", baseURL, id, c.apiKey)
	var d tmdbMovieDetails
	if err := c.doRequest(ctx, urlUA, &d); err != nil {
		return nil, err
	}

	// 2. КАСКАД: якщо опис або актори порожні — пробуємо інші мови
	fallbacks := []string{"ru-RU", "en-US"}
	for _, lang := range fallbacks {
		if strings.TrimSpace(d.Overview) == "" || len(d.Credits.Cast) == 0 {
			urlNext := fmt.Sprintf("%s/movie/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
			var dNext tmdbMovieDetails
			if err := c.doRequest(ctx, urlNext, &dNext); err == nil {
				// Дозаповнюємо тільки те, чого не вистачає
				if strings.TrimSpace(d.Overview) == "" && dNext.Overview != "" {
					d.Overview = dNext.Overview
				}
				if len(d.Credits.Cast) == 0 && len(dNext.Credits.Cast) > 0 {
					d.Credits.Cast = dNext.Credits.Cast
				}
			}
		} else {
			break // Все знайшли, виходимо
		}
	}

	info := &MovieInfo{
		TMDBID:    d.ID,
		TitleUA:   d.Title,
		TitleEN:   d.OriginalTitle,
		Year:      extractYearFromDate(d.ReleaseDate),
		Plot:      d.Overview,
		Genres:    joinNames(d.Genres),
		Cast:      joinCast(d.Credits.Cast),
		MediaType: MediaTypeMovie,
	}

	if d.PosterPath != "" {
		info.PosterURL = imageBaseURL + d.PosterPath
		if originalFilename != "" {
			lp, _ := c.DownloadPoster(ctx, info.PosterURL, fmt.Sprintf("%d_%s", d.ID, originalFilename))
			info.LocalPosterPath = lp
		}
	}
	return info, nil
}

// getTVDetails отримує деталі серіалу (UA -> RU -> EN)
func (c *Client) getTVDetails(ctx context.Context, id int, originalFilename string) (*MovieInfo, error) {
	urlUA := fmt.Sprintf("%s/tv/%d?api_key=%s&language=uk-UA&append_to_response=credits", baseURL, id, c.apiKey)
	var d tmdbTVDetails
	if err := c.doRequest(ctx, urlUA, &d); err != nil {
		return nil, err
	}

	fallbacks := []string{"ru-RU", "en-US"}
	for _, lang := range fallbacks {
		if strings.TrimSpace(d.Overview) == "" || len(d.Credits.Cast) == 0 {
			urlNext := fmt.Sprintf("%s/tv/%d?api_key=%s&language=%s&append_to_response=credits", baseURL, id, c.apiKey, lang)
			var dNext tmdbTVDetails
			if err := c.doRequest(ctx, urlNext, &dNext); err == nil {
				if strings.TrimSpace(d.Overview) == "" && dNext.Overview != "" {
					d.Overview = dNext.Overview
				}
				if len(d.Credits.Cast) == 0 && len(dNext.Credits.Cast) > 0 {
					d.Credits.Cast = dNext.Credits.Cast
				}
			}
		} else {
			break
		}
	}

	info := &MovieInfo{
		TMDBID:    d.ID,
		TitleUA:   d.Name,
		TitleEN:   d.OriginalName,
		Year:      extractYearFromDate(d.FirstAirDate),
		Plot:      d.Overview,
		Genres:    joinNames(d.Genres),
		Cast:      joinCast(d.Credits.Cast),
		MediaType: MediaTypeTV,
	}

	if d.PosterPath != "" {
		info.PosterURL = imageBaseURL + d.PosterPath
		if originalFilename != "" {
			lp, _ := c.DownloadPoster(ctx, info.PosterURL, fmt.Sprintf("%d_%s", d.ID, originalFilename))
			info.LocalPosterPath = lp
		}
	}
	return info, nil
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
