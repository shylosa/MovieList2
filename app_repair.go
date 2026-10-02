package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
	"movielist-app/internal/utils"
)

type MetadataRepairResult struct {
	Movie   storage.Movie `json:"movie"`
	Warning string        `json:"warning,omitempty"`
}

// RepairMetadata deliberately has no access to recognition, filename parsing or search.
func (a *App) RepairMetadata(filename string) (*MetadataRepairResult, error) {
	started := time.Now()
	a.repairMutex.Lock()
	if a.repairClosing || a.ctx == nil || a.ctx.Err() != nil {
		a.repairMutex.Unlock()
		return nil, fmt.Errorf("застосунок завершує роботу")
	}
	a.scanMutex.Lock()
	if a.isScanning {
		a.scanMutex.Unlock()
		a.repairMutex.Unlock()
		return nil, fmt.Errorf("дочекайтеся завершення поточної операції")
	}
	a.isScanning = true // Serialize API work with scan/manual correction and other repairs.
	a.scanMutex.Unlock()
	ctx, cancel := context.WithCancel(utils.EnsureTrace(a.ctx))
	a.repairCancel = cancel
	a.wg.Add(1)
	a.repairMutex.Unlock()
	defer func() {
		cancel()
		a.repairMutex.Lock()
		a.repairCancel = nil
		a.repairMutex.Unlock()
		a.scanMutex.Lock()
		a.isScanning = false
		a.scanMutex.Unlock()
		a.wg.Done()
	}()
	before, err := a.db.GetMovieByFilename(ctx, filename)
	if err != nil {
		return nil, err
	}
	if before == nil || before.TmdbID <= 0 || (before.MediaType != "movie" && before.MediaType != "tv") {
		return nil, fmt.Errorf("для оновлення потрібні підтверджені TMDB ID і тип; спочатку визначте запис у редакторі")
	}
	fetch := a.repairDetailsFetcher
	if fetch == nil {
		if a.tmdbClient == nil {
			return nil, fmt.Errorf("TMDB недоступний")
		}
		fetch = a.tmdbClient.RefreshDetails
	}
	info, err := fetch(ctx, tmdb.MediaType(before.MediaType), before.TmdbID)
	if err != nil {
		return nil, fmt.Errorf("не вдалося оновити метадані TMDB: %w", err)
	}
	if info == nil || info.TMDBID != before.TmdbID || string(info.MediaType) != before.MediaType {
		return nil, fmt.Errorf("TMDB повернув інший запис; оновлення скасовано")
	}
	after, item := prepareMetadataRepair(*before, info)
	warnings := []string{}
	if item.Title != "" || item.Plot != "" {
		translate := a.repairTranslator
		if translate == nil && a.aiClient != nil {
			translate = a.aiClient.TranslateMetadata
		}
		if translate == nil {
			warnings = append(warnings, "Переклад недоступний.")
		} else {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			results, translateErr := translate(ctx, []ai.BulkTranslateItem{item})
			if translateErr != nil {
				utils.LoggerWithTrace(ctx).Warn("metadata_repair_translation_failed", slog.String("file", filename), slog.Any("error", translateErr))
				warnings = append(warnings, "Не вдалося завершити українську локалізацію.")
			} else if !applyRepairTranslation(&after, item, results) {
				warnings = append(warnings, "Переклад неповний або не пройшов перевірку.")
			}
		}
	}
	if item.Title == "" && !utils.IsGoodUkrainian(after.TitleUA) && strings.TrimSpace(info.TitleUA) == "" && strings.TrimSpace(info.TitleEN) == "" {
		warnings = append(warnings, "TMDB не має назви для локалізації.")
	}
	if item.Plot == "" && !utils.IsGoodUkrainian(after.Plot) {
		warnings = append(warnings, "TMDB не має опису для перекладу.")
	}
	if info.PosterURL != "" && a.tmdbClient != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Poster URLs identify TMDB image versions. Include that version in the
		// cache key so a newly selected image never reuses the old local poster.
		version := sha256.Sum256([]byte(info.PosterURL))
		path, posterErr := a.tmdbClient.DownloadPoster(ctx, info.PosterURL, fmt.Sprintf("%d_%x_%s", before.TmdbID, version[:8], filename))
		if posterErr != nil {
			warnings = append(warnings, "Не вдалося оновити локальний постер.")
		} else if path != "" {
			after.LocalPosterPath = path
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.db.SaveRepairedMovie(ctx, *before, after); err != nil {
		return nil, err
	}
	after.FileLabel = utils.DisplayFileLabel(filename)
	a.emitEvent(a.ctx, "movie-updated", map[string]any{"filename": filename, "tmdb_id": before.TmdbID})
	utils.LoggerWithTrace(ctx).Info("metadata_repaired",
		slog.String("file", filename), slog.Int("tmdb_id", before.TmdbID),
		slog.Int("before_tmdb_id", before.TmdbID), slog.Int("after_tmdb_id", after.TmdbID),
		slog.String("before_media_type", before.MediaType), slog.String("after_media_type", after.MediaType),
		slog.Float64("before_verification_score", before.VerificationScore), slog.Float64("after_verification_score", after.VerificationScore),
		slog.Bool("before_needs_review", before.NeedsReview), slog.Bool("after_needs_review", after.NeedsReview),
		slog.String("before_review_reason", before.ReviewReason), slog.String("after_review_reason", after.ReviewReason),
		slog.Any("changed_fields", repairedFieldNames(*before, after)),
		slog.String("title_source", repairLocalizationSource(before.TitleUA, info.TitleUA, after.TitleUA, item.Title)),
		slog.String("plot_source", repairLocalizationSource(before.Plot, info.Plot, after.Plot, item.Plot)),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()), slog.Int("warnings", len(warnings)))
	return &MetadataRepairResult{Movie: after, Warning: strings.Join(warnings, " ")}, nil
}

