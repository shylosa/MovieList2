package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/sync/singleflight"
	"google.golang.org/genai"

	"movielist-app/internal/ai"
	"movielist-app/internal/config"
	"movielist-app/internal/scanner"
	"movielist-app/internal/sheets"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
	"movielist-app/internal/utils"
	"movielist-app/internal/version"
	"movielist-app/internal/web"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx                        context.Context
	cfg                        *config.Config
	db                         *storage.DB
	tmdbClient                 *tmdb.Client
	aiClient                   *ai.Client
	aiModelsCache              []string
	discoveredGrokModels       []string
	discoveredGroqModels       []string
	discoveredAIModels         []string
	aiModelsHTTPClient         *http.Client
	modelsMutex                sync.RWMutex
	configFileMutex            sync.Mutex
	modelsGroup                singleflight.Group
	scanCancel                 context.CancelFunc
	isScanning                 bool
	scanMutex                  sync.Mutex
	isGitHubSyncing            bool
	githubSyncMutex            sync.Mutex
	isCloudSyncing             bool
	cloudSyncMutex             sync.Mutex
	wg                         sync.WaitGroup
	gitRunner                  func(context.Context, string, string, ...string) ([]byte, error)
	eventEmitter               func(context.Context, string, ...interface{})
	diskFileScanner            func(context.Context) ([]string, error)
	candidateTranslationRunner func(context.Context, []string, map[string]int)
	repairMutex                sync.Mutex
	repairCancel               context.CancelFunc
	repairClosing              bool
	repairDetailsFetcher       func(context.Context, tmdb.MediaType, int) (*tmdb.MovieInfo, error)
	repairTranslator           func(context.Context, []ai.BulkTranslateItem) ([]ai.BulkTranslateItem, error)
}

type AIModelCatalog struct {
	Current   []string `json:"current"`
	Available []string `json:"available"`
}

var (
	reTMDBURL              = regexp.MustCompile(`themoviedb\.org/(movie|tv)/(\d+)`)
	reTMDBID               = regexp.MustCompile(`^\d{5,}$`) // TMDB IDs: мін. 5 цифр, щоб не сплутати з роком
	reIMDBHint             = regexp.MustCompile(`(?i)(?:imdb\.com/title/)?(tt\d{7,10})`)
	candidateReleaseTagRE  = regexp.MustCompile(`(?i)\b(?:mkv|mp4|avi|mov|HDTVRip|HDTV|WEB-DLRip|WEB-DL|WEBRip|HDRip|BDRip|BluRay|DVDRip|GeneralFilm|1080p|720p|2160p|x26[45]|h26[45]|HEVC)\b`)
	candidateWeakEpisodeRE = regexp.MustCompile(`(?:^|\s)\d{1,3}(?:$|\s)`)
	candidateSpaceRE       = regexp.MustCompile(`\s{2,}`)
)

func NewApp() *App {
	return &App{
		aiModelsHTTPClient: &http.Client{Timeout: 10 * time.Second},
		gitRunner: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = dir
			return cmd.CombinedOutput()
		},
		eventEmitter: wailsRuntime.EventsEmit,
	}
}

func (a *App) emitEvent(ctx context.Context, name string, data ...interface{}) {
	if a.eventEmitter != nil && ctx != nil {
		a.eventEmitter(ctx, name, data...)
	}
}

func (a *App) getDiskFiles(ctx context.Context) ([]string, error) {
	if a.diskFileScanner != nil {
		return a.diskFileScanner(ctx)
	}
	return scanner.NewScanner(a.cfg).GetDiskFiles(ctx)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.cfg == nil {
		a.cfg = config.Load()
	}
	slog.Info("app_started", slog.String("version", version.Current))

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
	a.restoreMediaFolder(ctx)
	a.restoreExcludedFolders()
	a.restoreScanFolders()
	a.tmdbClient = tmdb.NewClient(a.cfg)
	a.aiClient = ai.NewClient(a.cfg)
	if err := a.auditDuplicateMovieIdentities(ctx); err != nil {
		slog.Warn("duplicate_identity_audit_failed", slog.Any("error", err))
	}
}

func (a *App) restoreMediaFolder(ctx context.Context) {
	selected := strings.TrimSpace(a.db.GetState(ctx, "media_folder_path"))
	if selected == "" {
		return
	}
	if info, err := os.Stat(selected); err == nil && info.IsDir() {
		a.cfg.MediaFolderPath = selected
	} else {
		slog.Warn("saved_media_folder_unavailable", slog.String("path", selected), slog.Any("error", err))
	}
}

func (a *App) shutdown(ctx context.Context) {
	slog.Info("app_closed")
	a.repairMutex.Lock()
	a.repairClosing = true
	if a.repairCancel != nil {
		a.repairCancel()
	}
	a.repairMutex.Unlock()
	a.cancelScan() // спочатку сигналізуємо зупинку всім горутинам
	a.wg.Wait()    // потім чекаємо graceful завершення
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
		a.emitEvent(a.ctx, "log-message", msg)
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
	a.scanMutex.Lock()
	defer a.scanMutex.Unlock()
	for i := range movies {
		if err := a.ctx.Err(); err != nil {
			return nil, err
		}
		movies[i].FileLabel = utils.DisplayFileLabel(movies[i].Filename)
		if a.cfg != nil {
			movies[i].FilePath = a.cfg.ResolveMediaPath(movies[i].Filename)
		}
	}
	return movies, nil
}

func (a *App) GetStats() map[string]interface{} {
	total, unrec, suspicious, err := a.db.GetStatsCounts(a.ctx)
	if err != nil {
		return map[string]interface{}{"total": 0, "unrec": 0, "last": "Помилка"}
	}

	lastScan := "Ніколи"
	if val := a.db.GetState(a.ctx, "last_scan_at"); val != "" {
		lastScan = val
	}
	return map[string]interface{}{
		"total":      total,
		"unrec":      unrec,
		"suspicious": suspicious,
		"last":       lastScan,
	}
}

func (a *App) GetAppVersion() string {
	return version.Current
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
	logger := utils.LoggerWithTrace(utils.EnsureTrace(a.ctx))
	oldID := 0
	if m != nil {
		oldID = m.TmdbID
	}
	logger.Debug("catalog_record_deleted", slog.String("file", filename), slog.Int("previous_tmdb_id", oldID))

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
	if path == "" {
		return "", nil
	}
	return a.setMediaFolder(path)
}

func (a *App) setMediaFolder(path string) (string, error) {
	a.scanMutex.Lock()
	defer a.scanMutex.Unlock()
	if a.isScanning {
		return "", fmt.Errorf("cannot change media folder while scanning")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("selected media folder: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("selected media path is not a directory")
	}
	if err := a.db.SetState(a.ctx, "media_folder_path", path); err != nil {
		return "", fmt.Errorf("save selected media folder: %w", err)
	}
	state, err := json.Marshal(savedScanFolders{Root: path, Folders: []string{path}, Sources: a.cfg.MediaSources})
	if err != nil {
		return "", err
	}
	if err := a.db.SetState(a.ctx, "scan_folders", string(state)); err != nil {
		return "", err
	}
	a.cfg.MediaFolderPath = path
	a.cfg.MediaFolders = []string{path}
	if a.tmdbClient != nil {
		a.tmdbClient.SetMediaRoot(path)
	}
	slog.Info("media_folder_selected", slog.String("path", path))
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
	a.cloudSyncMutex.Lock()
	if a.isCloudSyncing {
		a.cloudSyncMutex.Unlock()
		a.logFront("⚠️ Синхронізація з хмарою вже йде. Ігнорую повторний виклик.")
		return
	}
	a.isCloudSyncing = true
	a.cloudSyncMutex.Unlock()

	defer func() {
		a.cloudSyncMutex.Lock()
		a.isCloudSyncing = false
		a.cloudSyncMutex.Unlock()
	}()

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
			a.emitEvent(a.ctx, "github-sync-finished", map[string]interface{}{
				"success": success,
				"message": msg,
			})
		}

		a.emitEvent(a.ctx, "github-sync-started")
		a.logFront("📱 Підготовка мобільної вітрини для GitHub Pages...")
		success, msg := a.syncToGitHub()
		emitFinished(success, msg)
	}()
}

