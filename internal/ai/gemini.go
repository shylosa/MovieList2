package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"movielist-app/internal/config"
	"movielist-app/internal/tmdb"
	"movielist-app/internal/utils"

	"golang.org/x/time/rate"
	"google.golang.org/genai"
)

var geminiQuotaLocked atomic.Bool

func isQuotaExhaustedError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "resource_exhausted") ||
		strings.Contains(errStr, "quota exceeded") ||
		strings.Contains(errStr, "generate_content_free_tier")
}

// FileRecognitionContext — структурований контекст файлу для промпту Gemini.
type FileRecognitionContext struct {
	ID           int    `json:"id"`
	OriginalFile string `json:"original_file"`
	FilePath     string `json:"-"`
	CleanTitle   string `json:"parsed_title"`
	Year         int    `json:"parsed_year,omitempty"`
	MediaType    string `json:"parsed_media_type"`
	ParentDir    string `json:"parent_folder,omitempty"`
	IMDBID       string `json:"imdb_id,omitempty"`
}

// FileRecognitionContextFromPath будує контекст із повного шляху або basename.
func FileRecognitionContextFromPath(path string) FileRecognitionContext {
	parsed := tmdb.ParseFilename(path)
	year := 0
	if parsed.Year > 0 {
		year = parsed.Year
	}
	return FileRecognitionContext{
		OriginalFile: filepath.Base(path),
		FilePath:     path,
		CleanTitle:   parsed.CleanTitle,
		Year:         year,
		MediaType:    string(parsed.MediaType),
		ParentDir:    parsed.ParentDir,
		IMDBID:       parsed.IMDBID,
	}
}

// RecognizedTitle — відповідь Gemini для одного файлу.
//
// Стратегія мержу з TMDB (пріоритет завжди у TMDB):
//
//	TMDB поле непорожнє → беремо TMDB
//	TMDB поле порожнє   → беремо Gemini як fallback
//
// Виняток: en_title та year — тільки для пошуку в TMDB, не зберігаємо напряму.
type RecognizedTitle struct {
	ID           int     `json:"id"`
	OriginalFile string  `json:"original_file"` // ім'я файлу як є — для маппінгу
	ENTitle      string  `json:"en_title"`      // оригінальна англійська назва (для TMDB пошуку)
	Year         *int    `json:"year"`
	MediaType    string  `json:"media_type"` // "movie" або "tv"
	Confidence   float64 `json:"confidence"` // Оцінка впевненості 0.0-1.0, 0 якщо не вказано
}

