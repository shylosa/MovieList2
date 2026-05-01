package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"movielist-app/internal/ai"
	"movielist-app/internal/config"
	"movielist-app/internal/scanner"
	"movielist-app/internal/sheets"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
	"movielist-app/internal/utils"
	"movielist-app/internal/web"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx           context.Context
	cfg           *config.Config
	db            *storage.DB
	tmdbClient    *tmdb.Client
	aiClient      *ai.Client
	aiModelsCache []string
	modelsMutex   sync.RWMutex
	scanCancel    context.CancelFunc
	isScanning    bool
	scanMutex     sync.Mutex
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	utils.InitStructuredLogger()
	a.ctx = ctx
	a.cfg = config.Load()
	a.tmdbClient = tmdb.NewClient(a.cfg)
	a.aiClient = ai.NewClient(a.cfg)

	var err error
	a.db, err = storage.New(a.cfg.DBPath)
	if err != nil {
		slog.Error("db_critical_error", slog.Any("error", err))
		os.Exit(1)
	}
	_ = a.db.InitSchema(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.tmdbClient != nil {
		a.tmdbClient.Close()
	}
	if a.db != nil {
		a.db.Close()
	}
}

func (a *App) logFront(msg string) {
	wailsRuntime.EventsEmit(a.ctx, "log-message", msg)
	log.Output(2, msg)
}

// ── Публічні методи інтерфейсу ──────────────────────────────────────────────

func (a *App) GetMovies() ([]storage.Movie, error) {
	movies, err := a.db.GetAllMovies(a.ctx)
	if err != nil {
		return nil, err
	}
	if movies == nil {
		return []storage.Movie{}, nil
	}
	return movies, nil
}

func (a *App) GetStats() map[string]interface{} {
	movies, err := a.db.GetAllMovies(a.ctx)
	if err != nil {
		return map[string]interface{}{"total": 0, "unrec": 0, "last": "Помилка"}
	}
	if movies == nil {
		movies = []storage.Movie{}
	}

	unrec := 0
	for _, m := range movies {
		if m.TitleUA == "" && m.TitleEN == "" {
			unrec++
		}
	}
	lastScan := "Ніколи"
	if info, err := os.Stat(a.cfg.DBPath); err == nil {
		lastScan = info.ModTime().Format("2006-01-02 15:04")
	}
	return map[string]interface{}{
		"total": len(movies),
		"unrec": unrec,
		"last":  lastScan,
	}
}

func (a *App) GetAppVersion() string {
	return a.cfg.AppVersion
}

func (a *App) DeleteMovie(filename string) error {
	m, err := a.db.GetMovieByFilename(a.ctx, filename)
	if err == nil && m != nil && m.LocalPosterPath != "" {
		_ = os.Remove(m.LocalPosterPath)
	}
	if err = a.db.DeleteMovieByFilename(a.ctx, filename); err != nil {
		a.logFront(fmt.Sprintf("❌ Помилка видалення %s: %v", filename, err))
		return err
	}
	a.logFront(fmt.Sprintf("🗑 Видалено: %s", filename))
	return nil
}

func (a *App) OpenURL(url string) {
	wailsRuntime.BrowserOpenURL(a.ctx, url)
}

func (a *App) OpenLogs() {
	path, _ := filepath.Abs("logs")
	_ = os.MkdirAll(path, 0755)
	a.openInExplorer(path)
}
func (a *App) SelectMediaFolder() (string, error) {
	path, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Виберіть папку з медіафайлами",
	})
	if err != nil {
		return "", err
	}
	return path, nil
}
func (a *App) OpenSheet() {
	if a.cfg.GoogleSheetURL == "" {
		a.logFront("❌ URL таблиці не вказано у конфігурації")
		return
	}
	a.logFront("🌐 Відкриваю Google Таблицю...")
	wailsRuntime.BrowserOpenURL(a.ctx, a.cfg.GoogleSheetURL)
}

