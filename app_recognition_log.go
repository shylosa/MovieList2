package main

import (
	"context"
	"log/slog"

	"movielist-app/internal/storage"
	"movielist-app/internal/utils"
)

// saveRecognitionBatch logs committed catalog state rather than submitted values:
// the merge upsert may preserve an already verified identity over a placeholder.
func (a *App) saveRecognitionBatch(ctx context.Context, movies []storage.Movie, stage string) error {
	_, err := a.commitRecognitionBatch(ctx, movies, stage, nil)
	return err
}

func (a *App) commitRecognitionBatch(ctx context.Context, movies []storage.Movie, stage string, retried map[string]bool) ([]storage.Movie, error) {
	if len(movies) == 0 {
		return nil, nil
	}
	if len(retried) > 0 {
		var err error
		movies, err = a.db.SaveRecognitionRetries(ctx, movies, retried)
		if err != nil {
			return nil, err
		}
	} else if err := a.db.SaveMoviesBatch(ctx, movies); err != nil {
		return nil, err
	}
	logger := utils.LoggerWithTrace(ctx).With(slog.String("stage", stage))
	logger.Info("batch_save_success", slog.Int("count", len(movies)))
	filenames := make([]string, 0, len(movies))
	for _, movie := range movies {
		if ctx.Err() != nil {
			return movies, nil // The commit succeeded; cancellation only skips diagnostics.
		}
		filenames = append(filenames, movie.Filename)
	}
	saved, err := a.db.GetMoviesByFilenames(ctx, filenames)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn("recognition_saved_snapshot_failed")
		}
		return movies, nil
	}
	seen := make(map[string]bool, len(movies))
	for _, filename := range filenames {
		if ctx.Err() != nil {
			return movies, nil
		}
		if seen[filename] {
			continue
		}
		seen[filename] = true
		movie, ok := saved[filename]
		if !ok {
			logger.Warn("recognition_saved_snapshot_missing", slog.String("file", filename))
			continue
		}
		logger.Info("recognition_saved",
			slog.String("file", movie.Filename),
			slog.Int("tmdb_id", movie.TmdbID),
			slog.String("media_type", movie.MediaType),
			slog.String("recognition_source", movie.RecognitionSource),
			slog.Bool("resolved", movie.TmdbID > 0),
			slog.Bool("needs_review", movie.NeedsReview),
			slog.String("review_reason", movie.ReviewReason),
			slog.Float64("verification_score", movie.VerificationScore))
	}
	return movies, nil
}
