package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"movielist-app/internal/utils"

	_ "modernc.org/sqlite"
)

// Movie describes the movie record structure in the database (unified)
type Movie struct {
	ID                    int     `json:"id"`
	Filename              string  `json:"filename"`
	FileLabel             string  `json:"file_label,omitempty"` // computed for UI; not stored in SQLite
	TmdbID                int     `json:"tmdb_id"`
	TitleUA               string  `json:"title_ua"`
	TitleEN               string  `json:"title_en"`
	Year                  string  `json:"year"`
	Plot                  string  `json:"plot"`
	Genres                string  `json:"genres"`
	Cast                  string  `json:"cast"`
	PosterURL             string  `json:"poster_url"`
	LocalPosterPath       string  `json:"local_poster_path"`
	MediaType             string  `json:"media_type"`
	RecognitionSource     string  `json:"recognition_source"`
	RecognitionConfidence float64 `json:"recognition_confidence"`
	VerificationScore     float64 `json:"verification_score"`
	NeedsReview           bool    `json:"needs_review"`
	ReviewReason          string  `json:"review_reason,omitempty"`
	VoteAverage           float64 `json:"vote_average"`
	VoteCount             int     `json:"vote_count"`
}

// AIResolution is the Gemini recognition cache entry (L2 Cache)
type AIResolution struct {
	OriginalFilename string
	ResolvedTitle    string
	Year             int
	MediaType        string
	Confidence       float64
	PipelineVersion  int
	Provider         string
	Model            string
	UpdatedAt        time.Time
}

type DB struct {
	db *sql.DB
}

const filenameChunkSize = 500

// movieUpsertQuery inserts a row or merges non-empty incoming fields into an existing row.
// Avoids INSERT OR REPLACE, which deletes the old row and wipes metadata when partial structs are saved.
const movieUpsertQuery = `
	INSERT INTO movies
		(filename, tmdb_id, title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, media_type,
		 recognition_source, recognition_confidence, verification_score, needs_review, review_reason, vote_average, vote_count)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(filename) DO UPDATE SET
		tmdb_id = CASE WHEN excluded.tmdb_id != 0 THEN excluded.tmdb_id ELSE movies.tmdb_id END,
		title_ua = CASE
			WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.title_ua
			WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.title_ua
			ELSE COALESCE(NULLIF(excluded.title_ua, ''), movies.title_ua)
		END,
		title_en = CASE
			WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.title_en
			WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.title_en
			ELSE COALESCE(NULLIF(excluded.title_en, ''), movies.title_en)
		END,
		year = CASE
			WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.year
			WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.year
			ELSE COALESCE(NULLIF(excluded.year, ''), movies.year)
		END,
		genres = CASE WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.genres ELSE COALESCE(NULLIF(excluded.genres, ''), movies.genres) END,
		"cast" = CASE WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded."cast" ELSE COALESCE(NULLIF(excluded."cast", ''), movies."cast") END,
		plot = CASE WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.plot ELSE COALESCE(NULLIF(excluded.plot, ''), movies.plot) END,
		poster_url = CASE WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.poster_url ELSE COALESCE(NULLIF(excluded.poster_url, ''), movies.poster_url) END,
		local_poster_path = CASE WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.local_poster_path ELSE COALESCE(NULLIF(excluded.local_poster_path, ''), movies.local_poster_path) END,
		media_type = COALESCE(NULLIF(excluded.media_type, ''), movies.media_type),
		recognition_source = CASE
			WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.recognition_source
			WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.recognition_source
			ELSE COALESCE(NULLIF(excluded.recognition_source, ''), movies.recognition_source)
		END,
		recognition_confidence = CASE WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.recognition_confidence ELSE excluded.recognition_confidence END,
		verification_score = CASE WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.verification_score ELSE excluded.verification_score END,
		needs_review = CASE WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.needs_review ELSE excluded.needs_review END,
		review_reason = CASE WHEN excluded.tmdb_id = 0 AND movies.tmdb_id > 0 THEN movies.review_reason ELSE excluded.review_reason END,
		vote_average = CASE WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.vote_average WHEN excluded.vote_average > 0 THEN excluded.vote_average ELSE movies.vote_average END,
		vote_count = CASE WHEN excluded.tmdb_id > 0 AND (excluded.tmdb_id != movies.tmdb_id OR (movies.media_type != '' AND excluded.media_type != '' AND excluded.media_type != movies.media_type)) THEN excluded.vote_count WHEN excluded.vote_count > 0 THEN excluded.vote_count ELSE movies.vote_count END
`

