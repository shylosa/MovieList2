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
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	discoveredAIModels         []string
	aiModelsHTTPClient         *http.Client
	modelsMutex                sync.RWMutex
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
}

type scanResult struct {
	path         string
	fname        string
	info         *tmdb.MovieInfo
	needsGemini  bool
	needsReview  bool
	reviewReason string
}

type AIModelCatalog struct {
	Current   []string `json:"current"`
	Available []string `json:"available"`
}

// aiConfidenceThreshold — єдиний поріг для Gemini, L2-кешу та merge.
const aiConfidenceThreshold = 0.55
const recognitionPipelineVersion = 25

// geminiTMDBVerifyMinJW — мінімальна схожість EN-назви Gemini і TMDB після верифікації.
const geminiTMDBVerifyMinJW = 0.85

var (
	groupedStrongEpisodeRE = regexp.MustCompile(`(?i)(?:^|[. _-])(?:s\d{1,2}e\d{1,3}|(?:season|episode|сезон|серія)\s*\d+)(?:$|[. _-])`)
	groupedWeakEpisodeRE   = regexp.MustCompile(`(?:^|[. _-])(\d{1,3})(?:[. _-]|$)`)
	groupedSpaceRE         = regexp.MustCompile(`\s+`)
)

const groupedTVReviewReason = "grouped_tv_type_conflict"

type groupedTVDecision struct {
	useParentTitle bool
	reason         string
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
	if err := a.auditDuplicateMovieIdentities(ctx); err != nil {
		slog.Warn("duplicate_identity_audit_failed", slog.Any("error", err))
	}
}

func (a *App) shutdown(ctx context.Context) {
	slog.Info("app_closed")
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
	for i := range movies {
		movies[i].FileLabel = utils.DisplayFileLabel(movies[i].Filename)
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

	return a.deployToGitHubPagesIn(workDir)
}

func (a *App) deployToGitHubPagesIn(workDir string) error {
	run := func(args ...string) error {
		out, err := a.gitRunner(a.ctx, workDir, args[0], args[1:]...)
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

// GetAIModelCatalog refreshes Gemini's model list and returns configured models
// separately from every compatible model reported by the API.
func (a *App) GetAIModelCatalog() (AIModelCatalog, error) {
	a.modelsMutex.Lock()
	a.aiModelsCache = nil
	a.discoveredAIModels = nil
	a.modelsMutex.Unlock()
	current, err := a.fetchAIModels(a.ctx)
	a.modelsMutex.RLock()
	available := append([]string(nil), a.discoveredAIModels...)
	a.modelsMutex.RUnlock()
	return AIModelCatalog{Current: current, Available: available}, err
}

func (a *App) SetAIModels(names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("select at least one Gemini model")
	}
	catalog, err := a.GetAIModelCatalog()
	if err != nil {
		return err
	}
	available := make(map[string]bool, len(catalog.Available))
	for _, name := range catalog.Available {
		available[name] = true
	}
	selected := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if !available[name] || seen[name] {
			continue
		}
		seen[name] = true
		selected = append(selected, name)
	}
	if len(selected) == 0 {
		return fmt.Errorf("selected models are not available for generateContent")
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return err
	}
	if err := a.db.SetState(a.ctx, "selected_gemini_models", string(encoded)); err != nil {
		return err
	}
	if a.aiClient != nil {
		a.aiClient.SetModels(selected)
	}
	a.modelsMutex.Lock()
	a.aiModelsCache = append([]string(nil), selected...)
	a.modelsMutex.Unlock()
	slog.Info("ai_models_selection_updated", slog.Int("selected", len(selected)))
	return nil
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
		client := a.aiModelsHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Second}
		}
		genClient, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey: a.cfg.GeminiAPIKey, Backend: genai.BackendGeminiAPI, HTTPClient: client,
		})
		if err != nil {
			return nil, err
		}
		discovered := make(map[string]bool)
		for model, listErr := range genClient.Models.All(ctx) {
			if listErr != nil {
				return nil, listErr
			}
			name := strings.TrimPrefix(model.Name, "models/")
			if isUsableGeminiModel(name, model.SupportedActions) {
				discovered[name] = true
			}
		}
		discoveredNames := make([]string, 0, len(discovered))
		for name := range discovered {
			discoveredNames = append(discoveredNames, name)
		}
		sort.Strings(discoveredNames)
		names := selectConfiguredGeminiModels(a.configuredGeminiModels(), discovered)
		if len(names) == 0 {
			return nil, fmt.Errorf("no configured Gemini generateContent models are available")
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
		a.discoveredAIModels = append([]string(nil), discoveredNames...)
		a.modelsMutex.Unlock()

		return append([]string(nil), names...), nil
	})
	if err != nil {
		configured := a.configuredGeminiModels()
		fallback := make([]string, 0, len(configured))
		for _, name := range configured {
			if isUsableGeminiModel(name, []string{"generateContent"}) {
				fallback = append(fallback, name)
			}
		}
		if len(fallback) > 0 {
			slog.Warn("ai_models_discovery_fallback", slog.Any("error", err), slog.Any("models", fallback))
			if a.aiClient != nil {
				a.aiClient.SetModels(fallback)
			}
			return fallback, nil
		}
		return nil, err
	}

	names, ok := value.([]string)
	if !ok {
		slog.Error("fetch_ai_models_type_assertion_failed", slog.String("type", fmt.Sprintf("%T", value)))
		return nil, fmt.Errorf("fetchAIModels: unexpected type %T", value)
	}
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
	ctx, cancel := context.WithCancel(a.ctx)
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
				slog.Int64("tmdb_search_calls", tmdbMetrics.SearchCalls),
				slog.Int64("tmdb_details_calls", tmdbMetrics.DetailsCalls),
				slog.Int64("tmdb_cache_hits", tmdbMetrics.CacheHits),
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

		// 🟢 Очищуємо кеш від попереднього сканування
		if a.tmdbClient != nil {
			a.tmdbClient.ClearCaches()
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
			a.cleanOrphanPostersAfterSuccess(scanCtx)
			a.finalizeScan("Змін не знайдено.", true)
			scanFinished = true
			return
		}

		a.logFront(fmt.Sprintf("📂 Файлів для обробки: %d", len(filesToProcess)))

		resultsChan := a.runTMDBScan(scanCtx, filesToProcess)

		// 🛡️ Єдиний Writer для SQLite: збирає батч і пише транзакцією
		moviesToSave, geminiQueue, translationQueue := a.processScanResults(scanCtx, resultsChan)
		tmdbAccepted = len(moviesToSave)

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
			aiAccepted = len(recognizedByGemini)
			unresolved = len(geminiQueue) - aiAccepted
			translationQueue = append(translationQueue, recognizedByGemini...)
		}

		if len(translationQueue) > 0 {
			a.processTranslationQueue(scanCtx, translationQueue, a.aiClient, nil)
		}
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

