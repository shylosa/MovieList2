package main

import (
	"context"
	"log/slog"
	"strings"

	"movielist-app/internal/storage"
	"movielist-app/internal/utils"
)

func localizationTitleOutcome(movie storage.Movie, title string) string {
	if title == "" {
		return "empty"
	}
	if strings.HasPrefix(strings.TrimSpace(title), "<think>") {
		return "reasoning_rejected"
	}
	if title == movie.TitleUA {
		if title == movie.TitleEN && needsTranslation(title) {
			return "original_preserved"
		}
		return "unchanged"
	}
	if utils.IsGoodUkrainian(movie.TitleUA) {
		return "trusted_title_preserved"
	}
	if utils.HasCyrillic(movie.TitleUA) && !utils.HasCyrillic(title) {
		return "downgrade_rejected"
	}
	if !validLocalizationTitle(title, movie.TitleEN) {
		return "non_ukrainian_rejected"
	}
	if title == movie.TitleEN {
		return "original_selected"
	}
	return "applied"
}

func (a *App) logLocalizationSnapshot(ctx context.Context, movies []storage.Movie) {
	logger := utils.LoggerWithTrace(ctx)
	if !logger.Enabled(ctx, slog.LevelDebug) || ctx.Err() != nil {
		return
	}
	filenames := make([]string, 0, len(movies))
	for _, movie := range movies {
		if ctx.Err() != nil {
			return
		}
		filenames = append(filenames, movie.Filename)
	}
	saved, err := a.db.GetMoviesByFilenames(ctx, filenames)
	if err != nil {
		logger.Debug("localization_snapshot_failed", slog.Bool("cancelled", ctx.Err() != nil))
		return
	}
	for _, filename := range filenames {
		if ctx.Err() != nil {
			return
		}
		if movie, ok := saved[filename]; ok {
			logger.Debug("localization_saved", slog.String("file", filename), slog.Int("tmdb_id", movie.TmdbID),
				slog.String("media_type", movie.MediaType), slog.String("title_ua", movie.TitleUA),
				slog.Bool("title_ukrainian", utils.IsGoodUkrainian(movie.TitleUA)))
		}
	}
}