func (a *App) syncToGitHub() (bool, string) {
	movies, err := a.db.GetAllMovies(a.ctx)
	if err != nil {
		return false, "❌ Помилка читання БД: " + err.Error()
	}
	if len(movies) == 0 {
		return false, "⚠️ База порожня, нічого публікувати."
	}

	repoDir, err := a.gitRepoRoot()
	if err != nil {
		return false, "❌ Git репозиторій не знайдено: " + err.Error()
	}
	mobileCfg := *a.cfg
	mobileCfg.HTMLPath = filepath.Join(repoDir, "index.html")
	if err := web.Generate(&mobileCfg, movies, true); err != nil {
		return false, fmt.Sprintf("❌ Помилка генерації index.html: %v", err)
	}
	if err := a.deployToGitHubPagesIn(repoDir); err != nil {
		return false, "❌ GitHub Pages: " + err.Error()
	}
	return true, "✅ GitHub Pages оновлено!"
}

func (a *App) gitRepoRoot() (string, error) {
	out, err := a.gitRunner(a.ctx, "", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		a.logGitFailure("rev-parse", out, err)
		diagnostic := safeGitDiagnostic(string(out))
		if diagnostic == "" {
			diagnostic = safeGitDiagnostic(err.Error())
		}
		return "", fmt.Errorf("%s", diagnostic)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("empty git root")
	}
	return filepath.FromSlash(root), nil
}

func (a *App) deployToGitHubPagesIn(workDir string) error {
	run := func(args ...string) error {
		out, err := a.gitRunner(a.ctx, workDir, args[0], args[1:]...)
		if err != nil {
			a.logGitFailure(args[1], out, err)
			a.logFront(fmt.Sprintf("❌ git %s: %s", args[1], safeGitDiagnostic(string(out))))
		}
		return err
	}

	if err := run("git", "add", "-f", "index.html"); err != nil {
		return err
	}

	// commit може повернути ненульовий код якщо "nothing to commit" — це не помилка
	_ = run("git", "commit", "--only", "-m", fmt.Sprintf("Update mobile showcase %s",
		time.Now().Format("2006-01-02 15:04")), "--", "index.html")

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

// GetAIModelCatalog refreshes Gemini's model list and returns configured models
// separately from every compatible model reported by the API.
func (a *App) GetAIModelCatalog() (AIModelCatalog, error) {
	current := a.configuredGeminiModels()
	if a.cfg.GeminiAPIKey == "" {
		return AIModelCatalog{Current: current}, fmt.Errorf("GEMINI_API_KEY not configured")
	}
	value, err, _ := a.modelsGroup.Do("gemini_catalog", func() (interface{}, error) {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		client := a.aiModelsHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		genClient, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: a.cfg.GeminiAPIKey, Backend: genai.BackendGeminiAPI, HTTPClient: client})
		if err != nil {
			return nil, fmt.Errorf("Gemini catalog initialization failed")
		}
		available := []string{}
		for model, listErr := range genClient.Models.All(ctx) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if listErr != nil {
				return nil, fmt.Errorf("Gemini catalog request failed")
			}
			name := strings.TrimPrefix(model.Name, "models/")
			if isUsableGeminiModel(name, model.SupportedActions) {
				available = append(available, name)
			}
		}
		sort.Strings(available)
		a.modelsMutex.Lock()
		a.discoveredAIModels = append([]string(nil), available...)
		a.modelsMutex.Unlock()
		if a.aiClient != nil {
			a.aiClient.RefreshModelAvailability()
		}
		return available, nil
	})
	if err != nil {
		return AIModelCatalog{Current: current}, err
	}
	return AIModelCatalog{Current: current, Available: value.([]string)}, nil
}

func (a *App) SetAIModels(names []string) error {
	return a.SetProviderModels("gemini", names)
}

func (a *App) configuredGeminiModels() []string {
	if a.db != nil {
		var selected []string
		if raw := a.db.GetState(a.ctx, "selected_gemini_models"); raw != "" && json.Unmarshal([]byte(raw), &selected) == nil && len(selected) > 0 {
			return selected
		}
	}
	return append([]string(nil), a.cfg.GeminiModels...)
}

// fetchAIModels is the internal implementation that accepts a context.
// Discovery shares the same singleflight request as the settings catalog.
func (a *App) fetchAIModels(ctx context.Context) ([]string, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if a.cfg.GeminiAPIKey == "" {
		if a.cfg.GroqAPIKey != "" {
			return a.configuredGroqModels(), nil
		}
		if a.cfg.GrokAPIKey != "" {
			return a.configuredGrokModels(), nil
		}
		return nil, fmt.Errorf("no AI API key configured")
	}
	a.modelsMutex.RLock()
	cached := append([]string(nil), a.aiModelsCache...)
	a.modelsMutex.RUnlock()
	if len(cached) > 0 {
		return cached, nil
	}
	catalog, err := a.GetAIModelCatalog()
	configured := a.configuredGeminiModels()
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		fallback := []string{}
		for _, name := range configured {
			if isUsableGeminiModel(name, []string{"generateContent"}) {
				fallback = append(fallback, name)
			}
		}
		if len(fallback) == 0 {
			return nil, err
		}
		slog.Warn("ai_models_discovery_fallback", slog.Any("error", err))
		return fallback, nil
	}
	discovered := make(map[string]bool)
	for _, name := range catalog.Available {
		discovered[name] = true
	}
	names := selectConfiguredGeminiModels(configured, discovered)
	if len(names) == 0 {
		return nil, fmt.Errorf("no configured Gemini generateContent models are available")
	}
	a.modelsMutex.Lock()
	a.aiModelsCache = append([]string(nil), names...)
	a.modelsMutex.Unlock()
	return names, nil
}

func isUsableGeminiModel(name string, methods []string) bool {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, "gemini-") || strings.Contains(lower, "preview") ||
		strings.Contains(lower, "embedding") || strings.Contains(lower, "image") ||
		strings.Contains(lower, "tts") || strings.Contains(lower, "audio") {
		return false
	}
	for _, method := range methods {
		if method == "generateContent" {
			return true
		}
	}
	return false
}