func New(dbPath string) (*DB, error) {
	// Ensure the SQLite DSN contains busy timeout and WAL journal mode to
	// reduce "database is locked" errors under concurrency. Use the
	// "file:" URI form when a plain path is provided.
	connStr := dbPath
	if !strings.HasPrefix(connStr, "file:") {
		// Normalize path separators for URI form
		connStr = "file:" + filepath.ToSlash(connStr) + "?_busy_timeout=5000&_journal_mode=WAL"
	} else {
		// If already a file: URI, append params if missing
		if !strings.Contains(connStr, "_busy_timeout=") {
			if strings.Contains(connStr, "?") {
				connStr = connStr + "&_busy_timeout=5000"
			} else {
				connStr = connStr + "?_busy_timeout=5000"
			}
		}
		if !strings.Contains(connStr, "_journal_mode=") {
			if strings.Contains(connStr, "?") {
				connStr = connStr + "&_journal_mode=WAL"
			} else {
				connStr = connStr + "?_journal_mode=WAL"
			}
		}
	}

	conn, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	return &DB{db: conn}, nil
}

func (db *DB) Close() error {
	return db.db.Close()
}

func (db *DB) InitSchema(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS movies (
		filename TEXT PRIMARY KEY,
		tmdb_id INTEGER, -- added column
		title_ua TEXT,
		title_en TEXT,
		year TEXT,
		genres TEXT,
		cast TEXT,
		plot TEXT,
		poster_url TEXT,
		local_poster_path TEXT,
		media_type TEXT
		, recognition_source TEXT NOT NULL DEFAULT ''
		, recognition_confidence REAL NOT NULL DEFAULT 0
		, verification_score REAL NOT NULL DEFAULT 0
		, needs_review INTEGER NOT NULL DEFAULT 0
		, review_reason TEXT NOT NULL DEFAULT ''
		, vote_average REAL NOT NULL DEFAULT 0
		, vote_count INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_tmdb_id ON movies(tmdb_id);
	CREATE INDEX IF NOT EXISTS idx_title_en ON movies(title_en);
	CREATE TABLE IF NOT EXISTS ai_resolutions (
		original_filename TEXT PRIMARY KEY,
		resolved_title TEXT,
		year INTEGER,
		media_type TEXT,
		confidence REAL,
		pipeline_version INTEGER NOT NULL DEFAULT 0,
		provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT ''
	);
	`
	_, err := db.db.ExecContext(ctx, query)
	if err != nil {
		return err
	}

	_, err = db.db.ExecContext(ctx, `
	CREATE TABLE IF NOT EXISTS app_state (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return fmt.Errorf("create app_state: %w", err)
	}

	var mode string
	if err := db.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL;").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		slog.Warn("wal_mode_unavailable", slog.String("actual_mode", mode))
	}
	if _, err := db.db.ExecContext(ctx, "PRAGMA synchronous = NORMAL;"); err != nil {
		return err
	}
	if _, err := db.db.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		return err
	}
	if _, err := db.db.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		return err
	}

	// Lazy migration for existing databases.
	// If the table already exists, explicitly add new columns.
	// We intentionally ignore errors here: if the column exists, SQLite returns "duplicate column name", which is OK.
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN tmdb_id INTEGER;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN media_type TEXT;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN recognition_source TEXT NOT NULL DEFAULT '';`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN recognition_confidence REAL NOT NULL DEFAULT 0;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN verification_score REAL NOT NULL DEFAULT 0;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN needs_review INTEGER NOT NULL DEFAULT 0;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN review_reason TEXT NOT NULL DEFAULT '';`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN vote_average REAL NOT NULL DEFAULT 0;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE movies ADD COLUMN vote_count INTEGER NOT NULL DEFAULT 0;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE ai_resolutions ADD COLUMN pipeline_version INTEGER NOT NULL DEFAULT 0;`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE ai_resolutions ADD COLUMN provider TEXT NOT NULL DEFAULT '';`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE ai_resolutions ADD COLUMN model TEXT NOT NULL DEFAULT '';`)
	_, _ = db.db.ExecContext(ctx, `ALTER TABLE ai_resolutions ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';`)

	return nil
}

