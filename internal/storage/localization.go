package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// The identity and source text define one completed localization attempt.
// Other metadata changes do not invalidate it. No catalog fields are overwritten.
func localizationFingerprint(movie Movie) string {
	data, _ := json.Marshal([]any{movie.TmdbID, movie.MediaType, movie.TitleUA, movie.TitleEN, movie.Plot})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// MarkLocalizationCompleted records only a still-current, verified snapshot.
// A concurrent edit must remain eligible for a fresh attempt.
func (db *DB) MarkLocalizationCompleted(ctx context.Context, movie Movie) error {
	if movie.TmdbID <= 0 {
		return nil
	}
	_, err := db.db.ExecContext(ctx, `UPDATE movies SET localization_fingerprint = ?
		WHERE filename = ? AND tmdb_id = ? AND media_type = ?
		AND title_ua = ? AND title_en = ? AND plot = ?`,
		localizationFingerprint(movie), movie.Filename, movie.TmdbID, movie.MediaType,
		movie.TitleUA, movie.TitleEN, movie.Plot)
	return err
}
