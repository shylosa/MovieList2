package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"strconv"
	"strings"
	"sync"
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

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx           context.Context
	cfg           *config.Config
	db            *storage.DB
	tmdbClient    *tmdb.Client
	aiModelsCache []string
	scanCancel    context.CancelFunc
	isScanning    bool
	scanMutex     sync.Mutex
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	utils.InitLogger()
	a.ctx = ctx
	a.cfg = config.Load()
	a.tmdbClient = tmdb.NewClient(a.cfg)

	var err error
	a.db, err = storage.New(a.cfg.DBPath)
	if err != nil {
		log.Fatalf("[CRITICAL] Помилка БД: %v", err)
	}
	_ = a.db.InitSchema(ctx)
}

func (a *App) shutdown(ctx context.Context) {
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
	if len(a.aiModelsCache) > 0 {
		return a.aiModelsCache, nil
	}
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

	var names []string
	for _, m := range result.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		if strings.Contains(name, "gemini") {
			names = append(names, name)
		}
	}
	a.aiModelsCache = names
	return names, nil
}

// ── Сканування ───────────────────────────────────────────────────────────────

func (a *App) RunScan() {
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

	// Спроба 1: прямий пошук через TMDB
	var geminiQueue []string
	for i, path := range filesToProcess {
		if ctx.Err() != nil {
			a.logFront("🛑 Процес сканування перервано.")
			break
		}

		fname := filepath.Base(path)
		a.emitProgress(i+1, len(filesToProcess), "🔍 TMDB: "+fname)

		// 🟢 БУЛО: a.tmdbClient.FetchFromFilename(a.ctx, fname)
		// 🟢 СТАЛО: Передаємо наш локальний ctx
		info, err := a.tmdbClient.FetchFromFilename(ctx, fname)
		if err != nil {
			a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", fname, err))
		}

		if info != nil && info.TitleEN != "" {
			movie := movieFromTMDB(fname, info)
			a.translateDataIfNeeded(ctx, &movie)

			_ = a.db.SaveMovie(ctx, movie)
			a.logFront(fmt.Sprintf("✅ TMDB: '%s' → '%s'", fname, info.TitleUA))
		} else {
			geminiQueue = append(geminiQueue, fname)
		}
	}

	// Спроба 2: Gemini для тих що TMDB не знайшов
	if len(geminiQueue) > 0 {
		a.logFront(fmt.Sprintf("🤖 Черга Gemini: %d файлів", len(geminiQueue)))
		a.processGeminiQueue(ctx, geminiQueue)
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
// 🟢 СТАЛО: Додаємо ctx context.Context першим параметром
func (a *App) processGeminiQueue(ctx context.Context, filenames []string) {
	const batchSize = 10
	total := len(filenames)
	totalBatches := (total + batchSize - 1) / batchSize
	aiClient := ai.NewClient(a.cfg)
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
			a.translateDataIfNeeded(ctx, &movie)

			_ = a.db.SaveMovie(ctx, movie)
		}
	}

	// Додаємо перевірку, щоб не писати "Успішно оброблено", якщо ми перервали процес
	if ctx.Err() == nil {
		a.logFront("✅ Gemini черга оброблена!")
	}
}

// mergeGeminiWithTMDB — шукає фільм в TMDB за EN назвою від Gemini,
// мержить результати: TMDB має пріоритет, Gemini заповнює прогалини
func (a *App) mergeGeminiWithTMDB(fname string, rec ai.RecognizedTitle) storage.Movie {
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

	// Базова заготовка з даними Gemini (будуть перезаписані TMDB де можливо)
	movie := storage.Movie{
		Filename: fname,
		TitleUA:  rec.TitleUA,
		TitleEN:  rec.ENTitle,
		Plot:     rec.Plot,
		Genres:   rec.Genres,
		Cast:     rec.Cast,
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
		a.logFront(fmt.Sprintf("❌ TMDB не знайшов '%s' — збережено з даними Gemini", rec.ENTitle))
		return movie
	}

	a.logFront(fmt.Sprintf("✅ TMDB знайшов: '%s' (%s)", tmdbInfo.TitleEN, tmdbInfo.Year))

	// Мерж: TMDB перезаписує непорожні поля, Gemini — fallback для порожніх
	movie.TmdbID = tmdbInfo.TMDBID
	movie.TitleEN = tmdbInfo.TitleEN
	movie.Year = tmdbInfo.Year
	movie.PosterURL = tmdbInfo.PosterURL
	movie.LocalPosterPath = tmdbInfo.LocalPosterPath

	// TitleUA: TMDB має пріоритет (офіційна локалізація), Gemini — fallback
	if tmdbInfo.TitleUA != "" {
		movie.TitleUA = tmdbInfo.TitleUA
	}
	// Plot: TMDB UA має пріоритет
	if tmdbInfo.Plot != "" {
		movie.Plot = tmdbInfo.Plot
	}
	// Genres: TMDB має пріоритет
	if tmdbInfo.Genres != "" {
		movie.Genres = tmdbInfo.Genres
	}
	// Cast: TMDB має пріоритет
	if tmdbInfo.Cast != "" {
		movie.Cast = tmdbInfo.Cast
	}

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

		// Тут бажано теж передати ctx, якщо UpdateMovie це підтримує
		_ = a.UpdateMovie(filename, hint)
	}

	// 3. Другий етап (черга Gemini) 🟢
	if len(geminiQueue) > 0 {
		// Перевіряємо стоп перед початком черги
		if ctx.Err() == nil {
			// ПЕРЕДАЄМО ctx у функцію (як ми домовились раніше)
			a.processGeminiQueue(ctx, geminiQueue)
		}
	}

	a.finalizeScan(fmt.Sprintf("Виправлено %d файлів", total))
}