func (db *DB) GetAllMovies(ctx context.Context) ([]Movie, error) {
	query := `SELECT rowid, filename, COALESCE(tmdb_id, 0), title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, COALESCE(media_type, ''), COALESCE(recognition_source, ''), COALESCE(recognition_confidence, 0), COALESCE(verification_score, 0), COALESCE(needs_review, 0), COALESCE(review_reason, ''), COALESCE(vote_average, 0), COALESCE(vote_count, 0) FROM movies ORDER BY rowid ASC`
	rows, err := db.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("GetAllMovies query failed: %w", err)
	}
	defer rows.Close()

	var movies []Movie
	for rows.Next() {
		var m Movie
		err := rows.Scan(
			&m.ID, &m.Filename, &m.TmdbID, &m.TitleUA, &m.TitleEN, &m.Year,
			&m.Genres, &m.Cast, &m.Plot, &m.PosterURL, &m.LocalPosterPath, &m.MediaType,
			&m.RecognitionSource, &m.RecognitionConfidence, &m.VerificationScore, &m.NeedsReview, &m.ReviewReason,
			&m.VoteAverage, &m.VoteCount,
		)
		if err != nil {
			return nil, fmt.Errorf("GetAllMovies scan failed: %w", err)
		}
		movies = append(movies, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetAllMovies rows iteration failed: %w", err)
	}
	if movies == nil {
		movies = []Movie{}
	}
	return movies, nil
}

func (db *DB) SaveMovie(ctx context.Context, m Movie) error {
	_, err := db.db.ExecContext(ctx, movieUpsertQuery,
		m.Filename, m.TmdbID, m.TitleUA, m.TitleEN, m.Year,
		m.Genres, m.Cast, m.Plot, m.PosterURL, m.LocalPosterPath, m.MediaType,
		m.RecognitionSource, m.RecognitionConfidence, m.VerificationScore, normalizedNeedsReview(m), m.ReviewReason,
		m.VoteAverage, m.VoteCount,
	)
	return err
}

// PatchMovie updates only non-zero / non-empty fields on an existing row.
// Inserts a new row when the filename is not present yet.
func (db *DB) PatchMovie(ctx context.Context, patch Movie) error {
	if patch.Filename == "" {
		return fmt.Errorf("PatchMovie: filename required")
	}
	existing, err := db.GetMovieByFilename(ctx, patch.Filename)
	if err != nil {
		return fmt.Errorf("PatchMovie lookup %q: %w", patch.Filename, err)
	}
	if existing == nil {
		return db.SaveMovie(ctx, patch)
	}
	merged := mergeMoviePatch(*existing, patch)
	return db.SaveMovie(ctx, merged)
}