func selectConfiguredGeminiModels(configured []string, discovered map[string]bool) []string {
	if len(configured) == 0 {
		selected := make([]string, 0, len(discovered))
		for name := range discovered {
			selected = append(selected, name)
		}
		sort.Strings(selected)
		return selected
	}
	selected := make([]string, 0, len(configured))
	for _, name := range configured {
		if discovered[name] {
			selected = append(selected, name)
		} else {
			slog.Warn("gemini_configured_model_unavailable", slog.String("model", name))
		}
	}
	return selected
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

	// 🟢 Створюємо контекст ДО запуску горутини (усуває race з StopScan).
	// Якщо StopScan() викликається між wg.Add і setScanCancel — cancelScan() був silent no-op.
	ctx, cancel := context.WithCancel(a.modelContext(a.ctx))
	a.setScanCancel(cancel)

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		// ctx і cancel доступні через closure — не створювати повторно

		// 2. Trace ID для всієї сесії сканування
		scanTraceID := uuid.New().String()[:8]
		scanCtx := utils.ContextWithTrace(ctx, scanTraceID)
		scanStartedAt := time.Now()
		utils.LoggerWithTrace(scanCtx).Info("scan_session_start")
		diskTotal := 0
		processedTotal := 0
		tmdbAccepted := 0
		aiAccepted := 0
		unresolved := 0

		scanFinished := false

		defer func() {
			stoppedByUser := ctx.Err() != nil
			metrics := ai.CallMetrics{}
			if a.aiClient != nil {
				metrics = a.aiClient.CallMetrics()
			}
			tmdbMetrics := tmdb.RequestMetrics{}
			if a.tmdbClient != nil {
				tmdbMetrics = a.tmdbClient.RequestMetrics()
			}
			_, _, suspicious, _ := a.db.GetStatsCounts(scanCtx)
			utils.LoggerWithTrace(scanCtx).Info("scan_completed",
				slog.Duration("duration", time.Since(scanStartedAt)),
				slog.Int("disk_total", diskTotal),
				slog.Int("processed_total", processedTotal),
				slog.Int("tmdb_accepted", tmdbAccepted),
				slog.Int("ai_accepted", aiAccepted),
				slog.Int("needs_review", unresolved+suspicious),
				slog.Int("suspicious", suspicious),
				slog.Int("unresolved", unresolved),
				slog.Int("resolved", tmdbAccepted+aiAccepted),
				slog.Int64("gemini_batch_calls", metrics.RecognitionBatch),
				slog.Int64("gemini_retry_calls", metrics.RecognitionRetry),
				slog.Int64("gemini_disambiguation_calls", metrics.Disambiguation),
				slog.Int64("gemini_generate_calls", metrics.GeminiGenerate),
				slog.Int64("grok_calls", metrics.Grok),
				slog.Int64("groq_calls", metrics.Groq),
				slog.Int64("tmdb_search_calls", tmdbMetrics.SearchCalls),
				slog.Int64("tmdb_details_calls", tmdbMetrics.DetailsCalls),
				slog.Bool("cancelled", stoppedByUser),
			)
			if stoppedByUser {
				utils.LoggerWithTrace(scanCtx).Info("scan_cancelled",
					slog.Duration("duration", time.Since(scanStartedAt)),
				)
			}
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
				a.finalizeScan(msg, !stoppedByUser)
			}
		}()

		a.emitEvent(a.ctx, "scan-started")

		// 🟢 ДОДАНО: Асинхронно прогріваємо кеш моделей, щоб aiClient отримав актуальний список.
		// Tracked у a.wg щоб shutdown не прийшов раніше завершення горутини.
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			// a.ctx (не scanCtx): warmup кешує моделі для всього lifecycle додатку.
			// scanCtx скасовується при завершенні/зупинці скана і передчасно вбиває HTTP-запит.
			if _, err := a.fetchAIModels(a.ctx); err != nil {
				utils.LoggerWithTrace(a.ctx).Debug("ai_models_warmup_failed", slog.Any("error", err))
			}
		}()

		// 🟢 Скидаємо лічильники TMDB-запитів від попереднього сканування
		if a.tmdbClient != nil {
			a.tmdbClient.ResetRequestMetrics()
		}

		// Reset Gemini quota lock so recovered quotas are retried in this session.
		if a.aiClient != nil {
			a.aiClient.ResetQuotaLock()
			a.aiClient.ResetCallMetrics()
		}

		diskPaths, err := a.getDiskFiles(scanCtx)
		if err != nil {
			a.finalizeScan(fmt.Sprintf("❌ Помилка сканування диску: %v", err), false)
			scanFinished = true
			return
		}
		diskTotal = len(diskPaths)

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
		if err := a.backfillMissingRatings(scanCtx); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			utils.LoggerWithTrace(scanCtx).Warn("rating_backfill_failed", slog.Any("error", err))
		}

		// Визначаємо що треба обробити (нові + нерозпізнані)
		filesToProcess := a.filterUnprocessed(scanCtx, diskPaths)
		processedTotal = len(filesToProcess)
		if len(filesToProcess) == 0 {
			a.localizeStoredMovies(scanCtx, nil)
			if scanCtx.Err() != nil {
				return
			}
			a.cleanOrphanPostersAfterSuccess(scanCtx)
			a.finalizeScan("Змін не знайдено.", true)
			scanFinished = true
			return
		}

		a.logFront(fmt.Sprintf("📂 Файлів для обробки: %d", len(filesToProcess)))

		// Автоматичне розпізнавання: TMDB → класифікація → commit → AI → TMDB-верифікація → commit.
		recognition := a.recognizeAutomaticScan(scanCtx, filesToProcess)
		tmdbAccepted = recognition.tmdbAccepted
		aiAccepted = recognition.aiAccepted
		unresolved = recognition.unresolved

		if len(recognition.translationQueue) > 0 {
			a.processTranslationQueue(scanCtx, recognition.translationQueue, a.aiClient, nil)
		}
		a.localizeStoredMovies(scanCtx, recognition.translationQueue)
		if scanCtx.Err() == nil {
			a.cleanOrphanPostersAfterSuccess(scanCtx)
		}
	}()
}

func (a *App) backfillMissingRatings(ctx context.Context) error {
	movies, err := a.db.GetAllMovies(ctx)
	if err != nil {
		return err
	}
	patches := make([]storage.Movie, 0)
	for _, movie := range movies {
		if err := ctx.Err(); err != nil {
			return err
		}
		if movie.TmdbID <= 0 || movie.VoteCount > 0 {
			continue
		}
		mediaType := tmdb.MediaType(movie.MediaType)
		if mediaType != tmdb.MediaTypeMovie && mediaType != tmdb.MediaTypeTV {
			continue
		}
		details, err := a.tmdbClient.GetCandidateDetails(ctx, mediaType, movie.TmdbID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			utils.LoggerWithTrace(ctx).Debug("rating_backfill_item_failed", slog.String("file", movie.Filename), slog.Any("error", err))
			continue
		}
		movie.VoteAverage, movie.VoteCount = details.VoteAverage, details.VoteCount
		patches = append(patches, movie)
	}
	if len(patches) == 0 {
		return nil
	}
	if err := a.db.SaveMoviesBatch(ctx, patches); err != nil {
		return err
	}
	utils.LoggerWithTrace(ctx).Info("rating_backfill_completed", slog.Int("updated", len(patches)))
	return nil
}

func (a *App) cleanOrphanPostersAfterSuccess(ctx context.Context) {
	utils.LoggerWithTrace(ctx).Info("poster_cleanup_started")
	checked, deleted, err := a.db.CleanOrphanPosters(ctx, a.cfg.PostersDir)
	if err != nil {
		utils.LoggerWithTrace(ctx).Warn("poster_cleanup_failed", slog.Any("error", err))
		return
	}
	utils.LoggerWithTrace(ctx).Info("poster_cleanup_completed", slog.Int("checked", checked), slog.Int("deleted", deleted))
}

// StopScan зупиняє поточний процес сканування
func (a *App) StopScan() {
	a.scanMutex.Lock()
	isActive := a.scanCancel != nil
	a.scanMutex.Unlock()

	if !isActive {
		a.logFront("⚠️ [СТОП] Немає активного сканування для зупинки.")
		return
	}

	a.logFront("🚨 [СТОП] Сигнал скасування отримано бекендом!")
	a.cancelScan()
}