type Client struct {
	cfg         *config.Config
	limiter     *rate.Limiter
	grokLimiter *rate.Limiter
	// 🟢 ДОДАНО: Динамічний каскад та м'ютекс для його захисту
	activeModels   []string
	modelsMu       sync.RWMutex
	genaiClient    *genai.Client
	initMu         sync.Mutex
	httpClient     *http.Client // 🔴 ДОДАНО ДЛЯ ТЕСТІВ: Дозволяє мокувати відповіді API
	grokHTTPClient *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		// rate.Every(4 * time.Second) = 15 RPM. Burst = 1.
		limiter: rate.NewLimiter(rate.Every(4*time.Second), 1),
		// Grok: rate.Every(2 * time.Second) = 30s per minute, burst=1 (conservative)
		grokLimiter:    rate.NewLimiter(rate.Every(2*time.Second), 1),
		grokHTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// SetModels дозволяє оновити список доступних моделей "на льоту"
func (c *Client) SetModels(models []string) {
	c.modelsMu.Lock()
	defer c.modelsMu.Unlock()
	// Копіюємо слайс, щоб уникнути data race
	c.activeModels = append([]string(nil), models...)
}

// getModels повертає актуальний список моделей для каскаду
func (c *Client) getModels() []string {
	c.modelsMu.RLock()
	defer c.modelsMu.RUnlock()

	var candidates []string
	// Пріоритет 1: Динамічний список від API
	if len(c.activeModels) > 0 {
		candidates = append([]string(nil), c.activeModels...)
	} else if len(c.cfg.GeminiModels) > 0 {
		// Пріоритет 2: Конфіг
		candidates = append([]string(nil), c.cfg.GeminiModels...)
	} else {
		// Пріоритет 3: Хардкод-фолбек
		candidates = []string{"gemini-2.0-flash", "gemini-2.0-pro", "gemini-1.5-flash"}
	}

	var filtered []string
	for _, m := range candidates {
		mLower := strings.ToLower(m)
		// Exclude embedding, audio, tts, robotics, computer-use
		if strings.Contains(mLower, "embedding") ||
			strings.Contains(mLower, "audio") ||
			strings.Contains(mLower, "tts") ||
			strings.Contains(mLower, "robotics") ||
			strings.Contains(mLower, "computer-use") {
			continue
		}
		// Include only flash, pro, and lite
		if strings.Contains(mLower, "flash") ||
			strings.Contains(mLower, "pro") ||
			strings.Contains(mLower, "lite") {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	return c.limiter.Wait(ctx)
}

// getGenaiClient ліниво ініціалізує singleton genai.Client.
func (c *Client) getGenaiClient(ctx context.Context) (*genai.Client, error) {
	c.initMu.Lock()
	defer c.initMu.Unlock()

	if c.genaiClient != nil {
		return c.genaiClient, nil
	}
	if c.cfg.GeminiAPIKey == "" {
		return nil, fmt.Errorf("gemini: API key not configured")
	}

	clientConfig := &genai.ClientConfig{
		APIKey:  c.cfg.GeminiAPIKey,
		Backend: genai.BackendGeminiAPI,
	}
	// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Якщо є тестовий клієнт — використовуємо його
	if c.httpClient != nil {
		clientConfig.HTTPClient = c.httpClient
	}

	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("помилка ініціалізації genai: %w", err)
	}

	c.genaiClient = client
	return c.genaiClient, nil
}

// Close залишено для уніфікованого lifecycle API клієнта.
// У поточній версії google.golang.org/genai Client не має методу Close().
func (c *Client) Close() {}

// RecognizeBulk — пакетне розпізнавання імен файлів через Gemini.
// Повертає дані для пошуку в TMDB + fallback-поля для мержу.
func (c *Client) RecognizeBulk(ctx context.Context, contexts []FileRecognitionContext) ([]RecognizedTitle, error) {
	if len(contexts) == 0 {
		return nil, nil
	}

	prompt, err := buildPrompt(contexts)
	if err != nil {
		return nil, fmt.Errorf("build_prompt: %w", err)
	}
	utils.LoggerWithTrace(ctx).Info("gemini_recognition_start", slog.Int("file_count", len(contexts)))

	return c.requestWithRetry(ctx, prompt)
}

func (c *Client) requestWithRetry(ctx context.Context, prompt string) ([]RecognizedTitle, error) {
	if geminiQuotaLocked.Load() {
		utils.LoggerWithTrace(ctx).Warn("gemini_quota_lock_skip")
		return nil, fmt.Errorf("gemini quota lock is active")
	}
	var lastErr error
	models := c.getModels() // 🟢: Беремо актуальний каскад

	// Йдемо по списку моделей (каскад)
	for i, modelName := range models {
		// Перевіряємо чи не скасовано контекст користувачем
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if i > 0 {
			utils.LoggerWithTrace(ctx).Info("gemini_cascade_switch",
				slog.String("model", modelName),
				slog.Int("cascade_index", i),
			)
		}

		// Робимо запит до поточної моделі
		result, err := c.makeRequest(ctx, prompt, modelName)
		if err == nil {
			// Успіх! Повертаємо результат, не чіпаємо інші моделі
			if i > 0 {
				utils.LoggerWithTrace(ctx).Info("gemini_backup_model_success", slog.String("model", modelName))
			}
			return result, nil
		}

		// Якщо помилка, записуємо її і йдемо на наступну ітерацію (до наступної моделі)
		lastErr = err
		utils.LoggerWithTrace(ctx).Warn("gemini_model_failed",
			slog.String("model", modelName),
			slog.Any("error", err),
		)
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Grok fallback — last resort after all Gemini models failed
	if c.cfg.GrokAPIKey != "" {
		utils.LoggerWithTrace(ctx).Info("grok_recognize_fallback")
		raw, grokErr := c.callGrok(ctx, prompt)
		if grokErr == nil {
			parsed, parseErr := parseRecognizeResponse(raw)
			if parseErr == nil {
				utils.LoggerWithTrace(ctx).Info("grok_recognize_success",
					slog.Int("results_count", len(parsed)),
				)
				return parsed, nil
			}
			lastErr = fmt.Errorf("grok parse error: %w", parseErr)
		} else {
			lastErr = grokErr
		}
	}
	return nil, fmt.Errorf("all AI models unavailable (incl. Grok): %w", lastErr)
}

func (c *Client) makeRequest(ctx context.Context, prompt, modelName string) ([]RecognizedTitle, error) {
	if err := c.waitForRateLimit(ctx); err != nil {
		return nil, err
	}

	client, err := c.getGenaiClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("помилка genai клієнта: %w", err)
	}

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.05),
		ResponseMIMEType: "application/json",
		ResponseSchema:   buildGenAISchema(),
	}

	resp, err := client.Models.GenerateContent(ctx, modelName, genai.Text(prompt), config)
	if err != nil {
		if isQuotaExhaustedError(err) {
			if geminiQuotaLocked.CompareAndSwap(false, true) {
				utils.LoggerWithTrace(ctx).Warn("gemini_quota_lock_enabled", slog.Any("error", err))
			}
		}
		return nil, fmt.Errorf("помилка API Gemini: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("модель повернула порожню відповідь")
	}

	return parseRecognizeResponse(resp.Text())
}

func parseRecognizeResponse(raw string) ([]RecognizedTitle, error) {
	cleaned := cleanJSON(raw)
	var results []RecognizedTitle
	if err := json.Unmarshal([]byte(cleaned), &results); err != nil {
		return nil, fmt.Errorf("неможливо розпарсити JSON від моделі: %w", err)
	}
	return results, nil
}

// cleanJSON attempts to extract the JSON payload from model responses that may
// include surrounding text, markdown fences, or other noise. It is intentionally
// simple and uses string searches (not regex) to avoid allocating compiled
// patterns per-call. Returns the original string if no obvious JSON boundaries
// are found.
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}

	// Fast path: already looks like JSON array or object
	if s[0] == '[' || s[0] == '{' {
		return s
	}

	// Find first occurrences of array/object starts
	idxArr := strings.IndexByte(s, '[')
	idxObj := strings.IndexByte(s, '{')

	// Choose the earliest positive index (or the one that exists)
	start := -1
	isArray := false
	if idxArr >= 0 && (idxObj == -1 || idxArr < idxObj) {
		start = idxArr
		isArray = true
	} else if idxObj >= 0 {
		start = idxObj
		isArray = false
	}

	if start == -1 {
		return s
	}

	// Find last matching closing bracket for the chosen type
	if isArray {
		end := strings.LastIndexByte(s, ']')
		if end > start {
			return strings.TrimSpace(s[start : end+1])
		}
	} else {
		end := strings.LastIndexByte(s, '}')
		if end > start {
			return strings.TrimSpace(s[start : end+1])
		}
	}

	// Fallback: return original to allow the caller to surface a parse error
	return s
}