func mergeMoviePatch(base, patch Movie) Movie {
	out := base
	if patch.TmdbID != 0 {
		out.TmdbID = patch.TmdbID
	}
	if patch.TitleUA != "" {
		out.TitleUA = patch.TitleUA
	}
	if patch.TitleEN != "" {
		out.TitleEN = patch.TitleEN
	}
	if patch.Year != "" {
		out.Year = patch.Year
	}
	if patch.Genres != "" {
		out.Genres = patch.Genres
	}
	if patch.Cast != "" {
		out.Cast = patch.Cast
	}
	if patch.Plot != "" {
		out.Plot = patch.Plot
	}
	if patch.PosterURL != "" {
		out.PosterURL = patch.PosterURL
	}
	if patch.LocalPosterPath != "" {
		out.LocalPosterPath = patch.LocalPosterPath
	}
	if patch.MediaType != "" {
		out.MediaType = patch.MediaType
	}
	if patch.RecognitionSource != "" {
		out.RecognitionSource = patch.RecognitionSource
	}
	if patch.RecognitionConfidence != 0 {
		out.RecognitionConfidence = patch.RecognitionConfidence
	}
	if patch.VerificationScore != 0 {
		out.VerificationScore = patch.VerificationScore
	}
	if patch.NeedsReview {
		out.NeedsReview = true
	}
	if patch.ReviewReason != "" {
		out.ReviewReason = patch.ReviewReason
	}
	if patch.VoteAverage > 0 {
		out.VoteAverage = patch.VoteAverage
	}
	if patch.VoteCount > 0 {
		out.VoteCount = patch.VoteCount
	}
	return out
}

func normalizedNeedsReview(m Movie) bool { return m.TmdbID == 0 || m.NeedsReview }

