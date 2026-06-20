package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/sync/singleflight"

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
	ctx                context.Context
	cfg                *config.Config
	db                 *storage.DB
	tmdbClient         *tmdb.Client
	aiClient           *ai.Client
	aiModelsCache      []string
	aiModelsHTTPClient *http.Client
	modelsMutex        sync.RWMutex
	modelsGroup        singleflight.Group
	scanCancel         context.CancelFunc
	isScanning         bool
	scanMutex          sync.Mutex
	isGitHubSyncing    bool
	githubSyncMutex    sync.Mutex
	wg                 sync.WaitGroup
}

type scanResult struct {
	path        string
	fname       string
	info        *tmdb.MovieInfo
	needsGemini bool
}

// aiConfidenceThreshold — єдиний поріг для Gemini, L2-кешу та merge.
const aiConfidenceThreshold = 0.55

// geminiTMDBVerifyMinJW — мінімальна схожість EN-назви Gemini і TMDB після верифікації.
const geminiTMDBVerifyMinJW = 0.85

var russianMarkers = []string{"из", "как", "что", "это", "бы", "вот"}
var russianMarkersRE = regexp.MustCompile(`(?i)\b(?:из|как|что|это|бы|вот)\b`)

var (
	reTMDBURL = regexp.MustCompile(`themoviedb\.org/(movie|tv)/(\d+)`)
	reTMDBID  = regexp.MustCompile(`^\d+$`)
)

func NewApp() *App {
	return &App{
		aiModelsHTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (a *App) startup(ctx context.Context) {
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
	// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Перевіряємо та логуємо помилку ініціалізації
	if err := a.db.InitSchema(ctx); err != nil {
		slog.Error("db_schema_init_error", slog.Any("error", err))
		os.Exit(1)
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.wg.Wait()
	a.cancelScan()
	if a.tmdbClient != nil {
		a.tmdbClient.Close()
	}
	if a.db != nil {
		a.db.Close()
	}
}

func (a *App) logFront(msg string) {
	if a.ctx == nil {
		slog.Info("log_front_fallback", slog.String("msg", msg))
		return
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("log_front_emit_panic", slog.Any("panic", r))
				slog.Info("log_front_fallback", slog.String("msg", msg))
			}
		}()
		wailsRuntime.EventsEmit(a.ctx, "log-message", msg)
	}()
}

func (a *App) setScanCancel(cancel context.CancelFunc) {
	a.scanMutex.Lock()
	defer a.scanMutex.Unlock()
	if a.scanCancel != nil {
		a.scanCancel()
	}
	a.scanCancel = cancel
}

func (a *App) clearScanCancel() {
	a.scanMutex.Lock()
	defer a.scanMutex.Unlock()
	a.scanCancel = nil
}

func (a *App) cancelScan() {
	a.scanMutex.Lock()
	cancel := a.scanCancel
	a.scanCancel = nil
	a.scanMutex.Unlock()
	if cancel != nil {
		cancel()
	}
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
	for i := range movies {
		movies[i].FileLabel = utils.DisplayFileLabel(movies[i].Filename, movies[i].MediaType)
	}
	return movies, nil
}

func (a *App) GetStats() map[string]interface{} {
	total, unrec, err := a.db.GetStatsCounts(a.ctx)
	if err != nil {
		return map[string]interface{}{"total": 0, "unrec": 0, "last": "Помилка"}
	}

	lastScan := "Ніколи"
	if info, err := os.Stat(a.cfg.DBPath); err == nil {
		lastScan = info.ModTime().Format("2006-01-02 15:04")
	}
	return map[string]interface{}{
		"total": total,
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

	// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Очищаємо сліди з L2 кешу ШІ
	if err := a.db.DeleteAIResolution(a.ctx, filename); err != nil {
		slog.Warn("delete_ai_resolution_failed", slog.String("file", filename), slog.Any("error", err))
	}

	a.logFront(fmt.Sprintf("🗑 Видалено: %s", filename))
	return nil
}

func (a *App) OpenURL(url string) {
	wailsRuntime.BrowserOpenURL(a.ctx, url)
}

func (a *App) OpenLogs() {
	path, err := filepath.Abs("logs")
	if err != nil {
		slog.Warn("logs_abs_path_failed", slog.Any("error", err))
		path = "logs"
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		slog.Warn("logs_dir_create_failed", slog.Any("error", err))
	}
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
	a.OpenGoogleSheet()
}

func (a *App) OpenGoogleSheet() {
	if a.cfg.GoogleSheetURL == "" {
		a.logFront("❌ URL таблиці не вказано у конфігурації")
		return
	}
	a.logFront("🌐 Відкриваю Google Таблицю...")
	wailsRuntime.BrowserOpenURL(a.ctx, a.cfg.GoogleSheetURL)
}

func (a *App) OpenGitHubRepo() {
	if a.cfg.GithubURL == "" {
		a.logFront("❌ URL репозиторію не вказано у конфігурації")
		return
	}
	a.logFront("🌐 Відкриваю GitHub Repository...")
	wailsRuntime.BrowserOpenURL(a.ctx, a.cfg.GithubURL)
}

func (a *App) OpenGitHubPage() {
	if a.cfg.GithubPageURL == "" {
		a.logFront("❌ URL сторінки проєкту не вказано у конфігурації")
		return
	}
	a.logFront("🌐 Відкриваю GitHub Pages...")
	wailsRuntime.BrowserOpenURL(a.ctx, a.cfg.GithubPageURL)
}

func (a *App) OpenShowcase() {
	a.logFront("🎬 Підготовка вітрини...")
	movies, err := a.db.GetAllMovies(a.ctx)
	if err != nil {
		slog.Warn("get_movies_for_showcase_failed", slog.Any("error", err))
		a.logFront("❌ Помилка читання БД: " + err.Error())
		return
	}
	if err := web.Generate(a.cfg, movies, false); err != nil {
		a.logFront(fmt.Sprintf("❌ Помилка генерації вітрини: %v", err))
		return
	}
	path, err := filepath.Abs(a.cfg.HTMLPath)
	if err != nil {
		slog.Warn("showcase_abs_path_failed", slog.Any("error", err))
		path = a.cfg.HTMLPath
	}
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

func (a *App) SyncToGitHub() {
	a.githubSyncMutex.Lock()
	if a.isGitHubSyncing {
		a.githubSyncMutex.Unlock()
		a.logFront("⚠️ Синхронізація GitHub вже йде. Ігнорую повторний виклик.")
		return
	}
	a.isGitHubSyncing = true
	a.githubSyncMutex.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			a.githubSyncMutex.Lock()
			a.isGitHubSyncing = false
			a.githubSyncMutex.Unlock()
		}()

		emitFinished := func(success bool, msg string) {
			if a.ctx != nil {
				wailsRuntime.EventsEmit(a.ctx, "github-sync-finished", map[string]interface{}{
					"success": success,
					"message": msg,
				})
			}
		}

		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "github-sync-started")
		}
		a.logFront("📱 Підготовка мобільної вітрини для GitHub Pages...")

		movies, err := a.db.GetAllMovies(a.ctx)
		if err != nil {
			emitFinished(false, "❌ Помилка читання БД: "+err.Error())
			return
		}
		if len(movies) == 0 {
			emitFinished(false, "⚠️ База порожня, нічого публікувати.")
			return
		}

		repoDir, err := a.gitRepoRoot()
		if err != nil {
			emitFinished(false, "❌ Git репозиторій не знайдено: "+err.Error())
			return
		}

		mobileCfg := *a.cfg
		mobileCfg.HTMLPath = filepath.Join(repoDir, "index.html")
		if err := web.Generate(&mobileCfg, movies, true); err != nil {
			emitFinished(false, fmt.Sprintf("❌ Помилка генерації index.html: %v", err))
			return
		}

		if err := a.deployToGitHubPages(); err != nil {
			emitFinished(false, "❌ GitHub Pages: "+err.Error())
			return
		}
		emitFinished(true, "✅ GitHub Pages оновлено!")
	}()
}

