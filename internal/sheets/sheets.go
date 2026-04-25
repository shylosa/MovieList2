package sheets

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"

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
		log.Println("[WARNING] [SHEETS] GOOGLE_SHEET_URL не знайдено у конфігурації")
		return nil, fmt.Errorf("GOOGLE_SHEET_URL не знайдено")
	}

	// Переконайся, що credentials.json лежить у корені проєкту
	srv, err := sheets.NewService(ctx, option.WithCredentialsFile("credentials.json"))
	if err != nil {
		log.Printf("[CRITICAL] [SHEETS] Помилка авторизації Google: %v", err)
		return nil, fmt.Errorf("помилка авторизації Google: %v", err)
	}

	log.Println("[INFO] [SHEETS] ✅ Клієнт Google Sheets успішно ініціалізований")
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

	log.Printf("[INFO] [SHEETS] 🔄 Початок синхронізації з таблицею (ID: %s, Лист: %s)", spreadsheetID, sheetName)

	// 1. Готуємо дані (Headers + Body)
	headers := []interface{}{"File Path", "Title (UA)", "Title (EN)", "Year", "Genre", "Cast", "Plot", "Poster URL"}

	var values [][]interface{}
	values = append(values, headers)

	for _, m := range movies {
		row := []interface{}{
			m.Filename,
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
	log.Printf("[INFO] [SHEETS] 🧹 Очищення вкладки '%s'...", sheetName)
	err := c.retry(func() error {
		_, err := c.service.Spreadsheets.Values.Clear(spreadsheetID, sheetName, &sheets.ClearValuesRequest{}).Context(ctx).Do()
		return err
	})
	if err != nil {
		log.Printf("[ERROR] [SHEETS] Не вдалося очистити таблицю: %v", err)
		return fmt.Errorf("не вдалося очистити таблицю: %v", err)
	}

	if len(movies) == 0 {
		log.Println("[WARNING] [SHEETS] Немає даних для запису (база порожня)")
		return nil
	}

	// 3. Запис даних (аналог sheet.update)
	endCol := c.colLetter(len(headers))
	cellRange := fmt.Sprintf("%s!A1:%s%d", sheetName, endCol, len(values))

	valueRange := &sheets.ValueRange{
		Values: values,
	}

	log.Printf("[INFO] [SHEETS] 📦 Відправка %d записів у хмару...", len(movies))
	err = c.retry(func() error {
		_, err := c.service.Spreadsheets.Values.Update(spreadsheetID, cellRange, valueRange).
			ValueInputOption("RAW").
			Context(ctx).
			Do()
		return err
	})

	if err != nil {
		log.Printf("[ERROR] [SHEETS] Помилка запису в Google Sheets: %v", err)
		return err
	}

	log.Printf("[INFO] [SHEETS] ✅ Хмарна таблиця успішно оновлена! (Записано %d рядків)", len(values))
	return nil
}

// retry реалізує логіку повторних спроб (аналог _retry у Python)
func (c *Client) retry(fn func() error) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			log.Printf("[WARNING] [SHEETS] API Гугла перевантажено, спроба %d/3 через 5 сек...", i+1)
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