// SaveMoviesBatch — масовий запис через єдину транзакцію
func (db *DB) SaveMoviesBatch(ctx context.Context, movies []Movie) error {
	if len(movies) == 0 {
		return nil
	}

	// Begin transaction
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx failed: %w", err)
	}
	defer tx.Rollback() // safe no-op if Commit succeeds

	// Prepare statement for performance
	stmt, err := tx.PrepareContext(ctx, movieUpsertQuery)
	if err != nil {
		return fmt.Errorf("prepare stmt failed: %w", err)
	}
	defer stmt.Close()

	for _, m := range movies {
		if err := ctx.Err(); err != nil {
			utils.LoggerWithTrace(ctx).Debug("batch_insert_cancelled", slog.Any("error", err))
			return err
		}

		_, err := stmt.ExecContext(ctx,
			m.Filename, m.TmdbID, m.TitleUA, m.TitleEN, m.Year,
			m.Genres, m.Cast, m.Plot, m.PosterURL, m.LocalPosterPath, m.MediaType,
			m.RecognitionSource, m.RecognitionConfidence, m.VerificationScore, normalizedNeedsReview(m), m.ReviewReason,
			m.VoteAverage, m.VoteCount,
		)
		if err != nil {
			return fmt.Errorf("batch insert %q: %w", m.Filename, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("batch commit failed: %w", err)
	}
	return nil
}

func (db *DB) GetMovieByFilename(ctx context.Context, filename string) (*Movie, error) {
	query := `SELECT rowid, filename, COALESCE(tmdb_id, 0), title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, COALESCE(media_type, ''), COALESCE(recognition_source, ''), COALESCE(recognition_confidence, 0), COALESCE(verification_score, 0), COALESCE(needs_review, 0), COALESCE(review_reason, ''), COALESCE(vote_average, 0), COALESCE(vote_count, 0)
			  FROM movies WHERE filename = ?`
	row := db.db.QueryRowContext(ctx, query, filename)
	var m Movie
	err := row.Scan(
		&m.ID, &m.Filename, &m.TmdbID, &m.TitleUA, &m.TitleEN, &m.Year,
		&m.Genres, &m.Cast, &m.Plot, &m.PosterURL, &m.LocalPosterPath, &m.MediaType,
		&m.RecognitionSource, &m.RecognitionConfidence, &m.VerificationScore, &m.NeedsReview, &m.ReviewReason,
		&m.VoteAverage, &m.VoteCount,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// CleanMissingMovies та CleanOrphanPosters — нижче.
func (db *DB) CleanMissingMovies(ctx context.Context, actualFiles []string) (int, error) {
	actualMap := make(map[string]bool)
	for _, f := range actualFiles {
		actualMap[filepath.ToSlash(f)] = true
	}
	rows, err := db.db.QueryContext(ctx, "SELECT filename FROM movies")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var toDelete []string
	for rows.Next() {
		var fname string
		if err := rows.Scan(&fname); err == nil {
			if !actualMap[fname] {
				toDelete = append(toDelete, fname)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, fname := range toDelete {
		if _, err := tx.ExecContext(ctx, "DELETE FROM movies WHERE filename = ?", fname); err != nil {
			slog.Warn("clean_missing_delete_failed",
				slog.String("file", fname), slog.Any("error", err))
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM ai_resolutions WHERE original_filename = ?", fname); err != nil {
			slog.Warn("clean_ai_resolution_delete_failed",
				slog.String("file", fname), slog.Any("error", err))
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(toDelete), nil
}

func (db *DB) CleanOrphanPosters(ctx context.Context, postersDir string) (int, int, error) {
	rows, err := db.db.QueryContext(ctx, "SELECT local_poster_path, filename FROM movies")
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	allValid := make(map[string]bool)
	for rows.Next() {
		var dbPath, filename string
		if err := rows.Scan(&dbPath, &filename); err == nil {
			if dbPath != "" {
				// safe to ignore: non-absolute paths still compare consistently as cleaned fallbacks.
				abs, _ := filepath.Abs(dbPath)
				allValid[abs] = true
			}
			ext := filepath.Ext(filename)
			stem := filename[:len(filename)-len(ext)]
			// safe to ignore: non-absolute paths still compare consistently as cleaned fallbacks.
			manualPath, _ := filepath.Abs(filepath.Join(postersDir, stem+".jpg"))
			allValid[manualPath] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	entries, err := os.ReadDir(postersDir)
	if err != nil {
		return 0, 0, nil
	}
	checkedCount := 0
	deletedCount := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return checkedCount, deletedCount, err
		}
		if entry.IsDir() {
			continue
		}
		checkedCount++
		fullPath := filepath.Join(postersDir, entry.Name())
		// safe to ignore: non-absolute paths still compare consistently as cleaned fallbacks.
		absPath, _ := filepath.Abs(fullPath)
		if !allValid[absPath] {
			if err := os.Remove(fullPath); err == nil {
				deletedCount++
			}
		}
	}
	return checkedCount, deletedCount, nil
}

func (db *DB) DeleteMovieByFilename(ctx context.Context, filename string) error {
	query := `DELETE FROM movies WHERE filename = ?`
	_, err := db.db.ExecContext(ctx, query, filename)
	return err
}

func (db *DB) GetAIResolution(ctx context.Context, filename string, pipelineVersion int) (*AIResolution, bool, error) {
	query := `SELECT original_filename, resolved_title, year, media_type, confidence, pipeline_version, provider, model, updated_at FROM ai_resolutions WHERE original_filename = ?`
	row := db.db.QueryRowContext(ctx, query, filename)
	var r AIResolution
	var updated string
	err := row.Scan(&r.OriginalFilename, &r.ResolvedTitle, &r.Year, &r.MediaType, &r.Confidence, &r.PipelineVersion, &r.Provider, &r.Model, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if r.PipelineVersion != pipelineVersion {
		return nil, true, nil
	}
	return &r, false, nil
}

func (db *DB) SaveAIResolution(ctx context.Context, r AIResolution) error {
	query := `INSERT INTO ai_resolutions (original_filename, resolved_title, year, media_type, confidence, pipeline_version, provider, model, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(original_filename) DO UPDATE SET resolved_title=excluded.resolved_title, year=excluded.year, media_type=excluded.media_type, confidence=excluded.confidence, pipeline_version=excluded.pipeline_version, provider=excluded.provider, model=excluded.model, updated_at=excluded.updated_at
	WHERE excluded.pipeline_version > ai_resolutions.pipeline_version
	   OR (excluded.pipeline_version = ai_resolutions.pipeline_version AND excluded.updated_at >= ai_resolutions.updated_at)`
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now().UTC()
	}
	_, err := db.db.ExecContext(ctx, query, r.OriginalFilename, r.ResolvedTitle, r.Year, r.MediaType, r.Confidence, r.PipelineVersion, r.Provider, r.Model, r.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// GetStatsCounts returns the total number of movies and the number of unrecognized movies.
func (db *DB) GetStatsCounts(ctx context.Context) (total, unrec, suspicious int, err error) {
	// Total count
	err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM movies").Scan(&total)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to get total count: %w", err)
	}

	// Unrecognized count (tmdb_id = 0)
	err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM movies WHERE tmdb_id = 0").Scan(&unrec)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to get unrecognized count: %w", err)
	}
	err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM movies WHERE tmdb_id > 0 AND needs_review != 0").Scan(&suspicious)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to get suspicious count: %w", err)
	}
	return total, unrec, suspicious, nil
}

// GetMoviesByFilenames returns a map of movies keyed by filename
func (db *DB) GetMoviesByFilenames(ctx context.Context, filenames []string) (map[string]Movie, error) {
	if len(filenames) == 0 {
		return make(map[string]Movie), nil
	}

	if len(filenames) > filenameChunkSize {
		slog.Warn("large_filenames_batch", slog.Int("count", len(filenames)))
	}

	result := make(map[string]Movie)
	for start := 0; start < len(filenames); start += filenameChunkSize {
		end := start + filenameChunkSize
		if end > len(filenames) {
			end = len(filenames)
		}

		chunk := filenames[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, len(chunk))
		for i, fname := range chunk {
			placeholders[i] = "?"
			args[i] = fname
		}

		query := fmt.Sprintf(
			`SELECT rowid, filename, COALESCE(tmdb_id, 0), title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, COALESCE(media_type, ''), COALESCE(recognition_source, ''), COALESCE(recognition_confidence, 0), COALESCE(verification_score, 0), COALESCE(needs_review, 0), COALESCE(review_reason, ''), COALESCE(vote_average, 0), COALESCE(vote_count, 0)
			 FROM movies WHERE filename IN (%s)`,
			strings.Join(placeholders, ","))

		if err := func() error {
			rows, err := db.db.QueryContext(ctx, query, args...)
			if err != nil {
				return fmt.Errorf("GetMoviesByFilenames query failed: %w", err)
			}
			defer rows.Close()

			for rows.Next() {
				var m Movie
				err := rows.Scan(
					&m.ID, &m.Filename, &m.TmdbID, &m.TitleUA, &m.TitleEN, &m.Year,
					&m.Genres, &m.Cast, &m.Plot, &m.PosterURL, &m.LocalPosterPath, &m.MediaType,
					&m.RecognitionSource, &m.RecognitionConfidence, &m.VerificationScore, &m.NeedsReview, &m.ReviewReason,
					&m.VoteAverage, &m.VoteCount,
				)
				if err != nil {
					slog.Error("storage_scan_error", slog.Any("error", err))
					continue
				}
				result[m.Filename] = m
			}

			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (db *DB) DeleteAIResolution(ctx context.Context, filename string) error {
	query := `DELETE FROM ai_resolutions WHERE original_filename = ?`
	_, err := db.db.ExecContext(ctx, query, filename)
	return err
}

// SetState зберігає довільне значення у app_state за ключем (upsert).
func (db *DB) SetState(ctx context.Context, key, value string) error {
	_, err := db.db.ExecContext(ctx,
		`INSERT INTO app_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// GetState повертає значення з app_state або порожній рядок якщо ключ відсутній.
func (db *DB) GetState(ctx context.Context, key string) string {
	var val string
	_ = db.db.QueryRowContext(ctx,
		`SELECT value FROM app_state WHERE key = ?`, key).Scan(&val)
	return val
}