// buildGenAISchema — типізована схема для structured output у genai SDK.
func buildGenAISchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeArray,
		Items: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"id":            {Type: genai.TypeInteger, Description: "exact integer identifier from input"},
				"original_file": {Type: genai.TypeString, Description: "exact original filename as provided, unchanged"},
				"en_title":      {Type: genai.TypeString, Description: "original English title for TMDB search. Must be the international release title, not a translation."},
				"year":          {Type: genai.TypeInteger, Nullable: genai.Ptr(true), Description: "release year. null if uncertain."},
				"media_type":    {Type: genai.TypeString, Description: "\"movie\" or \"tv\". Use \"tv\" only for clear series markers."},
				"confidence":    {Type: genai.TypeNumber, Description: "Confidence score 0.0-1.0, 0 if not provided."},
			},
			Required: []string{"id", "original_file", "en_title", "media_type", "confidence"},
		},
	}
}

func buildBulkTranslateSchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeArray,
		Items: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"filename":       {Type: genai.TypeString},
				"title":          {Type: genai.TypeString},
				"original_title": {Type: genai.TypeString},
				"plot":           {Type: genai.TypeString},
			},
			Required: []string{"filename", "title", "plot"},
		},
	}
}

// buildPrompt — compact recognition prompt for Gemini (schema defines response fields).
func buildPrompt(contexts []FileRecognitionContext) (string, error) {
	filesJSON, err := json.Marshal(contexts)
	if err != nil {
		return "", fmt.Errorf("build_prompt_marshal: %w", err)
	}
	if len(filesJSON) == 0 || string(filesJSON) == "null" {
		return "", fmt.Errorf("build_prompt: empty contexts")
	}
	return fmt.Sprintf(`You are a movie database expert. Find the OFFICIAL ORIGINAL (English) title on TMDB for each file.
CRITICAL: DO NOT translate localized titles literally — find the actual global release.
Example: "Moj malenkij angel" ≠ "My Little Angel" → actual title is "Foster".

Input JSON (TRUST parsed_year and parsed_media_type — do not re-guess them):
%s
"original_file" in your response MUST exactly match "original_file" from input.

MERGE STRATEGY: "en_title" is used to search TMDB. TMDB data always wins.
Return "" if uncertain — a miss is better than a hallucination.

TRANSLITERATION EXAMPLES (Latin-script dub titles → original EN release):
- "Vrag" → "Enemy" (2013)
- "Banshi Inisherina" → "The Banshees of Inisherin"
- "Nochnoj Rejs" → "Red Eye" (2005)
- "Ubiystvennyiy podkast" → find actual EN title, do not guess

CYRILLIC / LOCALIZED EXAMPLES (completely different from literal meaning):
- "Убийца 2. Против всех" → "Sicario: Day of the Soldado"
- "Иллюзия обмана 3" → "Now You See Me 3"
- "Отпуск на двоих" (2026) → "People We Meet on Vacation"
- "Список подозреваемых" (2024) → "Boneyard"

RULES:
1. en_title: exact TMDB-searchable title. "" if not 100%% certain — do NOT guess.
2. year: use parsed_year from input; if absent extract from filename; null if uncertain.
3. media_type: use parsed_media_type from input; "tv" only for S01/Season markers; default "movie".

Return ONLY a raw JSON array. No markdown, no explanation.`, string(filesJSON)), nil
}