func (a *App) runTMDBScan(ctx context.Context, paths []string) <-chan scanResult {
	totalFiles := len(paths)
	resultsChan := make(chan scanResult, len(paths))
	sem := make(chan struct{}, 10) // Ліміт у 10 одночасних запитів до TMDB (rate-limit safe)
	var wg sync.WaitGroup
	var processedCount int32
	groupTV := detectGroupedTV(ctx, paths)

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

			parsed := tmdb.ParseFilename(path)
			originalMediaType := parsed.MediaType
			groupDecision, grouped := groupTV[path]
			if grouped {
				parsed.MediaType = tmdb.MediaTypeTV
				if parent := filepath.Base(filepath.Dir(path)); groupDecision.useParentTitle && isMeaningfulSeriesParent(parent) {
					parentParsed := tmdb.ParseFilename(parent)
					if parentParsed.CleanTitle != "" {
						parsed.CleanTitle = parentParsed.CleanTitle
					}
				}
				logger.Info("grouped_tv_applied", slog.String("reason", groupDecision.reason), slog.String("title", parsed.CleanTitle))
			}
			info, err := a.tmdbClient.FetchFromParsed(fileCtx, parsed, path)
			if err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return
				}
				logger.Warn("tmdb_search_error",
					slog.String("file", fname),
					slog.Any("error", err),
				)
				a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", fname, err))
			}

			result := scanResult{path: path, fname: fname, info: info, needsGemini: true}
			if grouped && originalMediaType == tmdb.MediaTypeMovie {
				result.needsReview = true
				result.reviewReason = groupedTVReviewReason
			}
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

func detectGroupedTV(ctx context.Context, paths []string) map[string]groupedTVDecision {
	type group struct {
		episodes map[int][]string
	}
	groups := make(map[string]*group)
	result := make(map[string]groupedTVDecision)
	for _, path := range paths {
		if ctx.Err() != nil {
			return map[string]groupedTVDecision{}
		}
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if groupedStrongEpisodeRE.MatchString(base) {
			result[path] = groupedTVDecision{useParentTitle: true, reason: "strong_episode_marker"}
			continue
		}
		matches := groupedWeakEpisodeRE.FindAllStringSubmatchIndex(base, -1)
		for _, match := range matches {
			if len(match) < 4 || match[2] < 0 {
				continue
			}
			n, _ := strconv.Atoi(base[match[2]:match[3]])
			if !isPlausibleWeakEpisode(base, match[2], match[3], n) {
				continue
			}
			signature := weakEpisodeSignature(base, match[2], match[3])
			key := filepath.Clean(filepath.Dir(path)) + "\x00" + signature
			g := groups[key]
			if g == nil {
				g = &group{episodes: make(map[int][]string)}
				groups[key] = g
			}
			g.episodes[n] = append(g.episodes[n], path)
			break
		}
	}
	for _, g := range groups {
		for n, currentPaths := range g.episodes {
			nextPaths, adjacent := g.episodes[n+1]
			if !adjacent {
				continue
			}
			for _, path := range append(currentPaths, nextPaths...) {
				result[path] = groupedTVDecision{useParentTitle: true, reason: "adjacent_episode_numbers"}
			}
		}
	}
	return result
}

