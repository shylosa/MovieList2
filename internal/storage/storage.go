package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"movielist-app/internal/utils"

	_ "modernc.org/sqlite"
)

// Movie describes the movie record structure in the database (unified)
type Movie struct {
	ID              int    `json:"id"`
	Filename        string `json:"filename"`
	TmdbID          int    `json:"tmdb_id"`
	TitleUA         string `json:"title_ua"`
	TitleEN         string `json:"title_en"`
	Year            string `json:"year"`
	Plot            string `json:"plot"`
	Genres          string `json:"genres"`
	Cast            string `json:"cast"`
	PosterURL       string `json:"poster_url"`
	LocalPosterPath string `json:"local_poster_path"`
	MediaType       string `json:"media_type"`
}

// AIResolution is the Gemini recognition cache entry (L2 Cache)
type AIResolution struct {
	OriginalFilename string
	ResolvedTitle    string
	Year             int
	MediaType        string
	Confidence       float64
}

type DB struct {
	db *sql.DB
}

const filenameChunkSize = 500

func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
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
	);
	CREATE INDEX IF NOT EXISTS idx_tmdb_id ON movies(tmdb_id);
	CREATE INDEX IF NOT EXISTS idx_title_en ON movies(title_en);
	CREATE TABLE IF NOT EXISTS ai_resolutions (
		original_filename TEXT PRIMARY KEY,
		resolved_title TEXT,
		year INTEGER,
		media_type TEXT,
		confidence REAL
	);
	`
	_, err := db.db.ExecContext(ctx, query)
	if err != nil {
		return err
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

	return nil
}

func (db *DB) GetAllMovies(ctx context.Context) ([]Movie, error) {
	query := `SELECT filename, COALESCE(tmdb_id, 0), title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, COALESCE(media_type, '') FROM movies ORDER BY rowid ASC`
	rows, err := db.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("GetAllMovies query failed: %w", err)
	}
	defer rows.Close()

	var movies []Movie
	for rows.Next() {
		var m Movie
		err := rows.Scan(
			&m.Filename, &m.TmdbID, &m.TitleUA, &m.TitleEN, &m.Year,
			&m.Genres, &m.Cast, &m.Plot, &m.PosterURL, &m.LocalPosterPath, &m.MediaType,
		)
		if err != nil {
			slog.Error("storage_scan_error", slog.Any("error", err))
			continue
		}
		movies = append(movies, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if movies == nil {
		movies = []Movie{}
	}
	return movies, nil
}

func (db *DB) SaveMovie(ctx context.Context, m Movie) error {
	query := `
		INSERT OR REPLACE INTO movies
		(filename, tmdb_id, title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, media_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.db.ExecContext(ctx, query,
		m.Filename, m.TmdbID, m.TitleUA, m.TitleEN, m.Year,
		m.Genres, m.Cast, m.Plot, m.PosterURL, m.LocalPosterPath, m.MediaType,
	)
	return err
}

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

	query := `
		INSERT OR REPLACE INTO movies
		(filename, tmdb_id, title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, media_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// Prepare statement for performance
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare stmt failed: %w", err)
	}
	defer stmt.Close()

	// Partial save: errors per-row are logged but batch commit succeeds
	// Execute inserts in memory
	for _, m := range movies {
		// Abort safely if context is cancelled
		if err := ctx.Err(); err != nil {
			utils.LoggerWithTrace(ctx).Warn("batch_insert_cancelled", slog.Any("error", err))
			return err
		}

		_, err := stmt.ExecContext(ctx,
			m.Filename, m.TmdbID, m.TitleUA, m.TitleEN, m.Year,
			m.Genres, m.Cast, m.Plot, m.PosterURL, m.LocalPosterPath, m.MediaType,
		)
		if err != nil {
			// Log per-row error and continue
			slog.Warn("batch_insert_skip", slog.String("file", m.Filename), slog.Any("err", err))
			continue
		}
	}

	// Commit all changes at once
	return tx.Commit()
}

func (db *DB) GetMovieByFilename(ctx context.Context, filename string) (*Movie, error) {
	query := `SELECT filename, COALESCE(tmdb_id, 0), title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, COALESCE(media_type, '')
			  FROM movies WHERE filename = ?`
	row := db.db.QueryRowContext(ctx, query, filename)
	var m Movie
	err := row.Scan(
		&m.Filename, &m.TmdbID, &m.TitleUA, &m.TitleEN, &m.Year,
		&m.Genres, &m.Cast, &m.Plot, &m.PosterURL, &m.LocalPosterPath, &m.MediaType,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// Remaining methods (CleanMissingMovies, CleanOrphanPosters, GetAllFilenames) are unchanged
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
	for _, fname := range toDelete {
		_, _ = db.db.ExecContext(ctx, "DELETE FROM movies WHERE filename = ?", fname)
		_, _ = db.db.ExecContext(ctx, "DELETE FROM ai_resolutions WHERE original_filename = ?", fname)
	}
	return len(toDelete), nil
}

func (db *DB) CleanOrphanPosters(ctx context.Context, postersDir string) (int, error) {
	rows, err := db.db.QueryContext(ctx, "SELECT local_poster_path, filename FROM movies")
	if err != nil {
		return 0, err
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
		return 0, err
	}
	entries, err := os.ReadDir(postersDir)
	if err != nil {
		return 0, nil
	}
	deletedCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(postersDir, entry.Name())
		// safe to ignore: non-absolute paths still compare consistently as cleaned fallbacks.
		absPath, _ := filepath.Abs(fullPath)
		if !allValid[absPath] {
			if err := os.Remove(fullPath); err == nil {
				deletedCount++
			}
		}
	}
	return deletedCount, nil
}

func (db *DB) GetAllFilenames(ctx context.Context) (map[string]bool, error) {
	rows, err := db.db.QueryContext(ctx, "SELECT filename FROM movies")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := make(map[string]bool)
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err == nil {
			res[f] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (db *DB) DeleteMovieByFilename(ctx context.Context, filename string) error {
	query := `DELETE FROM movies WHERE filename = ?`
	_, err := db.db.ExecContext(ctx, query, filename)
	return err
}

func (db *DB) GetAIResolution(ctx context.Context, filename string) (*AIResolution, error) {
	query := `SELECT original_filename, resolved_title, year, media_type, confidence FROM ai_resolutions WHERE original_filename = ?`
	row := db.db.QueryRowContext(ctx, query, filename)
	var r AIResolution
	err := row.Scan(&r.OriginalFilename, &r.ResolvedTitle, &r.Year, &r.MediaType, &r.Confidence)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (db *DB) SaveAIResolution(ctx context.Context, r AIResolution) error {
	query := `INSERT OR REPLACE INTO ai_resolutions (original_filename, resolved_title, year, media_type, confidence) VALUES (?, ?, ?, ?, ?)`
	_, err := db.db.ExecContext(ctx, query, r.OriginalFilename, r.ResolvedTitle, r.Year, r.MediaType, r.Confidence)
	return err
}

// GetStatsCounts returns the total number of movies and the number of unrecognized movies.
func (db *DB) GetStatsCounts(ctx context.Context) (total, unrec int, err error) {
	// Total count
	err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM movies").Scan(&total)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get total count: %w", err)
	}

	// Unrecognized count (tmdb_id = 0)
	err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM movies WHERE tmdb_id = 0").Scan(&unrec)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get unrecognized count: %w", err)
	}

	return total, unrec, nil
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
			`SELECT filename, COALESCE(tmdb_id, 0), title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, COALESCE(media_type, '')
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
					&m.Filename, &m.TmdbID, &m.TitleUA, &m.TitleEN, &m.Year,
					&m.Genres, &m.Cast, &m.Plot, &m.PosterURL, &m.LocalPosterPath, &m.MediaType,
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