func (a *App) gitRepoRoot() (string, error) {
	cmd := exec.CommandContext(a.ctx, "git", "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("empty git root")
	}
	return filepath.FromSlash(root), nil
}

func (a *App) deployToGitHubPages() error {
	workDir, err := a.gitRepoRoot()
	if err != nil {
		return err
	}

	run := func(args ...string) error {
		cmd := exec.CommandContext(a.ctx, args[0], args[1:]...)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			a.logFront(fmt.Sprintf("❌ git %s: %s", args[1], strings.TrimSpace(string(out))))
		}
		return err
	}

	if err := run("git", "add", "-f", "index.html"); err != nil {
		return err
	}

	// commit може повернути ненульовий код якщо "nothing to commit" — це не помилка
	_ = run("git", "commit", "-m", fmt.Sprintf("Update mobile showcase %s",
		time.Now().Format("2006-01-02 15:04")))

	if err := run("git", "push", "origin", a.cfg.GitHubPagesBranch); err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	return nil
}

// GetAIModels is the exported Wails method (no context param).
// It delegates to fetchAIModels using the app context and ensures
// any error is recorded in the logs for observability.
func (a *App) GetAIModels() ([]string, error) {
	names, err := a.fetchAIModels(a.ctx)
	if err != nil {
		slog.Error("get_ai_models_error", slog.Any("error", err))
	}
	return names, err
}