// ── Ручне виправлення ─────────────────────────────────────────────────────────

type FixRequest struct {
	Filename  string `json:"filename"`
	Hint      string `json:"hint"`
	MediaType string `json:"media_type,omitempty"`
}

type CandidateSearchRequest struct {
	Filename  string `json:"filename"`
	Title     string `json:"title"`
	MediaType string `json:"media_type"`
}

type CandidateConfirmRequest struct {
	Filename  string `json:"filename"`
	TMDBID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
}

func (a *App) SearchTMDBCandidates(request CandidateSearchRequest) ([]tmdb.TMDBCandidate, error) {
	ctx := a.modelContext(utils.EnsureTrace(a.ctx))
	if strings.TrimSpace(request.Filename) == "" {
		return nil, fmt.Errorf("filename required")
	}
	parsed := tmdb.ParseFilename(request.Filename)
	current, getErr := a.db.GetMovieByFilename(ctx, request.Filename)
	if getErr != nil {
		return nil, fmt.Errorf("candidate lookup current movie: %w", getErr)
	}
	queries := candidateSearchQueries(request.Title, request.Filename, current)
	if len(queries) == 0 {
		return []tmdb.TMDBCandidate{}, nil
	}
	logger := utils.LoggerWithTrace(ctx).With(slog.String("file", request.Filename))
	candidates := currentReviewCandidate(current)
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		seen[fmt.Sprintf("%s:%d", candidate.MediaType, candidate.TMDBID)] = true
	}
	used := queries[0]
	for _, query := range queries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		logger.Info("candidate_search_query", slog.String("source", query.source), slog.String("query", query.title))
		found, err := a.tmdbClient.SearchCandidates(ctx, query.title, candidateSearchYear(parsed.Year, current), request.MediaType)
		if err != nil {
			return nil, err
		}
		before := len(candidates)
		for _, candidate := range excludeCurrentCandidate(found, current) {
			key := fmt.Sprintf("%s:%d", candidate.MediaType, candidate.TMDBID)
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, candidate)
			if len(candidates) == 5 {
				break
			}
		}
		if len(candidates) > before {
			used = query
			break
		}
	}
	movieCount, tvCount := 0, 0
	for _, candidate := range candidates {
		if candidate.MediaType == tmdb.MediaTypeTV {
			tvCount++
		} else {
			movieCount++
		}
	}
	logger.Info("candidate_search_completed", slog.String("source", used.source), slog.String("query", used.title), slog.Int("movie_results", movieCount), slog.Int("tv_results", tvCount), slog.Int("count", len(candidates)))
	return candidates, nil
}

func currentReviewCandidate(current *storage.Movie) []tmdb.TMDBCandidate {
	if current == nil || current.TmdbID <= 0 || !current.NeedsReview {
		return make([]tmdb.TMDBCandidate, 0, 5)
	}
	mediaType := tmdb.MediaType(current.MediaType)
	if mediaType != tmdb.MediaTypeMovie && mediaType != tmdb.MediaTypeTV {
		return make([]tmdb.TMDBCandidate, 0, 5)
	}
	year, _ := strconv.Atoi(current.Year)
	title := current.TitleUA
	if title == "" {
		title = current.TitleEN
	}
	return []tmdb.TMDBCandidate{{
		TMDBID: current.TmdbID, Title: title, OriginalTitle: current.TitleEN,
		Year: year, MediaType: mediaType, Exact: true,
	}}
}

func excludeCurrentCandidate(candidates []tmdb.TMDBCandidate, current *storage.Movie) []tmdb.TMDBCandidate {
	if current == nil || current.TmdbID <= 0 {
		return candidates
	}
	filtered := make([]tmdb.TMDBCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.TMDBID == current.TmdbID && string(candidate.MediaType) == current.MediaType {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func (a *App) auditDuplicateMovieIdentities(ctx context.Context) error {
	movies, err := a.db.GetAllMovies(ctx)
	if err != nil {
		return err
	}
	patches := duplicateMovieIdentitiesForReview(movies)
	if len(patches) == 0 {
		return nil
	}
	if err := a.db.SaveMoviesBatch(ctx, patches); err != nil {
		return err
	}
	utils.LoggerWithTrace(ctx).Info("duplicate_identity_audit_completed", slog.Int("flagged", len(patches)))
	return nil
}

func duplicateMovieIdentitiesForReview(movies []storage.Movie) []storage.Movie {
	groups := make(map[int][]int)
	for i := range movies {
		if movies[i].TmdbID > 0 && movies[i].MediaType != "tv" {
			groups[movies[i].TmdbID] = append(groups[movies[i].TmdbID], i)
		}
	}
	patches := make([]storage.Movie, 0)
	for _, indexes := range groups {
		if len(indexes) < 2 {
			continue
		}
		titles := make(map[string]struct{})
		for _, index := range indexes {
			title := strings.ToLower(strings.TrimSpace(tmdb.ParseFilename(movies[index].Filename).CleanTitle))
			if title != "" {
				titles[title] = struct{}{}
			}
		}
		if len(titles) < 2 {
			continue
		}
		for _, index := range indexes {
			movie := movies[index]
			switch movie.RecognitionSource {
			case "manual_id", "manual_title", "imdb":
				continue
			}
			if movie.NeedsReview && movie.ReviewReason != "" {
				continue
			}
			movie.NeedsReview = true
			movie.ReviewReason = "duplicate_tmdb_id"
			patches = append(patches, movie)
		}
	}
	return patches
}

func candidateSearchTitle(title, filename string) string {
	title = strings.TrimSpace(title)
	if strings.HasPrefix(strings.ToLower(title), "unresolved:") || sameCandidatePlaceholder(title, filename) {
		return ""
	}
	return title
}

type candidateQuery struct{ source, title string }

func candidateSearchQueries(title, filename string, current *storage.Movie) []candidateQuery {
	queries := make([]candidateQuery, 0, 4)
	seen := make(map[string]bool)
	add := func(source, value string) {
		value = strings.TrimSpace(value)
		key := normalizeCandidatePlaceholder(value)
		if value == "" || len([]rune(value)) < 3 || seen[key] {
			return
		}
		seen[key] = true
		queries = append(queries, candidateQuery{source, value})
	}
	add("manual", candidateSearchTitle(title, filename))
	if current != nil && current.TmdbID > 0 {
		add("stored_title", current.TitleEN)
		add("stored_title", current.TitleUA)
	}
	add("filename", cleanCandidateFilenameTitle(filename))
	parent := filepath.Base(filepath.Dir(filename))
	if parent != "." {
		add("parent", cleanCandidateFilenameTitle(parent))
	}
	return queries
}

func cleanCandidateFilenameTitle(value string) string {
	cleaned := tmdb.ParseFilename(filepath.Base(value)).CleanTitle
	cleaned = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(cleaned)
	cleaned = candidateReleaseTagRE.ReplaceAllString(cleaned, " ")
	cleaned = candidateWeakEpisodeRE.ReplaceAllString(cleaned, " ")
	return strings.TrimSpace(candidateSpaceRE.ReplaceAllString(cleaned, " "))
}

func sameCandidatePlaceholder(title, filename string) bool {
	want := normalizeCandidatePlaceholder(title)
	return want != "" && (want == normalizeCandidatePlaceholder(filename) || want == normalizeCandidatePlaceholder(filepath.Base(filename)))
}

func normalizeCandidatePlaceholder(value string) string {
	value = strings.TrimSuffix(strings.TrimSpace(value), filepath.Ext(value))
	value = strings.ToLower(filepath.ToSlash(value))
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return r == '/' || r == '\\' || r == '.' || r == '_' || r == '-' || unicode.IsSpace(r)
	}), " ")
}