func repairedFieldNames(before, after storage.Movie) []string {
	fields := []string{}
	a, b := reflect.ValueOf(before), reflect.ValueOf(after)
	for i := 0; i < a.NumField(); i++ {
		name := strings.Split(a.Type().Field(i).Tag.Get("json"), ",")[0]
		if name != "file_label" && !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			fields = append(fields, name)
		}
	}
	return fields
}

func repairLocalizationSource(before, remote, after, requested string) string {
	if utils.IsGoodUkrainian(remote) && after == remote {
		return "tmdb"
	}
	if requested != "" && after != before && after != remote {
		return "ai"
	}
	if after == before && before != "" {
		return "stored"
	}
	if after == remote && remote != "" {
		return "tmdb_fallback"
	}
	return "missing"
}

func prepareMetadataRepair(before storage.Movie, info *tmdb.MovieInfo) (storage.Movie, ai.BulkTranslateItem) {
	after := before
	// Merge only owned metadata. Empty remote fields never erase usable values.
	for _, pair := range []struct {
		target *string
		source string
	}{
		{&after.TitleEN, info.TitleEN}, {&after.Year, info.Year},
		{&after.Cast, info.Cast}, {&after.PosterURL, info.PosterURL},
	} {
		if strings.TrimSpace(pair.source) != "" {
			*pair.target = pair.source
		}
	}
	if tmdb.HasLocalizedGenres(info.Genres) || (after.Genres == "" && info.Genres != "") {
		after.Genres = info.Genres
	}
	if info.VoteCount > 0 {
		after.VoteAverage, after.VoteCount = info.VoteAverage, info.VoteCount
	}
	item := ai.BulkTranslateItem{Filename: before.Filename}
	if utils.IsGoodUkrainian(info.TitleUA) {
		after.TitleUA = info.TitleUA
	} else if !utils.IsGoodUkrainian(before.TitleUA) {
		item.Title = strings.TrimSpace(info.TitleUA)
		if item.Title == "" {
			item.Title = strings.TrimSpace(info.TitleEN)
		}
		if item.Title != "" {
			item.OriginalTitle = info.TitleEN
		}
		if after.TitleUA == "" {
			after.TitleUA = item.Title
		}
	}
	if utils.IsGoodUkrainian(info.Plot) {
		after.Plot = info.Plot
	} else if !utils.IsGoodUkrainian(before.Plot) {
		item.Plot = strings.TrimSpace(info.Plot)
		if after.Plot == "" {
			after.Plot = item.Plot
		}
	}
	return after, item
}

func applyRepairTranslation(movie *storage.Movie, requested ai.BulkTranslateItem, results []ai.BulkTranslateItem) bool {
	if len(results) != 1 || results[0].Filename != requested.Filename {
		return false
	}
	result := results[0]
	complete := true
	if requested.Title != "" {
		title := strings.TrimSpace(result.Title)
		if validRepairLocalization(title) || (requested.OriginalTitle != "" && title == requested.OriginalTitle) {
			movie.TitleUA = title
		} else {
			complete = false
		}
	}
	if requested.Plot != "" {
		if validRepairLocalization(result.Plot) {
			movie.Plot = strings.TrimSpace(result.Plot)
		} else {
			complete = false
		}
	}
	return complete
}

func validRepairLocalization(text string) bool {
	text = strings.TrimSpace(text)
	return utils.IsGoodUkrainian(text) && !strings.Contains(strings.ToLower(text), "<think>") && !strings.Contains(text, "```")
}