func (a *App) OpenShowcase() {
	a.logFront("🎬 Підготовка вітрини...")
	movies, _ := a.db.GetAllMovies(a.ctx)
	if err := web.Generate(a.cfg, movies); err != nil {
		a.logFront(fmt.Sprintf("❌ Помилка генерації вітрини: %v", err))
		return
	}
	path, _ := filepath.Abs(a.cfg.HTMLPath)
	a.openInExplorer(path)
	a.logFront("✅ Вітрину відкрито!")
}

func (a *App) SyncToCloud() {
	a.logFront("🚀 Підготовка до синхронізації...")
	movies, err := a.db.GetAllMovies(a.ctx)
	if err != nil {
		a.logFront("❌ Помилка читання БД: " + err.Error())
		return
	}
	if len(movies) == 0 {
		a.logFront("⚠️ База порожня, нічого відправляти.")
		return
	}
	sheetsClient, err := sheets.NewClient(a.ctx, a.cfg)
	if err != nil {
		a.logFront("❌ Помилка підключення до Google Sheets: " + err.Error())
		return
	}
	a.logFront(fmt.Sprintf("📦 Відправка %d записів...", len(movies)))
	if err = sheetsClient.SyncMovies(a.ctx, movies); err != nil {
		a.logFront("❌ Збій синхронізації: " + err.Error())
	} else {
		a.logFront("✅ Хмарна таблиця оновлена!")
	}
}

func (a *App) GetAIModels() ([]string, error) {
	a.modelsMutex.RLock()
	if len(a.aiModelsCache) > 0 {
		cache := a.aiModelsCache
		a.modelsMutex.RUnlock()
		return cache, nil
	}
	a.modelsMutex.RUnlock()

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", a.cfg.GeminiAPIKey)

	// --- ОПТИМІЗАЦІЯ: Захист від зависання мережі (таймаут 10 секунд) ---
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)

	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	// Додав обробку помилки декодування (про всяк випадок)
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		if strings.Contains(name, "gemini") {
			names = append(names, name)
		}
	}

	a.modelsMutex.Lock()
	a.aiModelsCache = names
	a.modelsMutex.Unlock()

	return names, nil
}

// ── Сканування ───────────────────────────────────────────────────────────────