func isPlausibleWeakEpisode(base string, start, end, number int) bool {
	if number <= 0 || number >= 1000 || number == 264 || number == 265 || number == 720 {
		return false
	}
	lower := strings.ToLower(base)
	prefix := strings.TrimRight(lower[:start], ". _-")
	lastToken := prefix
	if i := strings.LastIndexAny(prefix, ". _-"); i >= 0 {
		lastToken = prefix[i+1:]
	}
	if lastToken == "part" || lastToken == "pt" || lastToken == "частина" || lastToken == "часть" || lastToken == "x" || lastToken == "h" {
		return false
	}
	// Decimal audio layouts such as 5.1 and 7.1 are technical metadata. Do not
	// reject an episode followed by a multi-digit year or resolution token.
	if number < 10 && start > 0 && end+1 < len(base) && base[start-1] == '.' && base[end] == '.' && base[end+1] >= '0' && base[end+1] <= '9' {
		nextEnd := end + 1
		for nextEnd < len(base) && base[nextEnd] >= '0' && base[nextEnd] <= '9' {
			nextEnd++
		}
		if nextEnd == end+2 {
			return false
		}
	}
	return true
}

func weakEpisodeSignature(base string, start, end int) string {
	signature := strings.ToLower(base[:start] + " " + base[end:])
	signature = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(signature)
	return strings.TrimSpace(groupedSpaceRE.ReplaceAllString(signature, " "))
}

