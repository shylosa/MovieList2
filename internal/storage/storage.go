package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"movielist-app/internal/utils"

	_ "modernc.org/sqlite"
)

// Movie описує структуру фільму в базі даних (уніфіковано)
// Movie описує структуру фільму в базі даних (уніфіковано)
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

// AIResolution — кеш розпізнавання Gemini (L2 Cache)
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

func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	return &DB{db: conn}, nil
}

func (db *DB) Close() error {
	return db.db.Close()
}

func (db *DB) InitSchema(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS movies (
		filename TEXT PRIMARY KEY,
		tmdb_id INTEGER, -- 👈 Додано колонку
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
	PRAGMA journal_mode = WAL;
	PRAGMA synchronous = NORMAL;
	PRAGMA busy_timeout = 5000;

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

	// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Лінива міграція для старих баз.
	// Якщо таблиця вже існувала, ми маємо явно додати нові колонки.
	// Ми свідомо ігноруємо помилку тут, бо якщо колонка вже є, SQLite поверне помилку "duplicate column name", що для нас ОК.
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

	// Відкриваємо транзакцію
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx failed: %w", err)
	}
	defer tx.Rollback() // Безпечний відкат, якщо Commit не спрацює

	query := `
		INSERT OR REPLACE INTO movies
		(filename, tmdb_id, title_ua, title_en, year, genres, "cast", plot, poster_url, local_poster_path, media_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// Підготовлений вираз для оптимізації
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare stmt failed: %w", err)
	}
	defer stmt.Close()

	// Виконуємо вставку в пам'яті
	for _, m := range movies {
		// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Перериваємо цикл безпечно, якщо контекст скасовано.
		if err := ctx.Err(); err != nil {
			utils.LoggerWithTrace(ctx).Warn("batch_insert_cancelled", slog.Any("error", err))
			return err
		}

		_, err := stmt.ExecContext(ctx,
			m.Filename, m.TmdbID, m.TitleUA, m.TitleEN, m.Year,
			m.Genres, m.Cast, m.Plot, m.PosterURL, m.LocalPosterPath, m.MediaType,
		)
		if err != nil {
			// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Логуємо помилку для конкретного файлу і йдемо далі
			slog.Warn("batch_insert_skip", slog.String("file", m.Filename), slog.Any("err", err))
			continue
		}
	}

	// Фіксуємо зміни на диск за один раз
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

// Решта методів (CleanMissingMovies, CleanOrphanPosters, GetAllFilenames) залишаються без змін
func (db *DB) CleanMissingMovies(ctx context.Context, actualFiles []string) (int, error) {
	actualMap := make(map[string]bool)
	for _, f := range actualFiles {
		actualMap[filepath.Base(f)] = true
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
	for _, fname := range toDelete {
		_, _ = db.db.ExecContext(ctx, "DELETE FROM movies WHERE filename = ?", fname)
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
				abs, _ := filepath.Abs(dbPath)
				allValid[abs] = true
			}
			ext := filepath.Ext(filename)
			stem := filename[:len(filename)-len(ext)]
			manualPath, _ := filepath.Abs(filepath.Join(postersDir, stem+".jpg"))
			allValid[manualPath] = true
		}
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

func (db *DB) DeleteAIResolution(ctx context.Context, filename string) error {
	query := `DELETE FROM ai_resolutions WHERE original_filename = ?`
	_, err := db.db.ExecContext(ctx, query, filename)
	return err
}
