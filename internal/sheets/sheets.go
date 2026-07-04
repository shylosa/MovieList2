package sheets

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/utils"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type Client struct {
	cfg     *config.Config
	service *sheets.Service
}

// NewClient створює підключення до Google Sheets (аналог __init__ в Python)
func NewClient(ctx context.Context, cfg *config.Config) (*Client, error) {
	if cfg.GoogleSheetURL == "" {
		slog.Warn("google_sheet_url_missing")
		return nil, fmt.Errorf("GOOGLE_SHEET_URL не знайдено")
	}

	// Переконайся, що credentials.json лежить у корені проєкту
	srv, err := sheets.NewService(ctx, option.WithCredentialsFile("credentials.json"))
	if err != nil {
		slog.Error("google_sheets_auth_failed", slog.Any("error", err))
		return nil, fmt.Errorf("помилка авторизації Google: %v", err)
	}

	slog.Info("google_sheets_client_initialized")
	return &Client{
		cfg:     cfg,
		service: srv,
	}, nil
}

// SyncMovies реалізує повну синхронізацію (очищення + запис)
func (c *Client) SyncMovies(ctx context.Context, movies []storage.Movie) error {
	spreadsheetID := c.extractID(c.cfg.GoogleSheetURL)
	sheetName := c.cfg.SheetWorksheetName
	if sheetName == "" {
		sheetName = "base"
	}

	slog.Info("google_sheets_sync_started",
		slog.String("spreadsheet_id", spreadsheetID),
		slog.String("sheet_name", sheetName))

	// 1. Готуємо дані (Headers + Body)
	headers := []interface{}{"File", "Title (UA)", "Title (EN)", "Year", "Genre", "Cast", "Plot", "Poster URL"}

	var values [][]interface{}
	values = append(values, headers)

	for _, m := range movies {
		row := []interface{}{
			utils.DisplayFileLabel(m.Filename),
			m.TitleUA,
			m.TitleEN,
			m.Year,
			m.Genres,
			m.Cast,
			m.Plot,
			m.PosterURL,
		}
		values = append(values, row)
	}

	// 2. Очищення аркуша (аналог sheet.clear) з Retry-логікою
	slog.Info("google_sheets_clear_started", slog.String("sheet_name", sheetName))
	err := c.retry(func() error {
		_, err := c.service.Spreadsheets.Values.Clear(spreadsheetID, sheetName, &sheets.ClearValuesRequest{}).Context(ctx).Do()
		return err
	})
	if err != nil {
		slog.Error("google_sheets_clear_failed",
			slog.String("sheet_name", sheetName),
			slog.Any("error", err))
		return fmt.Errorf("не вдалося очистити таблицю: %v", err)
	}

	if len(movies) == 0 {
		slog.Warn("google_sheets_no_movies_to_sync")
		return nil
	}

	// 3. Запис даних (аналог sheet.update)
	endCol := c.colLetter(len(headers))
	cellRange := fmt.Sprintf("%s!A1:%s%d", sheetName, endCol, len(values))

	valueRange := &sheets.ValueRange{
		Values: values,
	}

	slog.Info("google_sheets_upload_started", slog.Int("movies_count", len(movies)))
	err = c.retry(func() error {
		_, err := c.service.Spreadsheets.Values.Update(spreadsheetID, cellRange, valueRange).
			ValueInputOption("RAW").
			Context(ctx).
			Do()
		return err
	})

	if err != nil {
		slog.Error("google_sheets_update_failed",
			slog.String("range", cellRange),
			slog.Any("error", err))
		return err
	}

	slog.Info("google_sheets_sync_completed", slog.Int("rows_written", len(values)))
	return nil
}

// retry реалізує логіку повторних спроб (аналог _retry у Python)
func (c *Client) retry(fn func() error) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			slog.Warn("google_sheets_retry_scheduled", slog.Int("attempt", i+1))
			time.Sleep(5 * time.Second)
		}
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

// colLetter конвертує номер у літеру (аналог _col_letter: 1->A, 26->Z, 27->AA)
func (c *Client) colLetter(n int) string {
	result := ""
	for n > 0 {
		n--
		result = string(rune('A'+(n%26))) + result
		n /= 26
	}
	return result
}

// extractID витягує ID таблиці з повного URL
func (c *Client) extractID(sheetURL string) string {
	prefix := "https://docs.google.com/spreadsheets/d/"

	if !strings.HasPrefix(sheetURL, prefix) {
		return sheetURL
	}

	idPart := strings.TrimPrefix(sheetURL, prefix)
	parts := strings.Split(idPart, "/")
	if len(parts) > 0 {
		return parts[0]
	}

	return idPart
}