func (a *App) RunScan() {
	slog.Info("scan_triggered")
	a.scanMutex.Lock()
	if a.isScanning {
		a.scanMutex.Unlock()
		a.logFront("⚠️ Сканування вже йде. Ігнорую повторний запуск.")
		return
	}
	a.isScanning = true
	a.scanMutex.Unlock()

	// 1. Створюємо контекст, який можна скасувати
	ctx, cancel := context.WithCancel(a.ctx)
	a.scanCancel = cancel

	defer func() {
		cancel()
		a.scanMutex.Lock()
		a.isScanning = false
		a.scanMutex.Unlock()

		// Якщо контекст був скасований штучно - пишемо що зупинено, інакше - завершено
		msg := "Сканування завершено"
		if a.ctx.Err() != nil {
			msg = "Сканування перервано користувачем"
		}

		wailsRuntime.EventsEmit(a.ctx, "scan-finished", msg)
		a.logFront("🏁 [ФІНАЛ] " + msg)
	}()

	wailsRuntime.EventsEmit(a.ctx, "scan-started")

	scn := scanner.NewScanner(a.cfg)
	diskPaths, err := scn.GetDiskFiles()
	if err != nil {
		a.finalizeScan(fmt.Sprintf("❌ Помилка сканування диску: %v", err))
		return
	}

	// Очищуємо записи про видалені файли
	a.cleanDeletedFiles(diskPaths)
	_, _ = a.db.CleanMissingMovies(a.ctx, diskPaths)
	a.db.CleanOrphanPosters(a.ctx, a.cfg.PostersDir)

	// Визначаємо що треба обробити (нові + нерозпізнані)
	filesToProcess := a.filterUnprocessed(diskPaths)
	if len(filesToProcess) == 0 {
		a.finalizeScan("Змін не знайдено.")
		return
	}

	a.logFront(fmt.Sprintf("📂 Файлів для обробки: %d", len(filesToProcess)))

	// 🟢 ДОДАНО: Черга для відкладеного перекладу
	var translationQueue []string

	// Спроба 1: конкурентний прямий пошук через TMDB
	var geminiQueue []string

	type scanResult struct {
		fname       string
		info        *tmdb.MovieInfo
		needsGemini bool
	}

	totalFiles := len(filesToProcess)
	resultsChan := make(chan scanResult, totalFiles)
	sem := make(chan struct{}, 10) // Ліміт у 10 одночасних запитів до TMDB (rate-limit safe)
	var wg sync.WaitGroup
	var processedCount int32

	for _, path := range filesToProcess {
		if ctx.Err() != nil {
			a.logFront("🛑 Процес сканування перервано.")
			break
		}

		wg.Add(1)
		sem <- struct{}{} // Захоплюємо слот

		go func(filePath string) {
			defer wg.Done()
			defer func() { <-sem }() // Звільняємо слот
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic_in_goroutine", slog.Any("panic", r))
				}
			}()

			if ctx.Err() != nil {
				return
			}

			fname := filepath.Base(filePath)

			// 🟢 СТВОРЮЄМО УНІКАЛЬНИЙ TRACE_ID ДЛЯ ЦЬОГО ФАЙЛУ
			fileTraceID := uuid.New().String()[:8]
			fileCtx := utils.ContextWithTrace(ctx, fileTraceID)
			logger := utils.LoggerWithTrace(fileCtx)

			// Атомарно збільшуємо лічильник для UI
			current := atomic.AddInt32(&processedCount, 1)
			a.emitProgress(int(current), totalFiles, "🔍 TMDB: "+fname)

			logger.Info("start_processing",
				slog.String("file", fname),
				slog.String("stage", "init"),
			)

			info, err := a.tmdbClient.FetchFromFilename(fileCtx, fname)
			if err != nil {
				logger.Warn("tmdb_search_error",
					slog.String("file", fname),
					slog.Any("error", err),
				)
				a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", fname, err))
			}

			if info != nil && info.TitleEN != "" {
				resultsChan <- scanResult{fname: fname, info: info, needsGemini: false}
			} else {
				resultsChan <- scanResult{fname: fname, info: nil, needsGemini: true}
			}
		}(path)
	}

	// Закриваємо канал у фоні, коли всі воркери відпрацюють
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// 🛡️ Єдиний Writer для SQLite: вигрібає канал і пише послідовно
	for res := range resultsChan {
		if !res.needsGemini {
			movie := movieFromTMDB(res.fname, res.info)
			_ = a.db.SaveMovie(ctx, movie)
			a.logFront(fmt.Sprintf("✅ TMDB: '%s' → '%s'", res.fname, res.info.TitleUA))
			translationQueue = append(translationQueue, res.fname)
		} else {
			geminiQueue = append(geminiQueue, res.fname)
		}
	}

	// Спроба 2: Gemini для тих що TMDB не знайшов
	if len(geminiQueue) > 0 {
		a.logFront(fmt.Sprintf("🤖 Черга Gemini: %d файлів", len(geminiQueue)))

		// 🟢 СТАЛО: processGeminiQueue тепер повертає успішні файли
		recognizedByGemini := a.processGeminiQueue(ctx, geminiQueue, a.aiClient)
		translationQueue = append(translationQueue, recognizedByGemini...)
	}

	// 🟢 НОВЕ: ФАЗА 3 - Асинхронний відкладений переклад
	if len(translationQueue) > 0 {
		a.processTranslationQueue(ctx, translationQueue, a.aiClient)
	}
}

// StopScan зупиняє поточний процес сканування
func (a *App) StopScan() {
	if a.scanCancel != nil {
		a.logFront("🚨 [СТОП] Сигнал скасування отримано бекендом!")
		a.scanCancel()
	} else {
		a.logFront("⚠️ [СТОП] ПОМИЛКА: scanCancel порожній!")
	}
}

