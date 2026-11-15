package main

import (
	"context"
	"fmt"
	"strings"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/utils"
)

type localizationUpdate struct {
	Movie        storage.Movie
	TitleChanged bool
	PlotChanged  bool
}

func prepareLocalizationUpdate(movie storage.Movie, result ai.BulkTranslateItem) (localizationUpdate, string) {
	update := localizationUpdate{Movie: movie}
	outcome := localizationTitleOutcome(movie, result.Title)
	if outcome == "applied" || outcome == "original_selected" {
		update.Movie.TitleUA = result.Title
		update.TitleChanged = true
	}
	if result.Plot != "" && result.Plot != movie.Plot && !needsTranslation(result.Plot) {
		update.Movie.Plot = result.Plot
		update.PlotChanged = true
	}
	return update, outcome
}

func localizationSavedMessage(update localizationUpdate) string {
	title := update.Movie.TitleUA
	if update.TitleChanged && utils.IsGoodUkrainian(title) {
		if update.PlotChanged {
			return fmt.Sprintf("✅ Назву й опис локалізовано: '%s'", title)
		}
		return fmt.Sprintf("✅ Назву локалізовано: '%s'", title)
	}
	if update.PlotChanged {
		if title == update.Movie.TitleEN {
			return fmt.Sprintf("✅ Опис перекладено; оригінальну назву збережено: '%s'", title)
		}
		return fmt.Sprintf("✅ Опис перекладено; назву залишено без змін: '%s'", title)
	}
	return fmt.Sprintf("ℹ️ Оригінальну назву збережено: '%s'", title)
}

func localizationUnchangedMessage(movie storage.Movie, outcome string) string {
	switch outcome {
	case "original_preserved":
		return fmt.Sprintf("ℹ️ Оригінальну назву збережено: '%s'", movie.TitleUA)
	case "trusted_title_preserved", "unchanged":
		if utils.IsGoodUkrainian(movie.TitleUA) {
			return fmt.Sprintf("ℹ️ Українську назву збережено: '%s'", movie.TitleUA)
		}
	}
	return fmt.Sprintf("⚠️ Локалізація не дала придатних змін: '%s'", movie.TitleUA)
}

// Save and report as one batch. A failed transaction must never count as an
// update or emit a success message; diagnostics do not affect committed counts.
func (a *App) saveLocalizationUpdates(ctx context.Context, updates []localizationUpdate) (int, error) {
	if len(updates) == 0 {
		return 0, nil
	}
	movies := make([]storage.Movie, 0, len(updates))
	for _, update := range updates {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		movies = append(movies, update.Movie)
	}
	if err := a.db.SaveMoviesBatch(ctx, movies); err != nil {
		return 0, err
	}
	for _, update := range updates {
		if ctx.Err() != nil {
			break
		}
		a.logFront(localizationSavedMessage(update))
	}
	a.logLocalizationSnapshot(ctx, movies)
	return len(updates), nil
}

// Keep matching exact original titles as fallbacks while rejecting reasoning
// fragments and unrelated non-Ukrainian names before any catalog write.
func validLocalizationTitle(title, original string) bool {
	return title != "" && !strings.HasPrefix(strings.TrimSpace(title), "<think>") &&
		(utils.IsGoodUkrainian(title) || title == original)
}