type BulkTranslateItem struct {
	Filename      string `json:"filename"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title,omitempty"` // 🟢 НОВЕ: для контексту при перекладі
	Plot          string `json:"plot"`
}

// TranslateBulk виконує пакетний переклад назв та описів за один HTTP-запит.
func (c *Client) TranslateBulk(ctx context.Context, items []BulkTranslateItem) ([]BulkTranslateItem, error) {
	if len(items) == 0 {
		return nil, nil
	}

	// If Gemini quota is locked but Grok is configured, fall back to Grok.
	if geminiQuotaLocked.Load() {
		utils.LoggerWithTrace(ctx).Warn("gemini_quota_lock_skip")
		if c.cfg.GrokAPIKey == "" {
			return nil, fmt.Errorf("gemini quota lock is active")
		}
	}

	// safe to ignore: BulkTranslateItem contains only JSON-marshalable primitive fields.
	inputJSON, _ := json.Marshal(items)

	prompt := fmt.Sprintf(`Localize each item to official Ukrainian title (and plot when provided). Keep "filename" unchanged. Use original_title (when provided) as context to find the correct official Ukrainian title. If no official UA title exists, keep original_title in "title". Input:
%s
Return ONLY a raw JSON array.`, string(inputJSON))

	var lastErr error
	if !geminiQuotaLocked.Load() {
		client, err := c.getGenaiClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("помилка клієнта genai: %w", err)
		}

		for _, modelName := range c.getModels() {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			if err := c.waitForRateLimit(ctx); err != nil {
				return nil, err
			}

			config := &genai.GenerateContentConfig{
				Temperature:      genai.Ptr[float32](0.1),
				ResponseMIMEType: "application/json",
				ResponseSchema:   buildBulkTranslateSchema(),
			}

			resp, err := client.Models.GenerateContent(ctx, modelName, genai.Text(prompt), config)
			if err != nil {
				if isQuotaExhaustedError(err) {
					if geminiQuotaLocked.CompareAndSwap(false, true) {
						utils.LoggerWithTrace(ctx).Warn("gemini_quota_lock_enabled", slog.Any("error", err))
					}
				}
				lastErr = err
				utils.LoggerWithTrace(ctx).Warn("bulk_translate_failed", slog.String("model", modelName), slog.Any("error", err))
				continue
			}

			if len(resp.Candidates) > 0 {
				var results []BulkTranslateItem
				cleaned := cleanJSON(resp.Text())
				unmarshalErr := json.Unmarshal([]byte(cleaned), &results)
				if unmarshalErr == nil {
					return results, nil
				}
				lastErr = fmt.Errorf("parse error on %s: %w", modelName, unmarshalErr)
				continue
			}
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Grok fallback — last resort after all Gemini models failed
	if c.cfg.GrokAPIKey != "" {
		utils.LoggerWithTrace(ctx).Info("grok_translate_fallback")
		raw, grokErr := c.callGrok(ctx, prompt)
		if grokErr == nil {
			var results []BulkTranslateItem
			cleaned := cleanJSON(raw)
			if parseErr := json.Unmarshal([]byte(cleaned), &results); parseErr == nil {
				utils.LoggerWithTrace(ctx).Info("grok_translate_success",
					slog.Int("results_count", len(results)),
				)
				return results, nil
			} else {
				lastErr = fmt.Errorf("grok translate parse error: %w", parseErr)
			}
		} else {
			lastErr = grokErr
		}
	}

	if geminiQuotaLocked.Load() && c.cfg.GrokAPIKey == "" {
		return nil, fmt.Errorf("gemini quota lock is active")
	}

	return nil, fmt.Errorf("all AI models unavailable for translation (incl. Grok): %w", lastErr)
}