// processGeminiQueue — Gemini розпізнає назви → TMDB верифікує → мерж → збереження
// 🟢 СТАЛО: Додаємо ctx context.Context першим параметром і повертаємо []string
func (a *App) processGeminiQueue(ctx context.Context, filenames []string, aiClient *ai.Client) []string {
	var recognizedFiles []string // 👈 Збираємо успішні файли

	const batchSize = 10
	total := len(filenames)
	totalBatches := (total + batchSize - 1) / batchSize
	processed := 0

	for i := 0; i < total; i += batchSize {
		// 🟢 НОВЕ: Перевіряємо, чи не натиснуто СТОП перед кожною новою пачкою
		if ctx.Err() != nil {
			a.logFront("🛑 Зупинено обробку черги Gemini.")
			break
		}

		end := i + batchSize
		if end > total {
			end = total
		}
		batch := filenames[i:end]
		currentBatch := i/batchSize + 1

		a.logFront(fmt.Sprintf("📦 Gemini пачка %d/%d (%d файлів)...", currentBatch, totalBatches, len(batch)))

		// 🟢 СТАЛО: Передаємо локальний ctx замість a.ctx у мережевий запит!
		results, err := aiClient.RecognizeBulk(ctx, batch)
		if err != nil {
			a.logFront(fmt.Sprintf("⚠️ Gemini помилка пачки %d: %v", currentBatch, err))

			for _, fname := range batch {
				processed++
				a.emitProgress(processed, total, "❌ Помилка: "+fname)

				_ = a.db.SaveMovie(ctx, storage.Movie{Filename: fname})
			}
			continue
		}

		recognizedMap := make(map[string]ai.RecognizedTitle, len(results))
		for _, r := range results {
			recognizedMap[r.OriginalFile] = r
		}

		for _, fname := range batch {
			processed++
			rec, ok := recognizedMap[fname]
			if !ok || rec.ENTitle == "" {
				a.logFront(fmt.Sprintf("⚠️ Gemini не розпізнав: '%s'", fname))
				a.emitProgress(processed, total, "❓ Не розпізнано: "+fname)
				// 🟢 СТАЛО: Передаємо локальний ctx
				_ = a.db.SaveMovie(ctx, storage.Movie{Filename: fname})
				continue
			}

			a.emitProgress(processed, total, "🤖 Gemini: "+rec.ENTitle)
			movie := a.mergeGeminiWithTMDB(fname, rec)

			_ = a.db.SaveMovie(ctx, movie)
			recognizedFiles = append(recognizedFiles, fname) // 👈 Додаємо
		}
	}

	// Додаємо перевірку, щоб не писати "Успішно оброблено", якщо ми перервали процес
	if ctx.Err() == nil {
		a.logFront("✅ Gemini черга оброблена!")
	}
	return recognizedFiles // 👈 Повертаємо
}