func candidateSearchYear(parsedYear int, current *storage.Movie) int {
	if current != nil && current.TmdbID > 0 {
		if year, err := strconv.Atoi(current.Year); err == nil && year > 0 {
			return year
		}
	}
	return parsedYear
}

func (a *App) ConfirmTMDBCandidate(request CandidateConfirmRequest) error {
	ctx := a.modelContext(utils.EnsureTrace(a.ctx))
	startedAt := time.Now()
	if strings.TrimSpace(request.Filename) == "" || request.TMDBID <= 0 {
		return fmt.Errorf("valid filename and tmdb_id required")
	}
	mediaType := tmdb.MediaType(strings.ToLower(strings.TrimSpace(request.MediaType)))
	if mediaType != tmdb.MediaTypeMovie && mediaType != tmdb.MediaTypeTV {
		return fmt.Errorf("media_type must be movie or tv")
	}
	detailsStartedAt := time.Now()
	// Filename is deliberately empty: local poster download belongs to the
	// background completion phase and must not delay the visible selection.
	info, err := a.tmdbClient.GetDetails(ctx, mediaType, request.TMDBID, "")
	if err != nil {
		return err
	}
	detailsDuration := time.Since(detailsStartedAt)
	if info == nil || info.TMDBID <= 0 {
		return fmt.Errorf("TMDB %s/%d not found", mediaType, request.TMDBID)
	}
	existing, err := a.db.GetMovieByFilename(ctx, request.Filename)
	if err != nil {
		return err
	}
	if existing == nil {
		existing = &storage.Movie{Filename: request.Filename}
	}
	applyTMDBToMovie(existing, info)
	existing.RecognitionSource = "manual_id"
	if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing}); err != nil {
		return err
	}
	utils.LoggerWithTrace(ctx).Info("candidate_confirmed",
		slog.String("file", request.Filename), slog.Int("tmdb_id", request.TMDBID), slog.String("media_type", string(mediaType)),
		slog.Duration("details_duration", detailsDuration), slog.Duration("foreground_duration", time.Since(startedAt)))
	needsTranslation := a.aiClient != nil && (existing.TitleUA == "" || needsTranslation(existing.TitleUA) || existing.Plot == "" || needsTranslation(existing.Plot))
	if info.PosterURL != "" || needsTranslation {
		filename, expectedID, expectedMediaType := request.Filename, request.TMDBID, string(mediaType)
		posterURL := info.PosterURL
		backgroundCtx := ctx
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			translationStartedAt := time.Now()
			utils.LoggerWithTrace(backgroundCtx).Info("candidate_background_started", slog.String("file", filename), slog.Int("tmdb_id", expectedID))
			if posterURL != "" {
				posterStartedAt := time.Now()
				localPath, posterErr := a.tmdbClient.DownloadPoster(backgroundCtx, posterURL, fmt.Sprintf("%d_%s", expectedID, filename))
				if posterErr != nil {
					utils.LoggerWithTrace(backgroundCtx).Warn("candidate_poster_failed", slog.String("file", filename), slog.Any("error", posterErr))
				} else if current, ok := a.currentTranslationTarget(backgroundCtx, filename, expectedID, expectedMediaType); ok {
					current.LocalPosterPath = localPath
					if err := a.db.SaveMoviesBatch(backgroundCtx, []storage.Movie{current}); err != nil {
						utils.LoggerWithTrace(backgroundCtx).Warn("candidate_poster_save_failed", slog.String("file", filename), slog.Any("error", err))
					}
				}
				utils.LoggerWithTrace(backgroundCtx).Info("candidate_poster_completed", slog.String("file", filename), slog.Duration("duration", time.Since(posterStartedAt)))
			}
			if needsTranslation {
				utils.LoggerWithTrace(backgroundCtx).Info("candidate_translation_started", slog.String("file", filename), slog.Int("tmdb_id", expectedID))
				if a.candidateTranslationRunner != nil {
					a.candidateTranslationRunner(backgroundCtx, []string{filename}, map[string]int{filename: expectedID})
				} else {
					a.processTranslationQueue(backgroundCtx, []string{filename}, a.aiClient, map[string]int{filename: expectedID})
				}
			}
			if backgroundCtx.Err() == nil {
				a.emitEvent(a.ctx, "movie-updated", map[string]any{"filename": filename, "tmdb_id": expectedID})
			}
			utils.LoggerWithTrace(backgroundCtx).Info("candidate_background_completed", slog.String("file", filename), slog.Int("tmdb_id", expectedID), slog.Duration("duration", time.Since(translationStartedAt)))
		}()
	}
	return nil
}