// fetchAIModels is the internal implementation that accepts a context.
// This allows internal callers (like RunScan warmup) to pass their
// scan-specific context while keeping the exported signature RPC-friendly.
func (a *App) fetchAIModels(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Grok-only mode: if Gemini key is absent but Grok is configured — report Grok as available.
	if a.cfg.GeminiAPIKey == "" {
		if a.cfg.GrokAPIKey != "" {
			return []string{"grok-3-mini"}, nil
		}
		return nil, fmt.Errorf("no AI API key configured (set GEMINI_API_KEY or GROK_API_KEY in .env)")
	}
	a.modelsMutex.RLock()
	if len(a.aiModelsCache) > 0 {
		cache := append([]string(nil), a.aiModelsCache...)
		a.modelsMutex.RUnlock()
		return cache, nil
	}
	a.modelsMutex.RUnlock()

	// safe to ignore: singleflight shared flag is not needed by callers.
	value, err, _ := a.modelsGroup.Do("ai_models", func() (interface{}, error) {
		url := "https://generativelanguage.googleapis.com/v1beta/models"
		client := a.aiModelsHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Second}
		}
		// Use original context so that scan cancellation immediately aborts the HTTP request.
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", a.cfg.GeminiAPIKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				slog.Warn("ai_models_error_body_read_failed", slog.Any("error", readErr))
			}
			return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
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

		if a.aiClient != nil {
			a.aiClient.SetModels(names) // SetModels receives only Gemini models — correct.
		}

		// Append Grok as a known fallback model if configured.
		if a.cfg.GrokAPIKey != "" {
			names = append(names, "grok-3-mini (fallback)")
		}

		// Cache includes Grok suffix so all callers see a consistent list.
		a.modelsMutex.Lock()
		a.aiModelsCache = append([]string(nil), names...)
		a.modelsMutex.Unlock()

		return append([]string(nil), names...), nil
	})
	if err != nil {
		return nil, err
	}

	names, ok := value.([]string)
	if !ok {
		slog.Error("fetch_ai_models_type_assertion_failed", slog.String("type", fmt.Sprintf("%T", value)))
		return nil, fmt.Errorf("fetchAIModels: unexpected type %T", value)
	}
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

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		// 1. Створюємо контекст, який можна скасувати
		ctx, cancel := context.WithCancel(a.ctx)
		a.setScanCancel(cancel)

		// 2. Trace ID для всієї сесії сканування
		scanTraceID := uuid.New().String()[:8]
		scanCtx := utils.ContextWithTrace(ctx, scanTraceID)
		utils.LoggerWithTrace(scanCtx).Info("scan_session_start")

		scanFinished := false

		defer func() {
			stoppedByUser := ctx.Err() != nil
			cancel()
			a.clearScanCancel()
			a.scanMutex.Lock()
			a.isScanning = false
			a.scanMutex.Unlock()

			if !scanFinished {
				msg := "Сканування завершено"
				if stoppedByUser {
					msg = "Сканування перервано користувачем"
				}
				a.finalizeScan(a.ctx, msg)
			}
		}()

		wailsRuntime.EventsEmit(a.ctx, "scan-started")

		// 🟢 ДОДАНО: Асинхронно прогріваємо кеш моделей, щоб aiClient отримав актуальний список
		go func() {
			if _, err := a.fetchAIModels(scanCtx); err != nil {
				utils.LoggerWithTrace(scanCtx).Debug("ai_models_warmup_failed", slog.Any("error", err))
			}
		}()

		scn := scanner.NewScanner(a.cfg)

		// 🟢 Очищуємо кеш від попереднього сканування
		if a.tmdbClient != nil {
			a.tmdbClient.ClearCaches()
		}

		// Reset Gemini quota lock so recovered quotas are retried in this session.
		if a.aiClient != nil {
			a.aiClient.ResetQuotaLock()
		}

		diskPaths, err := scn.GetDiskFiles()
		if err != nil {
			a.finalizeScan(scanCtx, fmt.Sprintf("❌ Помилка сканування диску: %v", err))
			scanFinished = true
			return
		}

		// Clean up database records for missing disk files
		diskIDs := make([]string, 0, len(diskPaths))
		for _, p := range diskPaths {
			diskIDs = append(diskIDs, a.getFileIdentifier(p))
		}
		deletedCount, err := a.db.CleanMissingMovies(scanCtx, diskIDs)
		if err != nil {
			utils.LoggerWithTrace(scanCtx).Warn("clean_missing_movies_failed", slog.Any("error", err))
		} else if deletedCount > 0 {
			utils.LoggerWithTrace(scanCtx).Info("missing_movies_cleaned", slog.Int("count", deletedCount))
			a.logFront(fmt.Sprintf("🗑 Вичищено з бази відсутніх файлів: %d", deletedCount))
		}

		orphanCount, err := a.db.CleanOrphanPosters(scanCtx, a.cfg.PostersDir)
		if err != nil {
			utils.LoggerWithTrace(scanCtx).Warn("clean_orphan_posters_failed", slog.Any("error", err))
		} else if orphanCount > 0 {
			utils.LoggerWithTrace(scanCtx).Info("orphan_posters_cleaned", slog.Int("count", orphanCount))
			a.logFront(fmt.Sprintf("🖼 Видалено застарілих файлів постерів: %d", orphanCount))
		}

		// Визначаємо що треба обробити (нові + нерозпізнані)
		filesToProcess := a.filterUnprocessed(scanCtx, diskPaths)
		if len(filesToProcess) == 0 {
			a.finalizeScan(scanCtx, "Змін не знайдено.")
			scanFinished = true
			return
		}

		a.logFront(fmt.Sprintf("📂 Файлів для обробки: %d", len(filesToProcess)))

		resultsChan := a.runTMDBScan(scanCtx, filesToProcess)

		// 🛡️ Єдиний Writer для SQLite: збирає батч і пише транзакцією
		moviesToSave, geminiQueue, translationQueue := a.processScanResults(scanCtx, resultsChan)

		// Зберігаємо всіх знайдених одним запитом
		if len(moviesToSave) > 0 {
			if err := a.db.SaveMoviesBatch(scanCtx, moviesToSave); err != nil {
				utils.LoggerWithTrace(scanCtx).Error("batch_save_failed", slog.Any("error", err))
			} else {
				utils.LoggerWithTrace(scanCtx).Info("batch_save_success", slog.Int("count", len(moviesToSave)), slog.String("stage", "tmdb_scan"))
			}
		}

		// Спроба 2: Gemini для тих що TMDB не знайшов
		if len(geminiQueue) > 0 {
			a.logFront(fmt.Sprintf("🤖 Черга Gemini: %d файлів", len(geminiQueue)))

			recognizedByGemini := a.processGeminiQueue(scanCtx, geminiQueue, a.aiClient)
			translationQueue = append(translationQueue, recognizedByGemini...)
		}

		if len(translationQueue) > 0 {
			a.processTranslationQueue(scanCtx, translationQueue, a.aiClient)
		}
	}()
}

// StopScan зупиняє поточний процес сканування
func (a *App) StopScan() {
	a.logFront("🚨 [СТОП] Сигнал скасування отримано бекендом!")
	a.cancelScan()
}

func (a *App) runTMDBScan(ctx context.Context, paths []string) <-chan scanResult {
	totalFiles := len(paths)
	resultsChan := make(chan scanResult, len(paths))
	sem := make(chan struct{}, 10) // Ліміт у 10 одночасних запитів до TMDB (rate-limit safe)
	var wg sync.WaitGroup
	var processedCount int32

	for _, path := range paths {
		if ctx.Err() != nil {
			a.logFront("🛑 Процес сканування перервано.")
			break
		}

		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			continue
		}

		go func() {
			defer wg.Done()
			defer func() { <-sem }() // Звільняємо слот
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic_in_goroutine",
						slog.Any("panic", r),
						slog.String("stack", string(debug.Stack())),
					)
				}
			}()

			if ctx.Err() != nil {
				return
			}

			fname := a.getFileIdentifier(path)

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

			info, err := a.tmdbClient.FetchFromFilename(fileCtx, path)
			if err != nil {
				logger.Warn("tmdb_search_error",
					slog.String("file", fname),
					slog.Any("error", err),
				)
				a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", fname, err))
			}

			result := scanResult{path: path, fname: fname, info: info, needsGemini: true}
			if info != nil && info.TMDBID > 0 {
				result.needsGemini = false
			}

			select {
			case resultsChan <- result:
			case <-ctx.Done():
			}
		}()
	}

	// Закриваємо канал у фоні, коли всі воркери відпрацюють
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	return resultsChan
}