// UpdateMovie — оновлення одного запису за hint від користувача.
// hint може бути: TMDB URL (themoviedb.org/movie/123), числовий ID, або текстова назва/рік.
func (a *App) UpdateMovie(filename, hint string) error {
	hint = strings.TrimSpace(hint)

	existing, _ := a.db.GetMovieByFilename(a.ctx, filename)
	if existing == nil {
		existing = &storage.Movie{Filename: filename}
	}

	// Варіант 1: TMDB URL або числовий ID
	if tmdbID, mediaType := extractTMDBID(hint); tmdbID > 0 {
		a.logFront(fmt.Sprintf("🎯 [%s] TMDB ID: %d", filename, tmdbID))
		info, err := a.tmdbClient.GetDetails(a.ctx, mediaType, tmdbID, filename)
		if err != nil {
			a.logFront(fmt.Sprintf("❌ Помилка TMDB ID %d: %v", tmdbID, err))
			return err
		}
		if info != nil {
			applyTMDBToMovie(existing, info)
			a.translateDataIfNeeded(a.ctx, existing)

			return a.db.SaveMovie(a.ctx, *existing)
		}
	}

	// Варіант 2: текстова підказка (назва або рік) → Gemini → TMDB
	query := filename
	if hint != "" {
		query = fmt.Sprintf("%s (підказка: %s)", filename, hint)
	}

	a.logFront(fmt.Sprintf("🧠 [%s] Аналіз через Gemini...", filename))
	aiClient := ai.NewClient(a.cfg)
	results, err := aiClient.RecognizeBulk(a.ctx, []string{query})
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

	return a.db.SaveMovie(a.ctx, movie)
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

func (a *App) translateDataIfNeeded(ctx context.Context, movie *storage.Movie) {
	aiClient := ai.NewClient(a.cfg)
	madeChanges := false

	// 1. Перевіряємо НАЗВУ
	if needsTranslation(movie.TitleUA) {
		a.logFront(fmt.Sprintf("🔄 Перекладаю назву '%s'...", movie.TitleUA))
		translatedTitle := aiClient.TranslateTitle(ctx, movie.TitleUA)
		if translatedTitle != "" {
			movie.TitleUA = translatedTitle
			madeChanges = true
		}
	}

	// 2. Перевіряємо ОПИС
	if needsTranslation(movie.Plot) {
		a.logFront(fmt.Sprintf("🔄 Перекладаю опис для '%s'...", movie.TitleUA))
		translatedPlot := aiClient.TranslatePlot(ctx, movie.Plot)
		if translatedPlot != "" {
			movie.Plot = translatedPlot
			madeChanges = true
		}
	}

	if madeChanges {
		a.logFront(fmt.Sprintf("✅ Переклад завершено: '%s'", movie.TitleUA))
	}
}

// needsTranslation повертає true, якщо текст треба перекласти (немає кирилиці АБО є специфічні російські літери)
func needsTranslation(s string) bool {
	if s == "" {
		return false
	}

	hasCyrillic := false
	hasRussian := false

	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			hasCyrillic = true
		}
		// Перевірка на унікальні літери російської абетки
		if r == 'ы' || r == 'э' || r == 'ъ' || r == 'ё' ||
		   r == 'Ы' || r == 'Э' || r == 'Ъ' || r == 'Ё' {
			hasRussian = true
			break // Якщо знайшли російську літеру, далі можна не шукати
		}
	}

	// Перекладаємо, якщо немає кирилиці (англійська) АБО є російські літери
	return !hasCyrillic || hasRussian
}