func (a *App) GetTMDBCandidateDetails(request CandidateConfirmRequest) (*tmdb.CandidateDetails, error) {
	ctx := a.modelContext(utils.EnsureTrace(a.ctx))
	mediaType := tmdb.MediaType(strings.ToLower(strings.TrimSpace(request.MediaType)))
	if request.TMDBID <= 0 || (mediaType != tmdb.MediaTypeMovie && mediaType != tmdb.MediaTypeTV) {
		return nil, fmt.Errorf("valid tmdb_id and media_type required")
	}
	details, err := a.tmdbClient.GetCandidateDetails(ctx, mediaType, request.TMDBID)
	if err != nil {
		return nil, err
	}
	utils.LoggerWithTrace(ctx).Debug("candidate_details_loaded", slog.Int("tmdb_id", request.TMDBID), slog.String("media_type", string(mediaType)))
	return details, nil
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
	ctx, cancel := context.WithCancel(a.modelContext(utils.EnsureTrace(a.ctx)))
	a.setScanCancel(cancel)
	defer func() {
		cancel()
		a.clearScanCancel()
		a.scanMutex.Lock()
		a.isScanning = false
		a.scanMutex.Unlock()
		a.wg.Done()
	}()

	a.emitEvent(a.ctx, "scan-started")
	if a.tmdbClient != nil {
		a.tmdbClient.ResetRequestMetrics()
	}

	var withHint []FixRequest
	var geminiQueue []string

	for _, s := range selected {
		if s.Filename == "" {
			utils.LoggerWithTrace(ctx).Warn("fix_selected_invalid_filename", slog.Any("entry", s))
			continue
		}

		existing, lookupErr := a.db.GetMovieByFilename(ctx, s.Filename)
		if lookupErr != nil {
			utils.LoggerWithTrace(ctx).Warn("fix_selected_existing_lookup_failed", slog.String("file", s.Filename), slog.Any("error", lookupErr))
		}
		if fixRequestUsesDirectPath(s, existing) {
			withHint = append(withHint, s)
		} else {
			geminiQueue = append(geminiQueue, a.cfg.ResolveMediaPath(s.Filename))
		}
	}

	total := len(withHint) + len(geminiQueue)
	current := 0
	a.logFront(fmt.Sprintf("🛠 Виправлення %d файлів...", total))

	var translationQueue []string // 👈 НОВЕ
	succeeded := 0
	failed := 0

	// 2. Перший цикл (ручне виправлення) 🟢
	for _, fix := range withHint {
		// Перевірка, чи не натиснули СТОП
		if ctx.Err() != nil {
			a.logFront("🛑 Виправлення перервано.")
			a.finalizeScan("Виправлення перервано користувачем", false)
			return
		}

		current++
		a.emitProgress(current, total, "🔄 "+fix.Filename)

		// Передаємо локальний ctx
		if err := a.updateMovieWithMediaType(ctx, fix.Filename, fix.Hint, fix.MediaType); err != nil {
			failed++
			utils.LoggerWithTrace(ctx).Warn("fix_selected_item_failed", slog.String("file", fix.Filename), slog.Any("error", err))
			continue
		}
		m, err := a.db.GetMovieByFilename(ctx, fix.Filename)
		if err != nil || m == nil || m.TmdbID == 0 {
			failed++
			utils.LoggerWithTrace(ctx).Warn("fix_selected_item_unresolved", slog.String("file", fix.Filename))
			continue
		}
		succeeded++
		if m.TitleUA != "" && m.Plot != "" && !needsTranslation(m.TitleUA) && !needsTranslation(m.Plot) {
			utils.LoggerWithTrace(ctx).Debug("localization_skipped", slog.String("file", fix.Filename), slog.String("reason", "already_localized"))
			a.logFront(fmt.Sprintf("🎯 [TMDB Істина] Пропуск черги локалізації для '%s' (офіційний переклад та опис вже є)", m.TitleUA))
			continue
		}
		translationQueue = append(translationQueue, fix.Filename)
		utils.LoggerWithTrace(ctx).Debug("localization_queued", slog.String("file", fix.Filename), slog.Int("tmdb_id", m.TmdbID))
	}

	// 3. Другий етап (черга Gemini) 🟢
	if len(geminiQueue) > 0 {
		// Перевіряємо стоп перед початком черги
		if ctx.Err() == nil {
			// ПЕРЕДАЄМО ctx у функцію (як ми домовились раніше)
			recognized := a.processGeminiQueue(ctx, geminiQueue, a.aiClient)
			translationQueue = append(translationQueue, recognized...) // 👈 Додаємо
			succeeded += len(recognized)
			failed += len(geminiQueue) - len(recognized)
		}
	}

	// 🟢 НОВЕ: Запускаємо переклад для виправлених
	if len(translationQueue) > 0 {
		a.processTranslationQueue(ctx, translationQueue, a.aiClient, nil)
	}
	message, success := fixSelectedCompletion(ctx, succeeded, failed)
	if !success {
		a.finalizeScan(message, false)
		return
	}

	utils.LoggerWithTrace(ctx).Info("fix_selected_completed",
		slog.Int("requested", total),
		slog.Int("resolved", succeeded),
		slog.Int("unresolved", failed),
	)
	a.finalizeScan(message, true)
}

func fixRequestUsesDirectPath(request FixRequest, existing *storage.Movie) bool {
	requestedType := strings.ToLower(strings.TrimSpace(request.MediaType))
	return (request.Hint != "" && request.Hint != "skip") || requestedType == "movie" || requestedType == "tv" || (existing != nil && existing.TmdbID > 0)
}

func fixSelectedCompletion(ctx context.Context, succeeded, failed int) (string, bool) {
	if ctx.Err() != nil {
		return "Виправлення перервано користувачем", false
	}
	return fmt.Sprintf("Успішно виправлено: %d; не виправлено: %d", succeeded, failed), true
}

// UpdateMovie — Wails API: оновлення одного запису за hint від користувача.
func (a *App) UpdateMovie(filename, hint string) error {
	ctx := a.modelContext(utils.EnsureTrace(a.ctx))
	if err := a.updateMovie(ctx, filename, hint); err != nil {
		return err
	}
	if a.aiClient == nil {
		return nil
	}
	movie, err := a.db.GetMovieByFilename(ctx, filename)
	if err != nil {
		return err
	}
	if movie != nil && movie.TmdbID > 0 && (movie.TitleUA == "" || needsTranslation(movie.TitleUA) || movie.Plot == "" || needsTranslation(movie.Plot)) {
		a.processTranslationQueue(ctx, []string{filename}, a.aiClient, map[string]int{filename: movie.TmdbID})
	}
	return nil
}

// updateMovie — внутрішня реалізація з контекстом (FixSelected, тести).
// hint може бути: TMDB URL (themoviedb.org/movie/123), числовий ID, або текстова назва/рік.
func (a *App) updateMovie(ctx context.Context, filename, hint string) error {
	return a.updateMovieWithMediaType(ctx, filename, hint, "")
}