func isMeaningfulSeriesParent(parent string) bool {
	parent = strings.ToLower(strings.TrimSpace(parent))
	switch parent {
	case "", ".", "films", "film", "movies", "movie", "video", "videos", "media", "фільми", "фільм", "фильмы", "фильм", "відео", "видео":
		return false
	default:
		return true
	}
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
			if res.needsReview {
				movie.NeedsReview = true
				movie.ReviewReason = res.reviewReason
			}
			toSave = append(toSave, movie)
			a.logFront(fmt.Sprintf("✅ TMDB: '%s' → '%s'", res.fname, res.info.TitleUA))
			if a.movieInfoNeedsTranslation(res.info) {
				translationQueue = append(translationQueue, res.fname)
			}
		} else {
			// 🟢 ПЕРЕВІРКА L2 КЕШУ: Чи не розпізнавали ми цей файл раніше через ШІ?
			cached, stale, err := a.db.GetAIResolution(ctx, res.fname, recognitionPipelineVersion)
			if err != nil {
				utils.LoggerWithTrace(ctx).Warn("get_ai_resolution_failed", slog.String("file", res.fname), slog.Any("error", err))
			}
			if stale {
				utils.LoggerWithTrace(ctx).Info("ai_cache_stale", slog.String("file", res.fname), slog.Int("pipeline_version", recognitionPipelineVersion))
			}
			if cached != nil && cached.Confidence >= aiConfidenceThreshold {
				utils.LoggerWithTrace(ctx).Info("ai_cache_hit", slog.String("file", res.fname), slog.String("resolved", cached.ResolvedTitle), slog.Int("pipeline_version", cached.PipelineVersion))

				// Використовуємо кешовану назву для пошуку в TMDB
				info, err := a.tmdbClient.FetchByCleanTitle(ctx, cached.ResolvedTitle, strconv.Itoa(cached.Year), tmdb.MediaType(cached.MediaType))
				if err == nil && info != nil && info.TMDBID > 0 {
					jw := tmdb.TitleSimilarity(cached.ResolvedTitle, info.TitleEN)
					if info.MatchedAlias != "" {
						if jwAlias := tmdb.TitleSimilarity(cached.ResolvedTitle, info.MatchedAlias); jwAlias > jw {
							jw = jwAlias
						}
					}
					cachedYear := cached.Year
					if jw >= geminiTMDBVerifyMinJW && geminiTMDBYearCompatible(0, &cachedYear, info.Year) {
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
		Filename:     fname,
		TitleEN:      "Unresolved: " + fname,
		TitleUA:      fname,
		Year:         yearStr,
		TmdbID:       0,
		NeedsReview:  true,
		ReviewReason: "unresolved",
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

			if !ok {
				utils.LoggerWithTrace(ctx).Warn("gemini_recognition_missing",
					slog.String("file", fname),
					slog.Int("batch", currentBatchIdx),
					slog.Int("item_id", j),
				)
				a.logFront(fmt.Sprintf("⚠️ Gemini не розпізнав: '%s'", fname))
				a.emitProgress(processed, total, "❓ Не розпізнано: "+fname)

				// Create a placeholder movie record instead of silently skipping.
				a.appendUnresolvedFromMap(ctx, &moviesToSave, path, existingMovies)
				continue
			}

			yearVal := 0
			if rec.Year != nil {
				yearVal = *rec.Year
			}
			utils.LoggerWithTrace(ctx).Info("gemini_recognition_result",
				slog.String("file", fname),
				slog.String("en_title", rec.ENTitle),
				slog.String("media_type", rec.MediaType),
				slog.Int("year", yearVal),
				slog.Float64("confidence", rec.Confidence),
			)

			if rec.ENTitle == "" {
				utils.LoggerWithTrace(ctx).Warn("gemini_recognition_empty_title",
					slog.String("file", fname),
					slog.String("media_type", rec.MediaType),
					slog.Int("year", yearVal),
					slog.Float64("confidence", rec.Confidence),
				)
				a.logFront(fmt.Sprintf("⚠️ Gemini не розпізнав: '%s'", fname))
				a.emitProgress(processed, total, "❓ Не розпізнано: "+fname)

				movie := a.rescueEmptyGeminiWithFolder(ctx, path)
				if movie.TmdbID > 0 {
					moviesToSave = append(moviesToSave, movie)
					recognizedFiles = append(recognizedFiles, fname)
					continue
				}

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
				if err := a.db.SaveAIResolution(ctx, storage.AIResolution{
					OriginalFilename: fname,
					ResolvedTitle:    rec.ENTitle,
					Year:             yearVal,
					MediaType:        rec.MediaType,
					Confidence:       rec.Confidence,
					PipelineVersion:  recognitionPipelineVersion,
					Provider:         rec.Provider,
					Model:            rec.Model,
				}); err != nil {
					utils.LoggerWithTrace(ctx).Warn("save_ai_resolution_failed",
						slog.String("file", fname), slog.Any("error", err))
				} else {
					utils.LoggerWithTrace(ctx).Info("ai_cache_updated", slog.String("file", fname), slog.Int("pipeline_version", recognitionPipelineVersion))
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
	logger := utils.LoggerWithTrace(ctx).With(
		slog.String("file", fname),
		slog.String("en_title", rec.ENTitle),
		slog.String("requested_media_type", rec.MediaType),
		slog.Float64("confidence", rec.Confidence),
	)

	// 🛡️ КРОК 1: ПЕРЕВІРКА ВАЛІДНОСТІ ВІДПОВІДІ ШІ
	if rec.ENTitle == "" || (rec.Status != "" && rec.Status != "resolved") {
		logger.Warn("gemini_merge_skipped_empty_title")
		a.logFront(fmt.Sprintf("⚠️ [GEMINI] Відсутня EN назва для '%s'. Пропускаємо пошук.", fname))
		return storage.Movie{Filename: fname}
	}

	if rec.Confidence < aiConfidenceThreshold {
		logger.Warn("gemini_merge_skipped_low_confidence")
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

	logger = logger.With(
		slog.String("preferred_media_type", string(mt)),
		slog.String("year", yearStr),
	)
	logger.Info("gemini_merge_started")

	if ctx.Err() != nil {
		logger.Debug("gemini_merge_cancelled_before_tmdb", slog.Any("error", ctx.Err()))
		return storage.Movie{Filename: fname}
	}

	targetYear := 0
	if rec.Year != nil {
		targetYear = *rec.Year
	}
	tmdbInfo, err := a.tmdbClient.SearchExactTitle(ctx, rec.ENTitle, targetYear, mt, false, filePath)
	if err == nil && tmdbInfo != nil {
		logger.Info("gemini_merge_exact_candidate_selected",
			slog.Int("tmdb_id", tmdbInfo.TMDBID),
			slog.String("selected_media_type", string(tmdbInfo.MediaType)),
			slog.String("tmdb_title", tmdbInfo.TitleEN),
			slog.String("matched_alias", tmdbInfo.MatchedAlias),
		)
	}
	if tmdbInfo == nil && err == nil {
		tmdbInfo, err = a.tmdbClient.FetchByCleanTitle(ctx, rec.ENTitle, yearStr, mt)
	}
	if err != nil {
		logger.Warn("gemini_merge_tmdb_error", slog.Any("error", err))
		a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", rec.ENTitle, err))
	}

	if tmdbInfo == nil && err == nil && mt == tmdb.MediaTypeMovie && ctx.Err() == nil {
		logger.Info("gemini_merge_tv_retry")
		tmdbInfo, err = a.tmdbClient.FetchByCleanTitle(ctx, rec.ENTitle, yearStr, tmdb.MediaTypeTV)
		if err != nil {
			logger.Warn("gemini_merge_tv_retry_error", slog.Any("error", err))
			a.logFront(fmt.Sprintf("⚠️ TMDB помилка TV-пошуку для '%s': %v", rec.ENTitle, err))
		}
	}

	if tmdbInfo == nil {
		if ctx.Err() != nil {
			logger.Debug("gemini_merge_cancelled_after_tmdb", slog.Any("error", ctx.Err()))
		} else {
			logger.Warn("gemini_merge_tmdb_not_found")
		}
		a.logFront(fmt.Sprintf("❌ TMDB не знайшов '%s' — запис залишається нерозпізнаним", rec.ENTitle))
		return storage.Movie{Filename: fname}
	}

	strongJW := tmdb.TitleSimilarity(rec.ENTitle, tmdbInfo.TitleEN)
	if tmdbInfo.MatchedAlias != "" {
		jwAlias := tmdb.TitleSimilarity(rec.ENTitle, tmdbInfo.MatchedAlias)
		if jwAlias > strongJW {
			strongJW = jwAlias
		}
	}
	localizedJW := maxLocalizedTitleSimilarity(rec.ENTitle, tmdbInfo)
	yearCompatible := geminiTMDBYearCompatible(parsed.Year, rec.Year, tmdbInfo.Year)
	typeCompatible := tmdbInfo.MediaType == mt || strongJW >= 0.99

	if strongJW < geminiTMDBVerifyMinJW || !yearCompatible || !typeCompatible {
		logger.Warn("gemini_merge_post_verify_rejected",
			slog.String("tmdb_title", tmdbInfo.TitleEN),
			slog.String("search_title", tmdbInfo.SearchTitle),
			slog.String("matched_alias", tmdbInfo.MatchedAlias),
			slog.Float64("strong_similarity", strongJW),
			slog.Float64("localized_similarity", localizedJW),
			slog.Bool("year_compatible", yearCompatible),
			slog.Bool("media_type_compatible", typeCompatible),
		)
		a.logFront(fmt.Sprintf(
			"🛡️ [POST-VERIFY] Відхилено '%s': Gemini '%s' ≠ TMDB '%s' (Search: '%s', Alias: '%s', strong %.2f, localized %.2f)",
			fname, rec.ENTitle, tmdbInfo.TitleEN, tmdbInfo.SearchTitle, tmdbInfo.MatchedAlias, strongJW, localizedJW,
		))
		return storage.Movie{Filename: fname}
	}

	logger.Info("gemini_merge_tmdb_accepted",
		slog.Int("tmdb_id", tmdbInfo.TMDBID),
		slog.String("tmdb_title", tmdbInfo.TitleEN),
		slog.String("selected_media_type", string(tmdbInfo.MediaType)),
		slog.String("tmdb_year", tmdbInfo.Year),
	)
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
	movie.VoteAverage = tmdbInfo.VoteAverage
	movie.VoteCount = tmdbInfo.VoteCount
	movie.RecognitionSource = rec.Provider
	if movie.RecognitionSource == "" {
		movie.RecognitionSource = "gemini"
	}
	movie.RecognitionConfidence = rec.Confidence
	movie.VerificationScore = strongJW
	if tmdbInfo.NeedsReview {
		movie.NeedsReview = true
		movie.ReviewReason = tmdbInfo.ReviewReason
	}
	if tmdbInfo.AmbiguousExact || strongJW < tmdb.ReviewVerificationThreshold {
		movie.NeedsReview = true
		if tmdbInfo.AmbiguousExact {
			movie.ReviewReason = "ambiguous_exact"
		} else {
			movie.ReviewReason = "low_verification_score"
		}
		logger.Info("recognition_needs_review", slog.String("reason", movie.ReviewReason))
	}

	return movie
}

// rescueEmptyGeminiWithFolder tries a deterministic TMDB lookup when AI returned
// an empty title but the file sits in a meaningful release folder.
func (a *App) rescueEmptyGeminiWithFolder(ctx context.Context, filePath string) storage.Movie {
	fname := a.getFileIdentifier(filePath)
	logger := utils.LoggerWithTrace(ctx).With(slog.String("file", fname))

	if ctx.Err() != nil {
		logger.Debug("gemini_empty_rescue_cancelled_before_start", slog.Any("error", ctx.Err()))
		return storage.Movie{Filename: fname}
	}

	parsed := tmdb.ParseFilename(filePath)
	parentTitle := cleanRescueParentTitle(filePath, a.cfg.MediaFolderPath, parsed.ParentDir)
	if parentTitle == "" {
		logger.Warn("gemini_empty_rescue_skipped_no_parent_title")
		return storage.Movie{Filename: fname}
	}

	parentParsed := tmdb.ParseFilename(parentTitle)
	year := parsed.Year
	if year == 0 {
		year = parentParsed.Year
	}
	yearStr := ""
	if year > 0 {
		yearStr = strconv.Itoa(year)
	}

	candidates := buildGeminiEmptyRescueCandidates(parentParsed.CleanTitle)
	if len(candidates) == 0 {
		logger.Warn("gemini_empty_rescue_skipped_no_candidates", slog.String("parent", parentTitle))
		return storage.Movie{Filename: fname}
	}

	logger.Info("gemini_empty_rescue_started",
		slog.String("parent", parentTitle),
		slog.Int("candidate_count", len(candidates)),
		slog.String("year", yearStr),
	)

	for _, candidate := range candidates {
		if ctx.Err() != nil {
			logger.Debug("gemini_empty_rescue_cancelled", slog.Any("error", ctx.Err()))
			return storage.Movie{Filename: fname}
		}

		tmdbInfo, err := a.tmdbClient.FetchByCleanTitle(ctx, candidate, yearStr, tmdb.MediaTypeMovie)
		if err != nil {
			logger.Warn("gemini_empty_rescue_tmdb_error",
				slog.String("candidate", candidate),
				slog.Any("error", err),
			)
			continue
		}
		if tmdbInfo == nil && ctx.Err() == nil {
			logger.Info("gemini_empty_rescue_tv_retry", slog.String("candidate", candidate))
			tmdbInfo, err = a.tmdbClient.FetchByCleanTitle(ctx, candidate, yearStr, tmdb.MediaTypeTV)
			if err != nil {
				logger.Warn("gemini_empty_rescue_tv_retry_error",
					slog.String("candidate", candidate),
					slog.Any("error", err),
				)
				continue
			}
		}
		if tmdbInfo == nil {
			logger.Info("gemini_empty_rescue_candidate_miss", slog.String("candidate", candidate))
			continue
		}

		jw := maxTitleSimilarity(candidate, tmdbInfo)
		if jw < geminiTMDBVerifyMinJW {
			logger.Warn("gemini_empty_rescue_post_verify_rejected",
				slog.String("candidate", candidate),
				slog.String("tmdb_title", tmdbInfo.TitleEN),
				slog.String("search_title", tmdbInfo.SearchTitle),
				slog.String("matched_alias", tmdbInfo.MatchedAlias),
				slog.Float64("similarity", jw),
			)
			continue
		}

		logger.Info("gemini_empty_rescue_accepted",
			slog.String("candidate", candidate),
			slog.Int("tmdb_id", tmdbInfo.TMDBID),
			slog.String("tmdb_title", tmdbInfo.TitleEN),
			slog.String("tmdb_media_type", string(tmdbInfo.MediaType)),
			slog.String("tmdb_year", tmdbInfo.Year),
			slog.Float64("similarity", jw),
		)
		a.logFront(fmt.Sprintf("✅ [RESCUE] TMDB знайшов: '%s' (%s)", tmdbInfo.TitleEN, tmdbInfo.Year))
		return movieFromTMDB(fname, tmdbInfo)
	}

	logger.Warn("gemini_empty_rescue_failed", slog.String("parent", parentTitle))
	return storage.Movie{Filename: fname}
}

func cleanRescueParentTitle(filePath, mediaRoot, parentDir string) string {
	parent := strings.TrimSpace(parentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return ""
	}

	if mediaRoot != "" {
		fileAbs := filepath.Clean(filePath)
		rootAbs := filepath.Clean(mediaRoot)
		if !filepath.IsAbs(fileAbs) {
			fileAbs = filepath.Join(rootAbs, fileAbs)
		}
		parentPath := filepath.Clean(filepath.Dir(fileAbs))
		if rel, err := filepath.Rel(rootAbs, parentPath); err == nil && rel == "." {
			return ""
		}
	}

	switch strings.ToLower(parent) {
	case "movie", "movies", "film", "films", "serial", "serials", "series", "show", "shows",
		"video", "videos", "download", "downloads", "кино", "фильмы", "фільми", "серіали", "сериалы":
		return ""
	}

	return parent
}

func buildGeminiEmptyRescueCandidates(title string) []string {
	var candidates []string
	seen := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len([]rune(s)) < 3 {
			return
		}
		key := strings.ToLower(s)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, s)
	}

	add(title)
	latin := utils.CyrillicToLatin(title)
	add(latin)
	for _, variant := range borrowedLatinVariants(latin) {
		add(variant)
	}

	return candidates
}

func borrowedLatinVariants(s string) []string {
	if s == "" || utils.HasCyrillic(s) {
		return nil
	}

	var variants []string
	add := func(old, replacement string) {
		if strings.HasPrefix(s, old) {
			variants = append(variants, replacement+strings.TrimPrefix(s, old))
		}
	}

	add("Sk", "Sc")
	add("sk", "sc")
	add("SK", "SC")

	return variants
}

func maxTitleSimilarity(title string, info *tmdb.MovieInfo) float64 {
	jw := tmdb.TitleSimilarity(title, info.TitleEN)
	// TitleUA is the Cyrillic counterpart; JaroWinkler across alphabets is 0,
	// so a Cyrillic candidate needs this check to avoid false rejection.
	if info.TitleUA != "" {
		if jwUA := tmdb.TitleSimilarity(title, info.TitleUA); jwUA > jw {
			jw = jwUA
		}
	}
	if info.SearchTitle != "" {
		if jwSearch := tmdb.TitleSimilarity(title, info.SearchTitle); jwSearch > jw {
			jw = jwSearch
		}
	}
	if info.MatchedAlias != "" {
		if jwAlias := tmdb.TitleSimilarity(title, info.MatchedAlias); jwAlias > jw {
			jw = jwAlias
		}
	}
	return jw
}

func maxLocalizedTitleSimilarity(title string, info *tmdb.MovieInfo) float64 {
	if info == nil {
		return 0
	}
	jw := tmdb.TitleSimilarity(title, info.TitleUA)
	if searchJW := tmdb.TitleSimilarity(title, info.SearchTitle); searchJW > jw {
		jw = searchJW
	}
	return jw
}

func geminiTMDBYearCompatible(parsedYear int, recognizedYear *int, tmdbYear string) bool {
	foundYear, err := strconv.Atoi(tmdbYear)
	if err != nil || foundYear == 0 {
		return parsedYear == 0 && recognizedYear == nil
	}
	if parsedYear > 0 && absInt(parsedYear-foundYear) > 1 {
		return false
	}
	return recognizedYear == nil || *recognizedYear == 0 || absInt(*recognizedYear-foundYear) <= 1
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
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
	ctx := utils.EnsureTrace(a.ctx)
	if strings.TrimSpace(request.Filename) == "" {
		return nil, fmt.Errorf("filename required")
	}
	parsed := tmdb.ParseFilename(request.Filename)
	current, getErr := a.db.GetMovieByFilename(ctx, request.Filename)
	if getErr != nil {
		return nil, fmt.Errorf("candidate lookup current movie: %w", getErr)
	}
	resolvedTitle := ""
	if cached, stale, cacheErr := a.db.GetAIResolution(ctx, request.Filename, recognitionPipelineVersion); cacheErr != nil {
		return nil, fmt.Errorf("candidate lookup AI resolution: %w", cacheErr)
	} else if cached != nil && !stale {
		resolvedTitle = cached.ResolvedTitle
	}
	queries := candidateSearchQueries(request.Title, request.Filename, current, resolvedTitle)
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

func candidateSearchQueries(title, filename string, current *storage.Movie, resolvedTitle string) []candidateQuery {
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
		add("stored_title", resolvedTitle)
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
	ctx := utils.EnsureTrace(a.ctx)
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
	if err := a.db.DeleteAIResolution(ctx, request.Filename); err != nil {
		return err
	}
	needsTranslation := a.aiClient != nil && a.movieInfoNeedsTranslation(info)
	if info.PosterURL != "" || needsTranslation {
		filename, expectedID := request.Filename, request.TMDBID
		posterURL := info.PosterURL
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			backgroundCtx := utils.EnsureTrace(a.ctx)
			translationStartedAt := time.Now()
			utils.LoggerWithTrace(backgroundCtx).Info("candidate_background_started", slog.String("file", filename), slog.Int("tmdb_id", expectedID))
			if posterURL != "" {
				posterStartedAt := time.Now()
				localPath, posterErr := a.tmdbClient.DownloadPoster(backgroundCtx, posterURL, fmt.Sprintf("%d_%s", expectedID, filename))
				if posterErr != nil {
					utils.LoggerWithTrace(backgroundCtx).Warn("candidate_poster_failed", slog.String("file", filename), slog.Any("error", posterErr))
				} else if current, ok := a.currentTranslationTarget(backgroundCtx, filename, expectedID); ok {
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
	ctx := utils.EnsureTrace(a.ctx)
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

	a.emitEvent(a.ctx, "scan-started")
	if a.tmdbClient != nil {
		a.tmdbClient.ClearCaches()
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
		if (s.Hint != "" && s.Hint != "skip") || (existing != nil && existing.TmdbID > 0) {
			withHint = append(withHint, s)
		} else {
			geminiQueue = append(geminiQueue, filepath.Join(a.cfg.MediaFolderPath, s.Filename))
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
			a.logFront(fmt.Sprintf("🎯 [TMDB Істина] Пропуск черги локалізації для '%s' (офіційний переклад та опис вже є)", m.TitleUA))
			continue
		}
		translationQueue = append(translationQueue, fix.Filename)
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

	utils.LoggerWithTrace(ctx).Info("fix_selected_completed",
		slog.Int("requested", total),
		slog.Int("resolved", succeeded),
		slog.Int("unresolved", failed),
	)
	a.finalizeScan(fmt.Sprintf("Успішно виправлено: %d; не виправлено: %d", succeeded, failed), true)
}

// UpdateMovie — Wails API: оновлення одного запису за hint від користувача.
func (a *App) UpdateMovie(filename, hint string) error {
	ctx := utils.EnsureTrace(a.ctx)
	return a.updateMovie(ctx, filename, hint)
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
		_ = a.db.DeleteAIResolution(ctx, filename)
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
				return a.db.DeleteAIResolution(ctx, filename)
			}
			applyTMDBToMovie(existing, info)

			if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing}); err != nil {
				return err
			}
			return a.db.DeleteAIResolution(ctx, filename)
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
		_ = a.db.DeleteAIResolution(ctx, filename)
		logger.Info("manual_title_resolved", slog.Int("tmdb_id", info.TMDBID), slog.String("media_type", string(info.MediaType)), slog.String("title", info.TitleEN))
		return nil
	}

	// Варіант 2: порожня підказка → Gemini → TMDB
	a.logFront(fmt.Sprintf("🧠 [%s] Аналіз через Gemini...", filename))

	geminiCtx := ai.FileRecognitionContextFromPath(filename)
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
	rec = preserveRecognizedContext(rec, existing)
	movie := a.mergeGeminiWithTMDB(ctx, filepath.Join(a.cfg.MediaFolderPath, filename), rec)
	if identityReplacementConflicts(existing, movie) {
		utils.LoggerWithTrace(ctx).Warn("identity_replacement_rejected",
			slog.String("file", filename), slog.Int("old_tmdb_id", existing.TmdbID), slog.String("old_media_type", existing.MediaType), slog.String("old_year", existing.Year),
			slog.Int("new_tmdb_id", movie.TmdbID), slog.String("new_media_type", movie.MediaType), slog.String("new_year", movie.Year))
		existing.NeedsReview = true
		existing.ReviewReason = "identity_conflict"
		return a.db.SaveMoviesBatch(ctx, []storage.Movie{*existing})
	}

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
			PipelineVersion:  recognitionPipelineVersion,
			Provider:         rec.Provider,
			Model:            rec.Model,
		}); err != nil {
			slog.Warn("save_ai_resolution_failed",
				slog.String("file", filename), slog.Any("error", err))
		} else {
			utils.LoggerWithTrace(ctx).Info("ai_cache_updated", slog.String("file", filename), slog.Int("pipeline_version", recognitionPipelineVersion))
		}
	}

	// Зберігаємо існуючі поля якщо нові порожні
	if movie.TitleUA == "" {
		movie.TitleUA = existing.TitleUA
	}
	if movie.Plot == "" {
		movie.Plot = existing.Plot
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
	movie := storage.Movie{
		Filename:              fname,
		TmdbID:                info.TMDBID,
		TitleUA:               info.TitleUA,
		TitleEN:               info.TitleEN,
		Year:                  info.Year,
		Plot:                  info.Plot,
		Genres:                info.Genres,
		Cast:                  info.Cast,
		PosterURL:             info.PosterURL,
		LocalPosterPath:       info.LocalPosterPath,
		MediaType:             string(info.MediaType),
		RecognitionSource:     "tmdb",
		RecognitionConfidence: 1,
		VerificationScore:     info.VerificationScore,
		VoteAverage:           info.VoteAverage,
		VoteCount:             info.VoteCount,
	}
	if movie.VerificationScore == 0 {
		movie.VerificationScore = 1
	}
	if info.NeedsReview {
		movie.NeedsReview = true
		movie.ReviewReason = info.ReviewReason
	}
	if info.AmbiguousExact {
		movie.NeedsReview = true
		movie.ReviewReason = "ambiguous_exact"
	}
	return movie
}

// applyTMDBToMovie — перезаписує поля movie з tmdbInfo (для ручного виправлення)
func applyTMDBToMovie(movie *storage.Movie, info *tmdb.MovieInfo) {
	movie.TmdbID = info.TMDBID
	movie.VoteAverage = info.VoteAverage
	movie.VoteCount = info.VoteCount
	movie.NeedsReview = false
	movie.ReviewReason = ""
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
	lang := utils.DetectTextLanguage(s)
	return lang == utils.LanguageUnknown || lang == utils.LanguageEnglish || lang == utils.LanguageRussian
}

func (a *App) processTranslationQueue(ctx context.Context, filenames []string, aiClient *ai.Client, expectedTMDBIDs map[string]int) {
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
		currentMovies := movies
		if len(expectedTMDBIDs) > 0 {
			currentMovies, err = a.db.GetMoviesByFilenames(ctx, filenames)
			if err != nil {
				slog.Error("translation_guard_fetch_failed", slog.Any("error", err))
				continue
			}
		}
		for _, res := range results {
			movie, ok := movieMap[res.Filename]
			if !ok {
				continue
			}
			if expectedID, guarded := expectedTMDBIDs[res.Filename]; guarded {
				current, currentTarget := translationTargetCurrent(currentMovies, res.Filename, expectedID)
				if !currentTarget {
					utils.LoggerWithTrace(ctx).Info("candidate_translation_stale_skipped", slog.String("file", res.Filename), slog.Int("expected_tmdb_id", expectedID))
					continue
				}
				movie = current
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

func translationTargetCurrent(movies map[string]storage.Movie, filename string, expectedTMDBID int) (storage.Movie, bool) {
	movie, exists := movies[filename]
	return movie, exists && movie.TmdbID == expectedTMDBID
}

func (a *App) currentTranslationTarget(ctx context.Context, filename string, expectedTMDBID int) (storage.Movie, bool) {
	movies, err := a.db.GetMoviesByFilenames(ctx, []string{filename})
	if err != nil {
		utils.LoggerWithTrace(ctx).Warn("candidate_background_guard_failed", slog.String("file", filename), slog.Any("error", err))
		return storage.Movie{}, false
	}
	return translationTargetCurrent(movies, filename, expectedTMDBID)
}