func (a *App) processScanResults(ctx context.Context, results <-chan scanResult) (toSave []storage.Movie, geminiQueue []string, translationQueue []string) {
	for res := range results {
		if ctx.Err() != nil {
			go func() {
				for range results {
				}
			}()
			break
		}
		if !res.needsGemini {
			movie := movieFromTMDB(res.fname, res.info)
			toSave = append(toSave, movie)
			a.logFront(fmt.Sprintf("✅ TMDB: '%s' → '%s'", res.fname, res.info.TitleUA))
			if a.movieInfoNeedsTranslation(res.info) {
				translationQueue = append(translationQueue, res.fname)
			}
		} else {
			// 🟢 ПЕРЕВІРКА L2 КЕШУ: Чи не розпізнавали ми цей файл раніше через ШІ?
			cached, err := a.db.GetAIResolution(ctx, res.fname)
			if err != nil {
				utils.LoggerWithTrace(ctx).Warn("get_ai_resolution_failed", slog.String("file", res.fname), slog.Any("error", err))
			}
			if cached != nil && cached.Confidence >= aiConfidenceThreshold {
				utils.LoggerWithTrace(ctx).Info("gemini_l2_cache_hit", slog.String("file", res.fname), slog.String("resolved", cached.ResolvedTitle))

				// Використовуємо кешовану назву для пошуку в TMDB
				info, err := a.tmdbClient.FetchByCleanTitle(ctx, cached.ResolvedTitle, strconv.Itoa(cached.Year), tmdb.MediaType(cached.MediaType))
				if err == nil && info != nil && info.TMDBID > 0 {
					jw := tmdb.TitleSimilarity(cached.ResolvedTitle, info.TitleEN)
					if info.SearchTitle != "" {
						if jwSearch := tmdb.TitleSimilarity(cached.ResolvedTitle, info.SearchTitle); jwSearch > jw {
							jw = jwSearch
						}
					}
					if jw >= geminiTMDBVerifyMinJW {
						movie := movieFromTMDB(res.fname, info)
						toSave = append(toSave, movie)
						a.logFront(fmt.Sprintf("⚡ L2-Кеш: '%s' → '%s'", res.fname, info.TitleUA))
						if a.movieInfoNeedsTranslation(info) {
							translationQueue = append(translationQueue, res.fname)
						}
						continue
					}
				}
			}
			geminiQueue = append(geminiQueue, res.path)
		}
	}

	return toSave, geminiQueue, translationQueue
}

// buildUnresolvedMovie creates a placeholder record for files Gemini/TMDB could not verify.
func buildUnresolvedMovie(fname, path string) storage.Movie {
	parsed := tmdb.ParseFilename(path)
	yearStr := ""
	if parsed.Year > 0 {
		yearStr = strconv.Itoa(parsed.Year)
	}
	return storage.Movie{
		Filename: fname,
		TitleEN:  "Unresolved: " + fname,
		TitleUA:  fname,
		Year:     yearStr,
		TmdbID:   0,
	}
}

// appendUnresolvedFromMap adds a placeholder unless the file is already recognized (TmdbID > 0) using pre-loaded map.
func (a *App) appendUnresolvedFromMap(ctx context.Context, movies *[]storage.Movie, path string, existing map[string]storage.Movie) {
	fname := a.getFileIdentifier(path)
	movie, exists := existing[fname]
	if exists && movie.TmdbID > 0 {
		utils.LoggerWithTrace(ctx).Warn("skip_unresolved_downgrade",
			slog.String("file", fname), slog.Int("existing_tmdb_id", movie.TmdbID))
		return
	}
	*movies = append(*movies, buildUnresolvedMovie(fname, path))
}