// mergeGeminiWithTMDB — шукає фільм в TMDB за EN назвою від Gemini,
// мержить результати: TMDB має пріоритет, Gemini заповнює прогалини
func (a *App) mergeGeminiWithTMDB(fname string, rec ai.RecognizedTitle) storage.Movie {
	// 🛡️ КРОК 1: ПЕРЕВІРКА ВАЛІДНОСТІ ВІДПОВІДІ ШІ
	if rec.ENTitle == "" {
		a.logFront(fmt.Sprintf("⚠️ [GEMINI] Відсутня EN назва для '%s'. Пропускаємо пошук.", fname))
		return storage.Movie{Filename: fname}
	}

	// 🛡️ КРОК 4: ЗАЛІЗНИЙ КОНТРОЛЬ РОКУ (Захист від галюцинацій)
	// Парсимо ім'я файлу, щоб отримати еталонний рік
	parsed := tmdb.ParseFilename(fname)
	if parsed.Year > 0 {
		if rec.Year != nil {
			diff := *rec.Year - parsed.Year
			// Допускаємо похибку ±1 рік. Якщо більше — це галюцинація!
			if diff < -1 || diff > 1 {
				a.logFront(fmt.Sprintf("🛡️ [ЗАХИСТ] ШІ галюцинує рік %d для '%s'. Примусово беремо з файлу: %d", *rec.Year, fname, parsed.Year))
				*rec.Year = parsed.Year // Жорстко перезаписуємо брехню ШІ
			}
		} else {
			// Якщо ШІ взагалі не дав року, страхуємо його
			rec.Year = &parsed.Year
		}
	}

	// Базова заготовка з даними Gemini: тільки ідентифікатори для пошуку.
	movie := storage.Movie{
		Filename: fname,
		TitleEN:  rec.ENTitle,
	}
	if rec.Year != nil {
		movie.Year = strconv.Itoa(*rec.Year)
	}

	// Визначаємо MediaType для TMDB пошуку
	mt := tmdb.MediaTypeMovie
	if rec.MediaType == "tv" {
		mt = tmdb.MediaTypeTV
	}

	// Пошук в TMDB за EN назвою від Gemini + ПЕРЕВІРЕНИМ РОКОМ
	yearStr := ""
	if rec.Year != nil {
		yearStr = strconv.Itoa(*rec.Year)
	}

	tmdbInfo, err := a.tmdbClient.FetchByCleanTitle(a.ctx, rec.ENTitle, yearStr, mt)
	if err != nil {
		a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", rec.ENTitle, err))
	}

	if tmdbInfo == nil {
		a.logFront(fmt.Sprintf("❌ TMDB не знайшов '%s' — запис залишається нерозпізнаним", rec.ENTitle))
		// Повертаємо виключно Filename. Відсутність TitleEN та TitleUA гарантує,
		// що файл потрапить у статистику "Нерозпізнані" і UI покаже його для ручного фіксу.
		return storage.Movie{Filename: fname}
	}

	a.logFront(fmt.Sprintf("✅ TMDB знайшов: '%s' (%s)", tmdbInfo.TitleEN, tmdbInfo.Year))

	// TMDB є джерелом повних даних після Gemini Resolve.
	movie.TmdbID = tmdbInfo.TMDBID
	movie.TitleEN = tmdbInfo.TitleEN
	movie.Year = tmdbInfo.Year
	movie.PosterURL = tmdbInfo.PosterURL
	movie.LocalPosterPath = tmdbInfo.LocalPosterPath
	movie.TitleUA = tmdbInfo.TitleUA
	movie.Plot = tmdbInfo.Plot
	movie.Genres = tmdbInfo.Genres
	movie.Cast = tmdbInfo.Cast

	return movie
}

// ── Ручне виправлення ─────────────────────────────────────────────────────────

// FixSelected — виправлення вибраних записів.
// hint може бути: TMDB URL/ID, назва фільму, рік, або порожнє (→ Gemini)
func (a *App) FixSelected(selected []map[string]interface{}) {
	// 1. Створюємо керований контекст 🟢
	ctx, cancel := context.WithCancel(a.ctx)
	a.scanCancel = cancel
	defer cancel()

	wailsRuntime.EventsEmit(a.ctx, "scan-started")

	var withHint []map[string]interface{}
	var geminiQueue []string

	for _, s := range selected {
		filename, _ := s["filename"].(string)
		hint, _ := s["hint"].(string)

		if hint != "" && hint != "skip" {
			withHint = append(withHint, s)
		} else {
			geminiQueue = append(geminiQueue, filename)
		}
	}

	total := len(withHint) + len(geminiQueue)
	current := 0
	a.logFront(fmt.Sprintf("🛠 Виправлення %d файлів...", total))

	var translationQueue []string // 👈 НОВЕ

	// 2. Перший цикл (ручне виправлення) 🟢
	for _, fix := range withHint {
		// Перевірка, чи не натиснули СТОП
		if ctx.Err() != nil {
			a.logFront("🛑 Виправлення перервано.")
			return
		}

		current++
		filename := fix["filename"].(string)
		hint := fix["hint"].(string)
		a.emitProgress(current, total, "🔄 "+filename)

		// Передаємо локальний ctx
		_ = a.UpdateMovie(ctx, filename, hint)
		translationQueue = append(translationQueue, filename) // 👈 Додаємо
	}

	// 3. Другий етап (черга Gemini) 🟢
	if len(geminiQueue) > 0 {
		// Перевіряємо стоп перед початком черги
		if ctx.Err() == nil {
			// ПЕРЕДАЄМО ctx у функцію (як ми домовились раніше)
			recognized := a.processGeminiQueue(ctx, geminiQueue, a.aiClient)
			translationQueue = append(translationQueue, recognized...) // 👈 Додаємо
		}
	}

	// 🟢 НОВЕ: Запускаємо переклад для виправлених
	if len(translationQueue) > 0 {
		a.processTranslationQueue(ctx, translationQueue, a.aiClient)
	}

	a.finalizeScan(fmt.Sprintf("Виправлено %d файлів", total))
}