func (a *App) updateMovieWithMediaType(ctx context.Context, filename, hint, requestedMediaType string) error {
	hint = strings.TrimSpace(hint)

	existing, err := a.db.GetMovieByFilename(ctx, filename)
	if err != nil {
		slog.Warn("get_existing_movie_failed", slog.String("file", filename), slog.Any("error", err))
	}
	if existing == nil {
		existing = &storage.Movie{Filename: filename}
	}
	requestedMediaType = strings.ToLower(strings.TrimSpace(requestedMediaType))
	strictMediaType := requestedMediaType == "movie" || requestedMediaType == "tv"
	if hint == "" && existing.TmdbID > 0 && (requestedMediaType == "movie" || requestedMediaType == "tv") && requestedMediaType == existing.MediaType {
		existing.NeedsReview = false
		existing.ReviewReason = ""
		existing.RecognitionSource = "manual_id"
		if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing}); err != nil {
			return err
		}
		utils.LoggerWithTrace(ctx).Info("manual_type_confirmed", slog.String("file", filename), slog.Int("tmdb_id", existing.TmdbID), slog.String("media_type", existing.MediaType))
		return nil
	}

	// IMDb URL/ID є однозначною максимальною підказкою: тільки TMDB /find,
	// без title scoring, року filename або Gemini fallback.
	if imdbID := extractIMDBID(hint); imdbID != "" {
		logger := utils.LoggerWithTrace(ctx).With(
			slog.String("hint_type", "imdb"),
			slog.String("imdb_id", imdbID),
			slog.String("file", filename),
		)
		logger.Info("imdb_hint_lookup_started")
		info, err := a.tmdbClient.FetchByIMDB(ctx, imdbID, filename)
		if err != nil {
			logger.Warn("imdb_hint_lookup_failed", slog.Any("error", err))
			return fmt.Errorf("IMDb %s lookup: %w", imdbID, err)
		}
		if info == nil || info.TMDBID == 0 {
			logger.Warn("imdb_hint_not_found")
			return fmt.Errorf("IMDb %s не знайдено в TMDB", imdbID)
		}
		applyTMDBToMovie(existing, info)
		existing.RecognitionSource = "imdb"
		if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing}); err != nil {
			return err
		}
		logger.Info("imdb_hint_resolved",
			slog.Int("tmdb_id", info.TMDBID),
			slog.String("media_type", string(info.MediaType)),
			slog.String("title", info.TitleEN),
		)
		return nil
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
			existing.RecognitionSource = "manual_id"
			if info.TitleUA != "" && utils.HasCyrillic(info.TitleUA) {
				applyTMDBToMovie(existing, info)
				a.logFront(fmt.Sprintf("🎯 [TMDB Істина] Знайдено офіційний переклад '%s', пропуск Gemini", info.TitleUA))
				if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing}); err != nil {
					return err
				}
				return nil
			}
			applyTMDBToMovie(existing, info)

			if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing}); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("TMDB ID %d не знайдено", tmdbID)
	}

	// Ручна текстова назва є авторитетною: тільки exact TMDB lookup, без Gemini
	// і без відхилення через назву/рік, отримані з filename.
	if hint != "" {
		origParsed := tmdb.ParseFilename(filename)
		searchTitle := hint
		searchYear := origParsed.Year
		preferredType := origParsed.MediaType
		strictType := false
		switch strings.ToLower(strings.TrimSpace(requestedMediaType)) {
		case "movie":
			preferredType, strictType = tmdb.MediaTypeMovie, true
		case "tv":
			preferredType, strictType = tmdb.MediaTypeTV, true
		}

		logger := utils.LoggerWithTrace(ctx).With(slog.String("file", filename), slog.String("manual_title", searchTitle))
		logger.Info("manual_title_lookup_started", slog.Int("hint_year", searchYear), slog.String("media_type", string(preferredType)), slog.Bool("strict_media_type", strictType))
		info, err := a.tmdbClient.SearchExactTitle(ctx, searchTitle, searchYear, preferredType, strictType, filename)
		if err != nil {
			logger.Warn("manual_title_lookup_failed", slog.Any("error", err))
			return fmt.Errorf("ручний пошук %q: %w", searchTitle, err)
		}
		if info == nil || info.TMDBID == 0 {
			logger.Warn("manual_title_not_found")
			return fmt.Errorf("точну назву %q не знайдено в TMDB", searchTitle)
		}
		applyTMDBToMovie(existing, info)
		existing.RecognitionSource = "manual_title"
		if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing}); err != nil {
			return err
		}
		logger.Info("manual_title_resolved", slog.Int("tmdb_id", info.TMDBID), slog.String("media_type", string(info.MediaType)), slog.String("title", info.TitleEN))
		logger.Debug("manual_title_saved", slog.String("title_ua", existing.TitleUA), slog.Bool("needs_title_localization", existing.TitleUA == "" || needsTranslation(existing.TitleUA)))
		return nil
	}

	// Варіант 2: порожня підказка → Gemini → TMDB
	a.logFront(fmt.Sprintf("🧠 [%s] Аналіз через Gemini...", filename))

	geminiCtx := ai.FileRecognitionContextFromPath(filename)
	results, err := a.aiClient.RecognizeBulk(ctx, []ai.FileRecognitionContext{geminiCtx})
	if err != nil || results[0] == nil {
		a.logFront(fmt.Sprintf("❌ Gemini не відповів для '%s'", filename))
		return err
	}

	rec := *results[0]
	if rec.ENTitle == "" {
		a.logFront(fmt.Sprintf("❌ Gemini не розпізнав '%s'", filename))
		return nil
	}

	a.logFront(fmt.Sprintf("🤖 Gemini: '%s' → '%s'", filename, rec.ENTitle))
	if strictMediaType {
		rec.MediaType = requestedMediaType
	} else {
		rec = preserveRecognizedContext(rec, existing)
	}
	movie, mergeErr := a.mergeGeminiWithTMDBForType(ctx, a.cfg.ResolveMediaPath(filename), rec, strictMediaType)
	if mergeErr != nil {
		utils.LoggerWithTrace(ctx).Warn("manual_merge_tmdb_error",
			slog.String("file", filename),
			slog.Bool("strict_media_type", strictMediaType),
			slog.Any("error", mergeErr),
		)
		a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", filename, mergeErr))
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("перевірка гіпотези Gemini в TMDB: %w", mergeErr)
	}
	if !strictMediaType && identityReplacementConflicts(existing, movie) {
		utils.LoggerWithTrace(ctx).Warn("identity_replacement_rejected",
			slog.String("file", filename), slog.Int("old_tmdb_id", existing.TmdbID), slog.String("old_media_type", existing.MediaType), slog.String("old_year", existing.Year),
			slog.Int("new_tmdb_id", movie.TmdbID), slog.String("new_media_type", movie.MediaType), slog.String("new_year", movie.Year))
		existing.NeedsReview = true
		existing.ReviewReason = "identity_conflict"
		return a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing})
	}

	// Доповнюємо прогалини лише для тієї самої identity. Під час ручної
	// заміни типу/ID метадані попереднього твору не повинні переноситися.
	if sameMovieIdentity(existing, movie) {
		if movie.TitleUA == "" {
			movie.TitleUA = existing.TitleUA
		}
		if movie.Plot == "" {
			movie.Plot = existing.Plot
		}
	}

	if movie.TmdbID == 0 && existing.TmdbID > 0 {
		return nil
	}
	return a.db.SaveMoviesBatch(ctx, []storage.Movie{movie})
}

func preserveRecognizedContext(rec ai.RecognizedTitle, existing *storage.Movie) ai.RecognizedTitle {
	if existing == nil || existing.TmdbID <= 0 {
		return rec
	}
	if year, err := strconv.Atoi(existing.Year); err == nil && year > 0 {
		rec.Year = &year
	}
	if existing.MediaType == "movie" || existing.MediaType == "tv" {
		rec.MediaType = existing.MediaType
	}
	return rec
}

func identityReplacementConflicts(existing *storage.Movie, replacement storage.Movie) bool {
	return existing != nil && existing.TmdbID > 0 && replacement.TmdbID > 0 &&
		(replacement.TmdbID != existing.TmdbID || replacement.MediaType != existing.MediaType)
}