// processGeminiQueue — Gemini розпізнає назви → TMDB верифікує → мерж → збереження.
// paths — повні шляхи до файлів (для парсера та збагаченого промпту).
func (a *App) processGeminiQueue(ctx context.Context, paths []string, aiClient *ai.Client) []string {
	var recognizedFiles []string

	const batchSize = 10
	total := len(paths)
	totalBatches := (total + batchSize - 1) / batchSize
	processed := 0

	for i := 0; i < total; i += batchSize {
		if ctx.Err() != nil {
			a.logFront("🛑 Gemini черга перервана користувачем.")
			a.emitProgress(total, total, "🛑 Зупинено")
			break
		}

		end := i + batchSize
		if end > total {
			end = total
		}
		batch := paths[i:end]
		currentBatchIdx := i/batchSize + 1

		a.logFront(fmt.Sprintf("📦 Gemini пачка %d/%d (%d файлів) відправлена...", currentBatchIdx, totalBatches, len(batch)))

		// Pre-fetch all existing movies in this batch from DB to eliminate O(N) queries
		batchFnames := make([]string, len(batch))
		for j, p := range batch {
			batchFnames[j] = a.getFileIdentifier(p)
		}
		existingMovies, err := a.db.GetMoviesByFilenames(ctx, batchFnames)
		if err != nil {
			utils.LoggerWithTrace(ctx).Warn("batch_lookup_failed", slog.Any("error", err))
			existingMovies = make(map[string]storage.Movie)
		}

		contexts := make([]ai.FileRecognitionContext, len(batch))
		for j, path := range batch {
			ctxObj := ai.FileRecognitionContextFromPath(path)
			ctxObj.ID = j
			contexts[j] = ctxObj
		}

		results, err := aiClient.RecognizeBulk(ctx, contexts)
		if err != nil {
			a.logFront(fmt.Sprintf("⚠️ Gemini помилка пачки %d: %v", currentBatchIdx, err))
			utils.LoggerWithTrace(ctx).Warn("gemini_batch_failed", slog.Int("batch", currentBatchIdx), slog.Any("error", err))

			// Create placeholders for all files in this failed batch so they don't disappear.
			var failedBatchMovies []storage.Movie
			for _, path := range batch {
				a.appendUnresolvedFromMap(ctx, &failedBatchMovies, path, existingMovies)
			}
			if len(failedBatchMovies) > 0 {
				if errSave := a.db.SaveMoviesBatch(ctx, failedBatchMovies); errSave != nil {
					utils.LoggerWithTrace(ctx).Error("batch_save_failed", slog.Any("error", errSave))
				}
			}
			continue
		}

		recognizedMap := make(map[int]ai.RecognizedTitle, len(results))
		for _, r := range results {
			recognizedMap[r.ID] = r
		}

		var moviesToSave []storage.Movie
		for j, path := range batch {
			processed++
			fname := a.getFileIdentifier(path)
			rec, ok := recognizedMap[j]

			if !ok || rec.ENTitle == "" {
				a.logFront(fmt.Sprintf("⚠️ Gemini не розпізнав: '%s'", fname))
				a.emitProgress(processed, total, "❓ Не розпізнано: "+fname)

				// Create a placeholder movie record instead of silently skipping.
				a.appendUnresolvedFromMap(ctx, &moviesToSave, path, existingMovies)
				continue
			}

			a.emitProgress(processed, total, "🤖 Gemini: "+rec.ENTitle)

			movie := a.mergeGeminiWithTMDB(ctx, path, rec)
			if movie.TmdbID == 0 {
				a.appendUnresolvedFromMap(ctx, &moviesToSave, path, existingMovies)
				continue
			}
			moviesToSave = append(moviesToSave, movie)
			if movie.TmdbID > 0 {
				yearVal := 0
				if rec.Year != nil {
					yearVal = *rec.Year
				}
				if err := a.db.SaveAIResolution(ctx, storage.AIResolution{
					OriginalFilename: fname,
					ResolvedTitle:    rec.ENTitle,
					Year:             yearVal,
					MediaType:        rec.MediaType,
					Confidence:       rec.Confidence,
				}); err != nil {
					utils.LoggerWithTrace(ctx).Warn("save_ai_resolution_failed",
						slog.String("file", fname), slog.Any("error", err))
				}
				recognizedFiles = append(recognizedFiles, fname)
			}
		}

		if len(moviesToSave) > 0 {
			if err := a.db.SaveMoviesBatch(ctx, moviesToSave); err != nil {
				utils.LoggerWithTrace(ctx).Error("batch_save_failed", slog.Any("error", err))
			} else {
				utils.LoggerWithTrace(ctx).Info("batch_save_success", slog.Int("count", len(moviesToSave)), slog.String("stage", "gemini_queue"))
			}
		}
	}

	if ctx.Err() == nil {
		a.logFront("✅ Gemini черга оброблена!")
	}
	return recognizedFiles
}