// UpdateMovie — оновлення одного запису за hint від користувача.
// hint може бути: TMDB URL (themoviedb.org/movie/123), числовий ID, або текстова назва/рік.
func (a *App) UpdateMovie(ctx context.Context, filename, hint string) error {
	hint = strings.TrimSpace(hint)

	existing, _ := a.db.GetMovieByFilename(ctx, filename)
	if existing == nil {
		existing = &storage.Movie{Filename: filename}
	}

	// Варіант 1: TMDB URL або числовий ID
	if tmdbID, mediaType := extractTMDBID(hint); tmdbID > 0 {
		a.logFront(fmt.Sprintf("🎯 [%s] TMDB ID: %d", filename, tmdbID))
		info, err := a.tmdbClient.GetDetails(ctx, mediaType, tmdbID, filename)
		if err != nil {
			a.logFront(fmt.Sprintf("❌ Помилка TMDB ID %d: %v", tmdbID, err))
			return err
		}
		if info != nil {
			applyTMDBToMovie(existing, info)

			return a.db.SaveMovie(ctx, *existing)
		}
	}

	// Варіант 2: текстова підказка (назва або рік) → Gemini → TMDB
	query := filename
	if hint != "" {
		query = fmt.Sprintf("%s (підказка: %s)", filename, hint)
	}

	a.logFront(fmt.Sprintf("🧠 [%s] Аналіз через Gemini...", filename))
	results, err := a.aiClient.RecognizeBulk(ctx, []string{query})
	if err != nil || len(results) == 0 {
		a.logFront(fmt.Sprintf("❌ Gemini не відповів для '%s'", filename))
		return err
	}

	rec := results[0]
	if rec.ENTitle == "" {
		a.logFront(fmt.Sprintf("❌ Gemini не розпізнав '%s'", filename))
		return nil
	}

	a.logFront(fmt.Sprintf("🤖 Gemini: '%s' → '%s'", filename, rec.ENTitle))
	movie := a.mergeGeminiWithTMDB(filename, rec)

	// Зберігаємо існуючі поля якщо нові порожні
	if movie.TitleUA == "" {
		movie.TitleUA = existing.TitleUA
	}
	if movie.Plot == "" {
		movie.Plot = existing.Plot
	}

	return a.db.SaveMovie(ctx, movie)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// filterUnprocessed повертає файли яких немає в БД або які нерозпізнані
func (a *App) filterUnprocessed(diskPaths []string) []string {
	movies, _ := a.db.GetAllMovies(a.ctx)
	recognized := make(map[string]bool, len(movies))
	for _, m := range movies {
		// Файл вважається розпізнаним, ТІЛЬКИ якщо ми знайшли його в TMDB (є ID)
		if m.TmdbID > 0 {
			recognized[m.Filename] = true
		}
	}

	var result []string
	for _, p := range diskPaths {
		fname := filepath.Base(p)
		if !recognized[fname] {
			result = append(result, p)
		}
	}
	return result
}

// cleanDeletedFiles видаляє з БД записи файлів яких більше немає на диску
func (a *App) cleanDeletedFiles(diskPaths []string) {
	diskMap := make(map[string]bool, len(diskPaths))
	for _, p := range diskPaths {
		diskMap[filepath.Base(p)] = true
	}

	dbFilenames, err := a.db.GetAllFilenames(a.ctx)
	if err != nil || dbFilenames == nil {
		return
	}

	removed := 0
	for dbFile := range dbFilenames {
		if !diskMap[dbFile] {
			a.logFront(fmt.Sprintf("🗑 Видалено з диску: %s", dbFile))
			removed++
		}
	}
	if removed > 0 {
		a.logFront(fmt.Sprintf("🧹 Очищено %d застарілих записів", removed))
	}
}

// movieFromTMDB — створює storage.Movie з результату TMDB (без Gemini)
func movieFromTMDB(fname string, info *tmdb.MovieInfo) storage.Movie {
	return storage.Movie{
		Filename:        fname,
		TmdbID:          info.TMDBID,
		TitleUA:         info.TitleUA,
		TitleEN:         info.TitleEN,
		Year:            info.Year,
		Plot:            info.Plot,
		Genres:          info.Genres,
		Cast:            info.Cast,
		PosterURL:       info.PosterURL,
		LocalPosterPath: info.LocalPosterPath,
	}
}

// applyTMDBToMovie — перезаписує поля movie з tmdbInfo (для ручного виправлення)
func applyTMDBToMovie(movie *storage.Movie, info *tmdb.MovieInfo) {
	movie.TmdbID = info.TMDBID
	movie.TitleEN = info.TitleEN
	movie.Year = info.Year
	movie.PosterURL = info.PosterURL
	movie.LocalPosterPath = info.LocalPosterPath
	if info.TitleUA != "" {
		movie.TitleUA = info.TitleUA
	}
	if info.Plot != "" {
		movie.Plot = info.Plot
	}
	if info.Genres != "" {
		movie.Genres = info.Genres
	}
	if info.Cast != "" {
		movie.Cast = info.Cast
	}
}

// extractTMDBID витягує TMDB ID та тип медіа з підказки користувача.
// Підтримує: https://themoviedb.org/movie/123, /tv/456, або просто "123456"
func extractTMDBID(hint string) (int, tmdb.MediaType) {
	// TMDB URL з типом
	reURL := regexp.MustCompile(`themoviedb\.org/(movie|tv)/(\d+)`)
	if m := reURL.FindStringSubmatch(hint); len(m) > 2 {
		id, _ := strconv.Atoi(m[2])
		mt := tmdb.MediaTypeMovie
		if m[1] == "tv" {
			mt = tmdb.MediaTypeTV
		}
		return id, mt
	}

	// Чистий числовий ID (більше 4 цифр щоб не сплутати з роком)
	if matched, _ := regexp.MatchString(`^\d{5,}$`, hint); matched {
		id, _ := strconv.Atoi(hint)
		return id, tmdb.MediaTypeMovie
	}

	return 0, tmdb.MediaTypeMovie
}

func (a *App) emitProgress(current, total int, filename string) {
	wailsRuntime.EventsEmit(a.ctx, "scan-progress", map[string]interface{}{
		"current": current, "total": total, "filename": filename,
	})
}

func (a *App) finalizeScan(msg string) {
	movies, _ := a.db.GetAllMovies(a.ctx)
	_ = web.Generate(a.cfg, movies)
	wailsRuntime.EventsEmit(a.ctx, "scan-finished", msg)
}

func (a *App) openInExplorer(path string) {
	var cmd *exec.Cmd
	switch goRuntime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		a.logFront(fmt.Sprintf("❌ Не вдалося відкрити: %v", err))
	}
}