func sameMovieIdentity(existing *storage.Movie, replacement storage.Movie) bool {
	return existing != nil && existing.TmdbID > 0 && replacement.TmdbID == existing.TmdbID && replacement.MediaType == existing.MediaType
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// getFileIdentifier повертає відносний шлях до файлу (захищає від колізій імен файлів)
func (a *App) getFileIdentifier(p string) string {
	return a.cfg.MediaIdentifier(p)
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

// applyTMDBToMovie — перезаписує поля movie з tmdbInfo (для ручного виправлення)
func applyTMDBToMovie(movie *storage.Movie, info *tmdb.MovieInfo) {
	sameIdentity := movie.TmdbID > 0 && movie.TmdbID == info.TMDBID && movie.MediaType == string(info.MediaType)
	oldTitle, oldPlot := movie.TitleUA, movie.Plot
	movie.TmdbID = info.TMDBID
	movie.VoteAverage = info.VoteAverage
	movie.VoteCount = info.VoteCount
	movie.NeedsReview = false
	movie.ReviewReason = ""
	movie.TitleEN = info.TitleEN
	movie.Year = info.Year
	movie.PosterURL = info.PosterURL
	movie.LocalPosterPath = info.LocalPosterPath
	movie.TitleUA = info.TitleUA
	movie.Plot = info.Plot
	if sameIdentity {
		if oldTitle != "" && !needsTranslation(oldTitle) && needsTranslation(movie.TitleUA) {
			movie.TitleUA = oldTitle
		}
		if oldPlot != "" && !needsTranslation(oldPlot) && needsTranslation(movie.Plot) {
			movie.Plot = oldPlot
		}
	}
	movie.Genres = info.Genres
	movie.Cast = info.Cast
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

func extractIMDBID(hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return ""
	}
	m := reIMDBHint.FindStringSubmatch(hint)
	if len(m) != 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

func (a *App) emitProgress(current, total int, filename string) {
	a.emitEvent(a.ctx, "scan-progress", map[string]interface{}{
		"current": current, "total": total, "filename": filename,
	})
}

func (a *App) finalizeScan(msg string, success bool) {
	movies, err := a.db.GetAllMovies(a.ctx)
	if err != nil {
		slog.Warn("finalize_scan_get_movies_failed", slog.Any("error", err))
	}
	if err := web.Generate(a.cfg, movies, false); err != nil {
		slog.Error("web_generate_failed", slog.Any("error", err))
	}
	// Записуємо час ТІЛЬКИ при успішному завершенні
	if success {
		if err := a.db.SetState(a.ctx, "last_scan_at", time.Now().Format("2006-01-02 15:04")); err != nil {
			utils.LoggerWithTrace(a.ctx).Warn("set_last_scan_at_failed", slog.Any("error", err))
		}
	}
	a.emitEvent(a.ctx, "scan-finished", msg)
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

// localizeStoredMovies retries localization for recognized records from earlier scans.
// Entries already attempted in this scan are excluded to avoid duplicate AI calls.
func (a *App) localizeStoredMovies(ctx context.Context, attempted []string) {
	if a.aiClient == nil || ctx.Err() != nil {
		return
	}
	excluded := make(map[string]struct{}, len(attempted))
	for _, filename := range attempted {
		excluded[filename] = struct{}{}
	}
	var queue []string
	err := a.db.ForEachRecognizedLocalization(ctx, func(filename, titleUA, plot string) error {
		if _, skip := excluded[filename]; !skip && needsStoredLocalization(titleUA, plot) {
			queue = append(queue, filename)
		}
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			utils.LoggerWithTrace(ctx).Warn("localization_backfill_read_failed", slog.Any("error", err))
		}
		return
	}
	if len(queue) > 0 {
		a.processTranslationQueue(ctx, queue, a.aiClient, nil)
	}
}

func needsStoredLocalization(titleUA, plot string) bool {
	return titleUA == "" || needsTranslation(titleUA) || (plot != "" && needsTranslation(plot))
}

// needsTranslation повертає true, якщо текст треба перекласти (англійська або підозріла кирилиця)
func needsTranslation(s string) bool {
	lang := utils.DetectTextLanguage(s)
	return lang == utils.LanguageUnknown || lang == utils.LanguageEnglish || lang == utils.LanguageRussian
}

func (a *App) processTranslationQueue(ctx context.Context, filenames []string, aiClient *ai.Client, expectedTMDBIDs map[string]int) {
	logger := utils.LoggerWithTrace(ctx)
	logger.Debug("localization_started", slog.Int("requested", len(filenames)))
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
			logger.Debug("localization_skipped", slog.String("file", fname), slog.String("reason", "record_missing"))
			continue
		}

		needTitle := movie.TitleUA == "" || needsTranslation(movie.TitleUA)
		needPlot := movie.Plot == "" || needsTranslation(movie.Plot)
		logger.Debug("localization_prepared", slog.String("file", fname), slog.Int("tmdb_id", movie.TmdbID),
			slog.Bool("need_title", needTitle), slog.Bool("need_plot", needPlot))

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

	// Diagnostic: серед файлів поточної черги рахуємо скільки потребують перекладу,
	// але не потрапили до payload (наприклад, записи відсутні у БД або вже оброблені).
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
	updatedCount := 0

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
			logger.Debug("localization_batch_failed", slog.Int("batch", currentBatchIdx), slog.Bool("cancelled", ctx.Err() != nil))
			a.logFront(fmt.Sprintf("⚠️ Помилка перекладу пачки %d: %v", currentBatchIdx, err))
			continue
		}
		logger.Debug("localization_batch_received", slog.Int("batch", currentBatchIdx), slog.Int("requested", len(batch)), slog.Int("returned", len(results)))
		returned := make(map[string]bool, len(results))
		for _, res := range results {
			if ctx.Err() != nil {
				return
			}
			returned[res.Filename] = true
		}
		for _, item := range batch {
			if ctx.Err() != nil {
				return
			}
			if !returned[item.Filename] {
				logger.Debug("localization_result_missing", slog.String("file", item.Filename))
			}
		}

		var localizationUpdates []localizationUpdate
		var completedLocalizations []storage.Movie
		currentMovies := movies
		if len(expectedTMDBIDs) > 0 {
			currentMovies, err = a.db.GetMoviesByFilenames(ctx, filenames)
			if err != nil {
				slog.Error("translation_guard_fetch_failed", slog.Any("error", err))
				continue
			}
		}
		for _, res := range results {
			if ctx.Err() != nil {
				return
			}
			movie, ok := movieMap[res.Filename]
			if !ok {
				continue
			}
			if expectedID, guarded := expectedTMDBIDs[res.Filename]; guarded {
				expectedMediaType := movie.MediaType
				current, currentTarget := translationTargetCurrent(currentMovies, res.Filename, expectedID, expectedMediaType)
				if !currentTarget {
					utils.LoggerWithTrace(ctx).Info("candidate_translation_stale_skipped", slog.String("file", res.Filename), slog.Int("expected_tmdb_id", expectedID))
					continue
				}
				movie = current
			}

			update, titleOutcome := prepareLocalizationUpdate(movie, res)
			if localizationCompleted(movie, res, update) {
				completedLocalizations = append(completedLocalizations, update.Movie)
			}
			changed := update.TitleChanged || update.PlotChanged
			if titleOutcome == "non_ukrainian_rejected" {
				logger.Warn("translation_title_not_ukrainian", slog.String("file", res.Filename))
			}
			if res.Plot != "" && needsTranslation(res.Plot) {
				utils.LoggerWithTrace(ctx).Warn("translation_plot_not_ukrainian", slog.String("file", res.Filename))
			}

			if changed {
				localizationUpdates = append(localizationUpdates, update)
			} else {
				a.logFront(localizationUnchangedMessage(movie, titleOutcome))
			}
			logger.Debug("localization_result", slog.String("file", movie.Filename), slog.Int("tmdb_id", movie.TmdbID),
				slog.String("title_outcome", titleOutcome), slog.Bool("changed", changed),
				slog.Bool("returned_plot", res.Plot != ""), slog.Bool("returned_plot_ukrainian", res.Plot != "" && !needsTranslation(res.Plot)))
		}

		if len(localizationUpdates) > 0 {
			committed, err := a.saveLocalizationUpdates(ctx, localizationUpdates)
			if err != nil {
				slog.Error("translation_batch_save_failed", slog.Any("error", err))
				a.logFront("⚠️ Локалізацію не збережено через помилку запису.")
				continue
			} else {
				updatedCount += committed
			}
		}
		for _, movie := range completedLocalizations {
			if err := a.db.MarkLocalizationCompleted(ctx, movie); err != nil {
				logger.Warn("localization_completion_save_failed", slog.Bool("cancelled", ctx.Err() != nil))
			}
		}
	}

	if ctx.Err() == nil {
		logger.Debug("localization_completed", slog.Int("updated_records", updatedCount))
		a.logFront(fmt.Sprintf("✅ Фаза перекладу завершена! Оновлено записів: %d", updatedCount))
	}
}

func translationTargetCurrent(movies map[string]storage.Movie, filename string, expectedTMDBID int, expectedMediaType string) (storage.Movie, bool) {
	movie, exists := movies[filename]
	return movie, exists && movie.TmdbID == expectedTMDBID && movie.MediaType == expectedMediaType
}

func (a *App) currentTranslationTarget(ctx context.Context, filename string, expectedTMDBID int, expectedMediaType string) (storage.Movie, bool) {
	movies, err := a.db.GetMoviesByFilenames(ctx, []string{filename})
	if err != nil {
		utils.LoggerWithTrace(ctx).Warn("candidate_background_guard_failed", slog.String("file", filename), slog.Any("error", err))
		return storage.Movie{}, false
	}
	return translationTargetCurrent(movies, filename, expectedTMDBID, expectedMediaType)
}