// mergeGeminiWithTMDB — шукає фільм в TMDB за EN назвою від Gemini,
// мержить результати: TMDB має пріоритет, Gemini заповнює прогалини.
// filePath — повний шлях або basename (для ParseFilename).
func (a *App) mergeGeminiWithTMDB(ctx context.Context, filePath string, rec ai.RecognizedTitle) storage.Movie {
	fname := a.getFileIdentifier(filePath)

	// 🛡️ КРОК 1: ПЕРЕВІРКА ВАЛІДНОСТІ ВІДПОВІДІ ШІ
	if rec.ENTitle == "" {
		a.logFront(fmt.Sprintf("⚠️ [GEMINI] Відсутня EN назва для '%s'. Пропускаємо пошук.", fname))
		return storage.Movie{Filename: fname}
	}

	if rec.Confidence < aiConfidenceThreshold {
		a.logFront(fmt.Sprintf("🛡️ [ЗАХИСТ] Gemini невпевнений (%.2f) щодо '%s'. Відхиляємо.", rec.Confidence, fname))
		return storage.Movie{Filename: fname}
	}

	// 🛡️ КРОК 4: КОНТРОЛЬ РОКУ
	parsed := tmdb.ParseFilename(filePath)
	if parsed.Year > 0 {
		if rec.Year != nil {
			diff := *rec.Year - parsed.Year
			// Допускаємо похибку ±1 рік. Якщо більше — логуємо як потенційну галюцинацію,
			// але даємо шанс TMDB верифікувати цей рік.
			if diff < -1 || diff > 1 {
				a.logFront(fmt.Sprintf("⚠️ [ПОПЕРЕДЖЕННЯ] Gemini вказав рік %d для '%s' (у файлі %d). Довіряємо Gemini, але TMDB має це перевірити.", *rec.Year, fname, parsed.Year))
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

	tmdbInfo, err := a.tmdbClient.FetchByCleanTitle(ctx, rec.ENTitle, yearStr, mt)
	if err != nil {
		a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", rec.ENTitle, err))
	}

	if tmdbInfo == nil {
		a.logFront(fmt.Sprintf("❌ TMDB не знайшов '%s' — запис залишається нерозпізнаним", rec.ENTitle))
		return storage.Movie{Filename: fname}
	}

	jw := tmdb.TitleSimilarity(rec.ENTitle, tmdbInfo.TitleEN)
	if tmdbInfo.SearchTitle != "" {
		jwSearch := tmdb.TitleSimilarity(rec.ENTitle, tmdbInfo.SearchTitle)
		if jwSearch > jw {
			jw = jwSearch
		}
	}
	if tmdbInfo.MatchedAlias != "" {
		jwAlias := tmdb.TitleSimilarity(rec.ENTitle, tmdbInfo.MatchedAlias)
		if jwAlias > jw {
			jw = jwAlias
		}
	}

	if jw < geminiTMDBVerifyMinJW {
		a.logFront(fmt.Sprintf(
			"🛡️ [POST-VERIFY] Відхилено '%s': Gemini '%s' ≠ TMDB '%s' (Search: '%s', Alias: '%s', схожість %.2f)",
			fname, rec.ENTitle, tmdbInfo.TitleEN, tmdbInfo.SearchTitle, tmdbInfo.MatchedAlias, jw,
		))
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
	movie.MediaType = string(tmdbInfo.MediaType)

	return movie
}

// ── Ручне виправлення ─────────────────────────────────────────────────────────

type FixRequest struct {
	Filename string `json:"filename"`
	Hint     string `json:"hint"`
}

// FixSelected — виправлення вибраних записів.
// hint може бути: TMDB URL/ID, назва фільму, рік, або порожнє (→ Gemini)
func (a *App) FixSelected(selected []FixRequest) {
	a.scanMutex.Lock()
	if a.isScanning {
		a.scanMutex.Unlock()
		a.logFront("⚠️ Сканування вже йде. Ігнорую виправлення.")
		return
	}
	a.isScanning = true
	a.scanMutex.Unlock()

	a.wg.Add(1)

	// 1. Створюємо керований контекст з гарантованим trace_id 🟢
	ctx, cancel := context.WithCancel(utils.EnsureTrace(a.ctx))
	a.setScanCancel(cancel)
	defer func() {
		cancel()
		a.clearScanCancel()
		a.scanMutex.Lock()
		a.isScanning = false
		a.scanMutex.Unlock()
		a.wg.Done()
	}()

	wailsRuntime.EventsEmit(a.ctx, "scan-started")

	var withHint []FixRequest
	var geminiQueue []string

	for _, s := range selected {
		if s.Filename == "" {
			utils.LoggerWithTrace(ctx).Warn("fix_selected_invalid_filename", slog.Any("entry", s))
			continue
		}

		if s.Hint != "" && s.Hint != "skip" {
			withHint = append(withHint, s)
		} else {
			geminiQueue = append(geminiQueue, filepath.Join(a.cfg.MediaFolderPath, s.Filename))
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
			a.finalizeScan(a.ctx, "Виправлення перервано користувачем")
			return
		}

		current++
		a.emitProgress(current, total, "🔄 "+fix.Filename)

		// Передаємо локальний ctx
		if err := a.updateMovie(ctx, fix.Filename, fix.Hint); err == nil {
			if m, err := a.db.GetMovieByFilename(ctx, fix.Filename); err == nil && m != nil {
				if m.TitleUA != "" && m.Plot != "" && utils.HasCyrillic(m.TitleUA) {
					a.logFront(fmt.Sprintf("🎯 [TMDB Істина] Пропуск черги локалізації для '%s' (офіційний переклад та опис вже є)", m.TitleUA))
					continue
				}
			}
			translationQueue = append(translationQueue, fix.Filename)
		}
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

	a.finalizeScan(a.ctx, fmt.Sprintf("Виправлено %d файлів", total))
}

// UpdateMovie — Wails API: оновлення одного запису за hint від користувача.
func (a *App) UpdateMovie(filename, hint string) error {
	ctx := utils.EnsureTrace(a.ctx)
	return a.updateMovie(ctx, filename, hint)
}

// updateMovie — внутрішня реалізація з контекстом (FixSelected, тести).
// hint може бути: TMDB URL (themoviedb.org/movie/123), числовий ID, або текстова назва/рік.
func (a *App) updateMovie(ctx context.Context, filename, hint string) error {
	hint = strings.TrimSpace(hint)

	existing, err := a.db.GetMovieByFilename(ctx, filename)
	if err != nil {
		slog.Warn("get_existing_movie_failed", slog.String("file", filename), slog.Any("error", err))
	}
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
			if info.TitleUA != "" && utils.HasCyrillic(info.TitleUA) {
				applyTMDBToMovie(existing, info)
				a.logFront(fmt.Sprintf("🎯 [TMDB Істина] Знайдено офіційний переклад '%s', пропуск Gemini", info.TitleUA))
				return a.db.SaveMovie(ctx, *existing)
			}
			applyTMDBToMovie(existing, info)

			return a.db.SaveMovie(ctx, *existing)
		}
	}

	// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Варіант 1.5 - Прямий пошук у TMDB за підказкою
	// Обходимо анти-галюцинаційні фільтри (зокрема жорсткий блок по році)
	if hint != "" {
		origParsed := tmdb.ParseFilename(filename)
		hintParsed := tmdb.ParseFilename(hint)

		searchTitle := hintParsed.CleanTitle
		if searchTitle == "" {
			searchTitle = origParsed.CleanTitle // Якщо підказка - це тільки рік (напр. "2024")
		}

		searchYear := origParsed.Year
		if hintParsed.Year > 0 {
			searchYear = hintParsed.Year // Пріоритет року з підказки
		}

		targetToSearch := tmdb.ParsedFile{
			CleanTitle: searchTitle,
			Year:       searchYear,
			MediaType:  origParsed.MediaType,
		}

		a.logFront(fmt.Sprintf("🔍 [%s] Прямий пошук TMDB за: '%s' (рік: %d)", filename, searchTitle, searchYear))

		// Використовуємо SearchWithFallbacks напряму.
		// Він використає рік для сортування, але не відхилить ідеальний збіг по назві, якщо рік відрізняється.
		info, err := a.tmdbClient.SearchWithFallbacks(ctx, targetToSearch, filename)

		if err == nil && info != nil {
			if info.TitleUA != "" && utils.HasCyrillic(info.TitleUA) {
				applyTMDBToMovie(existing, info)
				a.logFront(fmt.Sprintf("🎯 [TMDB Істина] Знайдено офіційний переклад за підказкою '%s', пропуск Gemini", info.TitleUA))
				return a.db.SaveMovie(ctx, *existing)
			}
			a.logFront(fmt.Sprintf("✅ TMDB знайшов за підказкою: '%s'", info.TitleUA))
			applyTMDBToMovie(existing, info)
			return a.db.SaveMovie(ctx, *existing)
		}

		a.logFront("⚠️ Прямий пошук не дав результату, підключаємо Gemini...")
	}

	// Варіант 2: текстова підказка не дала результату → Gemini → TMDB
	a.logFront(fmt.Sprintf("🧠 [%s] Аналіз через Gemini...", filename))

	geminiCtx := ai.FileRecognitionContextFromPath(filename)
	if hint != "" {
		geminiCtx.OriginalFile = fmt.Sprintf("%s (підказка: %s)", filename, hint)
	}

	results, err := a.aiClient.RecognizeBulk(ctx, []ai.FileRecognitionContext{geminiCtx})
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
	movie := a.mergeGeminiWithTMDB(ctx, filepath.Join(a.cfg.MediaFolderPath, filename), rec)

	if movie.TmdbID > 0 {
		yearVal := 0
		if rec.Year != nil {
			yearVal = *rec.Year
		}
		if err := a.db.SaveAIResolution(ctx, storage.AIResolution{
			OriginalFilename: filename,
			ResolvedTitle:    rec.ENTitle,
			Year:             yearVal,
			MediaType:        rec.MediaType,
			Confidence:       rec.Confidence,
		}); err != nil {
			slog.Warn("save_ai_resolution_failed",
				slog.String("file", filename), slog.Any("error", err))
		}
	}

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

// getFileIdentifier повертає відносний шлях до файлу (захищає від колізій імен файлів)
func (a *App) getFileIdentifier(p string) string {
	rel, err := filepath.Rel(a.cfg.MediaFolderPath, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(p) // Фоллбек, якщо файл поза медіатекою
	}
	return filepath.ToSlash(rel)
}

// filterUnprocessed повертає файли яких немає в БД або які нерозпізнані
func (a *App) filterUnprocessed(ctx context.Context, diskPaths []string) []string {
	movies, err := a.db.GetAllMovies(ctx)
	if err != nil {
		slog.Error("filter_unprocessed_get_movies_failed", slog.Any("error", err))
		return nil
	}
	recognized := make(map[string]bool, len(movies))
	for _, m := range movies {
		// Файл вважається розпізнаним, ТІЛЬКИ якщо ми знайшли його в TMDB (є ID)
		if m.TmdbID > 0 {
			recognized[m.Filename] = true
		}
	}

	var result []string
	for _, p := range diskPaths {
		fname := a.getFileIdentifier(p)
		if !recognized[fname] {
			result = append(result, p)
		}
	}
	return result
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
		MediaType:       string(info.MediaType),
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
	movie.MediaType = string(info.MediaType)
}

// extractTMDBID витягує TMDB ID та тип медіа з підказки користувача.
// Підтримує: https://themoviedb.org/movie/123, /tv/456, або просто "123456"
func extractTMDBID(hint string) (int, tmdb.MediaType) {
	// TMDB URL з типом
	if m := reTMDBURL.FindStringSubmatch(hint); len(m) > 2 {
		id, err := strconv.Atoi(m[2])
		if err != nil {
			slog.Warn("tmdb_url_id_parse_failed", slog.String("hint", hint), slog.Any("error", err))
			return 0, tmdb.MediaTypeMovie
		}
		mt := tmdb.MediaTypeMovie
		if m[1] == "tv" {
			mt = tmdb.MediaTypeTV
		}
		return id, mt
	}

	// Чистий числовий ID (більше 4 цифр щоб не сплутати з роком)
	if reTMDBID.MatchString(hint) {
		id, err := strconv.Atoi(hint)
		if err != nil {
			slog.Warn("tmdb_id_parse_failed", slog.String("hint", hint), slog.Any("error", err))
			return 0, tmdb.MediaTypeMovie
		}
		return id, tmdb.MediaTypeMovie
	}

	return 0, tmdb.MediaTypeMovie
}

func (a *App) emitProgress(current, total int, filename string) {
	wailsRuntime.EventsEmit(a.ctx, "scan-progress", map[string]interface{}{
		"current": current, "total": total, "filename": filename,
	})
}

func (a *App) finalizeScan(ctx context.Context, msg string) {
	movies, err := a.db.GetAllMovies(ctx)
	if err != nil {
		slog.Warn("finalize_scan_get_movies_failed", slog.Any("error", err))
	}
	if err := web.Generate(a.cfg, movies, false); err != nil {
		slog.Error("web_generate_failed", slog.Any("error", err))
	}
	wailsRuntime.EventsEmit(a.ctx, "scan-finished", msg)
	a.logFront("🏁 [ФІНАЛ] " + msg)
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

func (a *App) movieInfoNeedsTranslation(info *tmdb.MovieInfo) bool {
	if info == nil {
		return false
	}
	needTitle := info.TitleUA == "" || needsTranslation(info.TitleUA)
	needPlot := info.Plot == "" || needsTranslation(info.Plot)
	return needTitle || needPlot
}

// needsTranslation повертає true, якщо текст треба перекласти (англійська або підозріла кирилиця)
func needsTranslation(s string) bool {
	if s == "" {
		return true // Порожнечу завжди наповнюємо
	}

	sLower := strings.ToLower(s)

	// 1. ПЕРЕВІРКА НА СЛОВА-МАРКЕРИ (Російські слова, що пишуться спільними літерами)
	// Використовуємо регулярний вираз з межами слова, щоб уникнути хибних спрацьовувань на підрядках.
	if russianMarkersRE.MatchString(sLower) {
		return true
	}

	foundCyrillic := false
	foundRussianLetter := false
	foundUkrainianLetter := false

	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			foundCyrillic = true
		}
		// Яскраві маркери російської (ы, э, ъ, ё)
		if r == 'ы' || r == 'э' || r == 'ъ' || r == 'ё' || r == 'Ы' || r == 'Э' || r == 'Ъ' || r == 'Ё' {
			foundRussianLetter = true
		}
		// Яскраві маркери української (і, ї, є, ґ)
		if r == 'і' || r == 'ї' || r == 'є' || r == 'ґ' || r == 'І' || r == 'Ї' || r == 'Є' || r == 'Ґ' {
			foundUkrainianLetter = true
		}
	}

	// 2. Немає кирилиці (англійська) -> перекладаємо
	if !foundCyrillic {
		return true
	}
	// 3. Є специфічні російські літери -> перекладаємо
	if foundRussianLetter {
		return true
	}

	// 5. СІРА ЗОНА: якщо немає специфічних українських літер (і, ї, є, ґ),
	// але є кирилиця та немає російських маркерів — скоріш за все OK.
	// Якщо є російські маркери — вже обробили вище.
	if !foundUkrainianLetter {
		return len([]rune(strings.TrimSpace(s))) > 5
	}

	return false
}

func (a *App) processTranslationQueue(ctx context.Context, filenames []string, aiClient *ai.Client) {
	a.logFront(fmt.Sprintf("🌍 Аналіз локалізації для %d файлів...", len(filenames)))

	var itemsToTranslate []ai.BulkTranslateItem
	movieMap := make(map[string]storage.Movie)

	// 1. Фільтруємо чергу: збираємо ТІЛЬКИ те, що дійсно треба перекладати
	// 🟢 ОПТИМІЗАЦІЯ: Один масований запит замість N окремих
	movies, err := a.db.GetMoviesByFilenames(ctx, filenames)
	if err != nil {
		slog.Error("failed_to_fetch_movies_for_translation", slog.Any("error", err))
		return
	}

	for _, fname := range filenames {
		if ctx.Err() != nil {
			a.logFront("🛑 Підготовку перервано.")
			return
		}

		movie, ok := movies[fname]
		if !ok {
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

			item := ai.BulkTranslateItem{
				Filename:      fname,
				Title:         fallbackTitle,
				OriginalTitle: movie.TitleEN,
			}
			if needPlot {
				item.Plot = movie.Plot
			}
			itemsToTranslate = append(itemsToTranslate, item)
			movieMap[fname] = movie
		}
	}

	// Diagnostic: count how many movies in the DB appear to need translation
	// but were not added to the translation payload (for later inspection).
	needCount := 0
	for _, m := range movies {
		if m.TitleUA == "" || needsTranslation(m.TitleUA) || m.Plot == "" || needsTranslation(m.Plot) {
			needCount++
		}
	}
	skipped := needCount - len(itemsToTranslate)
	if skipped > 0 {
		slog.Warn("translation_candidates_skipped",
			slog.Int("need_count", needCount),
			slog.Int("queued", len(itemsToTranslate)),
			slog.Int("skipped", skipped))
		a.logFront(fmt.Sprintf("⚠️ Пропущено %d файлів, які, можливо, потребують перекладу (див. logs).", skipped))
	}

	if len(itemsToTranslate) == 0 {
		a.logFront("✅ Усі файли вже мають коректну локалізацію.")
		return
	}

	a.logFront(fmt.Sprintf("🚀 Відправка в Gemini: %d файлів...", len(itemsToTranslate)))

	const batchSize = 20
	totalItems := len(itemsToTranslate)
	totalBatches := (totalItems + batchSize - 1) / batchSize
	var updatedCount int32

	for i := 0; i < totalItems; i += batchSize {
		if ctx.Err() != nil {
			a.logFront("🛑 Переклад перервано.")
			return
		}

		end := i + batchSize
		if end > totalItems {
			end = totalItems
		}
		batch := itemsToTranslate[i:end]
		currentBatchIdx := i/batchSize + 1

		a.logFront(fmt.Sprintf("📦 Переклад: пачка %d/%d (%d файлів)...", currentBatchIdx, totalBatches, len(batch)))

		results, err := aiClient.TranslateBulk(ctx, batch)
		if err != nil {
			a.logFront(fmt.Sprintf("⚠️ Помилка перекладу пачки %d: %v", currentBatchIdx, err))
			continue
		}

		var moviesToSave []storage.Movie
		for _, res := range results {
			movie, ok := movieMap[res.Filename]
			if !ok {
				continue
			}

			changed := false
			if res.Title != "" && res.Title != movie.TitleUA && !strings.HasPrefix(strings.TrimSpace(res.Title), "<think>") {
				// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Якщо TMDB вже дав надійну українську назву,
				// Gemini не може її перезаписати навіть кириличним калькою.
				if utils.IsGoodUkrainian(movie.TitleUA) {
					slog.Debug("localization_skip_ai_override_trusted_tmdb",
						slog.String("file", movie.Filename),
						slog.String("kept", movie.TitleUA),
						slog.String("rejected", res.Title))
				} else if utils.HasCyrillic(movie.TitleUA) && !utils.HasCyrillic(res.Title) {
					slog.Debug("localization_skip_downgrade",
						slog.String("file", movie.Filename),
						slog.String("kept", movie.TitleUA),
						slog.String("rejected", res.Title))
				} else {
					movie.TitleUA = res.Title
					changed = true
				}
			}
			if res.Plot != "" && res.Plot != movie.Plot {
				movie.Plot = res.Plot
				changed = true
			}

			if changed {
				moviesToSave = append(moviesToSave, movie)
				atomic.AddInt32(&updatedCount, 1)
				a.logFront(fmt.Sprintf("✅ Адаптовано: '%s'", movie.TitleUA))
			}
		}

		if len(moviesToSave) > 0 {
			if err := a.db.SaveMoviesBatch(ctx, moviesToSave); err != nil {
				slog.Error("translation_batch_save_failed", slog.Any("error", err))
			}
		}
	}

	if ctx.Err() == nil {
		a.logFront(fmt.Sprintf("✅ Фаза перекладу завершена! Оновлено записів: %d", updatedCount))
	}
}