// needsTranslation повертає true, якщо текст треба перекласти (англійська або підозріла кирилиця)
func needsTranslation(s string) bool {
	if s == "" {
		return true // Порожнечу завжди наповнюємо
	}

	sLower := strings.ToLower(s)

	// 1. ПЕРЕВІРКА НА СЛОВА-МАРКЕРИ (Російські слова, що пишуться спільними літерами)
	// Це значно дешевше за бібліотеки і відловить твій "Враг у ворот" (слово "у")
	russianMarkers := []string{"из", "как", "что", "он", "это", "бы", "вот", "для"}
	words := strings.Fields(sLower)
	for _, w := range words {
		for _, marker := range russianMarkers {
			if w == marker {
				return true // 100% російський маркер
			}
		}
	}

	hasCyrillic := false
	hasRussianLetter := false
	hasUkrainianLetter := false

	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			hasCyrillic = true
		}
		// Яскраві маркери російської (ы, э, ъ, ё)
		if r == 'ы' || r == 'э' || r == 'ъ' || r == 'ё' || r == 'Ы' || r == 'Э' || r == 'Ъ' || r == 'Ё' {
			hasRussianLetter = true
		}
		// Яскраві маркери української (і, ї, є, ґ)
		if r == 'і' || r == 'ї' || r == 'є' || r == 'ґ' || r == 'І' || r == 'Ї' || r == 'Є' || r == 'Ґ' {
			hasUkrainianLetter = true
		}
	}

	// 2. Немає кирилиці (англійська) -> перекладаємо
	if !hasCyrillic {
		return true
	}
	// 3. Є специфічні російські літери -> перекладаємо[cite: 12]
	if hasRussianLetter {
		return true
	}
	// 4. СІРА ЗОНА: якщо немає специфічних українських літер (і, ї, є, ґ),
	// але є кирилиця — краще відправити в пачку на переклад.
	// Gemini розбереться, а ти не отримаєш "Враг у ворот" у списку.
	if !hasUkrainianLetter {
		return true
	}

	return false
}

func (a *App) processTranslationQueue(ctx context.Context, filenames []string, aiClient *ai.Client) {
	a.logFront(fmt.Sprintf("🌍 Аналіз локалізації для %d файлів...", len(filenames)))

	var itemsToTranslate []ai.BulkTranslateItem
	movieMap := make(map[string]storage.Movie)

	// 1. Фільтруємо чергу: збираємо ТІЛЬКИ те, що дійсно треба перекладати
	for i, fname := range filenames {
		if ctx.Err() != nil {
			a.logFront("🛑 Підготовку перервано.")
			return
		}

		a.emitProgress(i+1, len(filenames), "🔄 Перевірка тексту: "+fname)

		movie, err := a.db.GetMovieByFilename(ctx, fname)
		if err != nil || movie == nil {
			continue
		}

		needTitle := movie.TitleUA == "" || needsTranslation(movie.TitleUA)
		needPlot := movie.Plot == "" || needsTranslation(movie.Plot)

		if needTitle || needPlot {
			fallbackTitle := movie.TitleUA
			if fallbackTitle == "" {
				fallbackTitle = movie.TitleEN
				if fallbackTitle == "" {
					fallbackTitle = filepath.Base(fname)
				}
			}

			itemsToTranslate = append(itemsToTranslate, ai.BulkTranslateItem{
				Filename: fname,
				Title:    fallbackTitle,
				Plot:     movie.Plot,
			})
			movieMap[fname] = *movie
		}
	}

	if len(itemsToTranslate) == 0 {
		a.logFront("✅ Усі файли вже мають коректну локалізацію.")
		return
	}

	a.logFront(fmt.Sprintf("🚀 Відправка в Gemini: %d файлів...", len(itemsToTranslate)))

	// 2. Батчинг та відправка до AI
	const batchSize = 10
	totalBatches := (len(itemsToTranslate) + batchSize - 1) / batchSize
	updatedCount := 0

	for i := 0; i < len(itemsToTranslate); i += batchSize {
		if ctx.Err() != nil {
			a.logFront("🛑 Переклад перервано.")
			break
		}

		end := i + batchSize
		if end > len(itemsToTranslate) {
			end = len(itemsToTranslate)
		}
		batch := itemsToTranslate[i:end]
		currentBatch := i/batchSize + 1

		a.logFront(fmt.Sprintf("📦 Переклад: пачка %d/%d (%d файлів)...", currentBatch, totalBatches, len(batch)))

		results, err := aiClient.TranslateBulk(ctx, batch)
		if err != nil {
			a.logFront(fmt.Sprintf("⚠️ Помилка перекладу пачки %d: %v", currentBatch, err))
			continue
		}

		// 3. Зберігаємо результати у БД
		for _, res := range results {
			movie, ok := movieMap[res.Filename]
			if !ok {
				continue
			}

			changed := false
			if res.Title != "" && res.Title != movie.TitleUA && !strings.Contains(strings.ToLower(res.Title), "thought") {
				movie.TitleUA = res.Title
				changed = true
			}
			if res.Plot != "" && res.Plot != movie.Plot {
				movie.Plot = res.Plot
				changed = true
			}

			if changed {
				_ = a.db.SaveMovie(ctx, movie)
				updatedCount++
				a.logFront(fmt.Sprintf("✅ Адаптовано: '%s'", movie.TitleUA))
			}
		}
	}

	if ctx.Err() == nil {
		a.logFront(fmt.Sprintf("✅ Фаза перекладу завершена! Оновлено записів: %d", updatedCount))
	}
}
