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
	RequestID    string `json:"request_id"`
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
	ID                int     `json:"id"`
	RequestID         string  `json:"request_id"`
	OriginalFile      string  `json:"original_file"` // ім'я файлу як є — для маппінгу
	ENTitle           string  `json:"en_title"`      // оригінальна англійська назва (для TMDB пошуку)
	OriginalTitle     string  `json:"original_title,omitempty"`
	Year              *int    `json:"year"`
	PossibleYears     []int   `json:"possible_years,omitempty"`
	MediaType         string  `json:"media_type"` // "movie" або "tv"
	Country           string  `json:"country,omitempty"`
	DirectorOrCreator string  `json:"director_or_creator,omitempty"`
	Status            string  `json:"status,omitempty"`
	Reason            string  `json:"reason,omitempty"`
	Confidence        float64 `json:"confidence"` // Оцінка впевненості 0.0-1.0, 0 якщо не вказано
	Provider          string  `json:"-"`
	Model             string  `json:"-"`
}

type Client struct {
	cfg                   *config.Config
	limiter               *rate.Limiter
	grokLimiter           *rate.Limiter
	quotaLocked           atomic.Bool
	recognitionBatchCalls atomic.Int64
	recognitionRetryCalls atomic.Int64
	disambiguationCalls   atomic.Int64
	// 🟢 ДОДАНО: Динамічний каскад та м'ютекс для його захисту
	activeModels      []string
	modelsMu          sync.RWMutex
	unavailableModels sync.Map
	genaiClient       *genai.Client
	initMu            sync.Mutex
	httpClient        *http.Client // 🔴 ДОДАНО ДЛЯ ТЕСТІВ: Дозволяє мокувати відповіді API
	grokHTTPClient    *http.Client
}

type CallMetrics struct {
	RecognitionBatch int64
	RecognitionRetry int64
	Disambiguation   int64
}

func (c *Client) ResetCallMetrics() {
	c.recognitionBatchCalls.Store(0)
	c.recognitionRetryCalls.Store(0)
	c.disambiguationCalls.Store(0)
}

func (c *Client) CallMetrics() CallMetrics {
	return CallMetrics{
		RecognitionBatch: c.recognitionBatchCalls.Load(),
		RecognitionRetry: c.recognitionRetryCalls.Load(),
		Disambiguation:   c.disambiguationCalls.Load(),
	}
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
	c.unavailableModels.Range(func(key, _ any) bool {
		c.unavailableModels.Delete(key)
		return true
	})
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
		candidates = []string{"gemini-2.5-flash", "gemini-flash-lite-latest"}
	}

	var filtered []string
	for _, m := range candidates {
		mLower := strings.ToLower(m)
		// Exclude embedding, audio, tts, robotics, computer-use
		if strings.Contains(mLower, "embedding") ||
			strings.Contains(mLower, "audio") ||
			strings.Contains(mLower, "tts") ||
			strings.Contains(mLower, "image") ||
			strings.Contains(mLower, "preview") ||
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

// ResetQuotaLock clears the Gemini quota lock flag so that recovered quotas
// are retried in the next scan session rather than skipped permanently.
func (c *Client) ResetQuotaLock() {
	if c.quotaLocked.CompareAndSwap(true, false) {
		slog.Info("gemini_quota_lock_reset")
	}
}

// RecognizeBulk — пакетне розпізнавання імен файлів через Gemini.
// Повертає дані для пошуку в TMDB + fallback-поля для мержу.
func (c *Client) RecognizeBulk(ctx context.Context, contexts []FileRecognitionContext) ([]RecognizedTitle, error) {
	if len(contexts) == 0 {
		return nil, nil
	}

	prepared := append([]FileRecognitionContext(nil), contexts...)
	for i := range prepared {
		if prepared[i].RequestID == "" {
			prepared[i].RequestID = fmt.Sprintf("recognition-%d", i)
		}
	}
	prompt, err := buildPrompt(prepared)
	if err != nil {
		return nil, fmt.Errorf("build_prompt: %w", err)
	}
	utils.LoggerWithTrace(ctx).Info("gemini_recognition_start", slog.Int("file_count", len(contexts)))
	c.recognitionBatchCalls.Add(1)

	results, err := c.requestWithRetry(ctx, prompt)
	if err != nil {
		return nil, err
	}
	return c.retryMissingRecognitions(ctx, prepared, results)
}

func (c *Client) retryMissingRecognitions(ctx context.Context, contexts []FileRecognitionContext, results []RecognizedTitle) ([]RecognizedTitle, error) {
	matched := make(map[string]RecognizedTitle, len(results))
	legacy := make(map[int]RecognizedTitle, len(results))
	for _, result := range results {
		if result.RequestID != "" {
			matched[result.RequestID] = result
		} else {
			legacy[result.ID] = result
		}
	}

	ordered := make([]RecognizedTitle, 0, len(contexts))
	for _, input := range contexts {
		if result, ok := matched[input.RequestID]; ok {
			ordered = append(ordered, result)
			continue
		}
		// Compatibility with cached/older model responses during schema rollout.
		if result, ok := legacy[input.ID]; ok {
			result.RequestID = input.RequestID
			ordered = append(ordered, result)
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		utils.LoggerWithTrace(ctx).Warn("gemini_recognition_retry_missing",
			slog.String("request_id", input.RequestID),
			slog.String("file", input.OriginalFile),
		)
		c.recognitionRetryCalls.Add(1)
		prompt, err := buildPrompt([]FileRecognitionContext{input})
		if err != nil {
			return nil, err
		}
		retry, err := c.requestWithRetry(ctx, prompt)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			utils.LoggerWithTrace(ctx).Warn("gemini_recognition_retry_failed",
				slog.String("request_id", input.RequestID), slog.Any("error", err))
			continue
		}
		for _, result := range retry {
			if result.RequestID == input.RequestID || (result.RequestID == "" && result.ID == input.ID) {
				result.RequestID = input.RequestID
				ordered = append(ordered, result)
				break
			}
		}
	}
	return ordered, nil
}

func (c *Client) requestWithRetry(ctx context.Context, prompt string) ([]RecognizedTitle, error) {
	// Quota locked: skip Gemini cascade entirely and go straight to Grok.
	if c.quotaLocked.Load() {
		utils.LoggerWithTrace(ctx).Warn("gemini_quota_lock_skip_to_grok")
		return c.grokRecognizeFallback(ctx, prompt)
	}
	var lastErr error
	models := c.getModels() // 🟢: Беремо актуальний каскад

	// Йдемо по списку моделей (каскад)
	for i, modelName := range models {
		if _, unavailable := c.unavailableModels.Load(modelName); unavailable {
			continue
		}
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
			for index := range result {
				result[index].Provider = "gemini"
				result[index].Model = modelName
			}
			// Успіх! Повертаємо результат, не чіпаємо інші моделі
			if i > 0 {
				utils.LoggerWithTrace(ctx).Info("gemini_backup_model_success", slog.String("model", modelName))
			}
			return result, nil
		}

		// Якщо помилка, записуємо її і йдемо на наступну ітерацію (до наступної моделі)
		lastErr = err
		if isModelUnavailableError(err) {
			c.unavailableModels.Store(modelName, true)
			utils.LoggerWithTrace(ctx).Warn("gemini_model_disabled", slog.String("model", modelName))
		}
		utils.LoggerWithTrace(ctx).Warn("gemini_model_failed",
			slog.String("model", modelName),
			slog.Any("error", err),
		)
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Grok fallback after all Gemini models failed.
	result, grokErr := c.grokRecognizeFallback(ctx, prompt)
	if grokErr == nil {
		return result, nil
	}
	utils.LoggerWithTrace(ctx).Warn("grok_recognize_fallback_failed", slog.Any("error", grokErr))
	return nil, fmt.Errorf("all AI models unavailable (incl. Grok): gemini=%w; grok=%s", lastErr, grokErr)
}

func isModelUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "404") || strings.Contains(message, "not_found") ||
		strings.Contains(message, "no longer available")
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
			if c.quotaLocked.CompareAndSwap(false, true) {
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
				"id":                  {Type: genai.TypeInteger, Description: "exact integer identifier from input"},
				"request_id":          {Type: genai.TypeString, Description: "exact request_id from input"},
				"original_file":       {Type: genai.TypeString, Description: "exact original filename as provided, unchanged"},
				"en_title":            {Type: genai.TypeString, Description: "original English title for TMDB search. Must be the international release title, not a translation."},
				"original_title":      {Type: genai.TypeString, Description: "official original-language title, empty if unknown"},
				"year":                {Type: genai.TypeInteger, Nullable: genai.Ptr(true), Description: "release year. null if uncertain."},
				"possible_years":      {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeInteger}},
				"media_type":          {Type: genai.TypeString, Description: "\"movie\" or \"tv\". Use \"tv\" only for clear series markers."},
				"country":             {Type: genai.TypeString, Description: "production country, empty if unknown"},
				"director_or_creator": {Type: genai.TypeString, Description: "director for movie or creator for TV, empty if unknown"},
				"status":              {Type: genai.TypeString, Description: "resolved, ambiguous, or unresolved"},
				"reason":              {Type: genai.TypeString, Description: "short machine-readable reason"},
				"confidence":          {Type: genai.TypeNumber, Description: "Confidence score 0.0-1.0, 0 if not provided."},
			},
			Required: []string{"id", "request_id", "original_file", "en_title", "original_title", "possible_years", "media_type", "country", "director_or_creator", "status", "reason", "confidence"},
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
"request_id" and "original_file" in your response MUST exactly match the input.

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
4. status: resolved only when the identity is reliable; ambiguous for multiple plausible works; unresolved when unknown.
5. Return exactly one output per input. Never return or invent a TMDB ID.

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

	// safe to ignore: BulkTranslateItem contains only JSON-marshalable primitive fields.
	inputJSON, _ := json.Marshal(items)

	prompt := fmt.Sprintf(`Localize each item to official Ukrainian title (and plot when provided). Keep "filename" unchanged. Use original_title (when provided) as context to find the correct official Ukrainian title. If no official UA title exists, keep original_title in "title". Input:
%s
Return ONLY a raw JSON array.`, string(inputJSON))

	// Quota locked: skip Gemini cascade entirely and go straight to Grok.
	if c.quotaLocked.Load() {
		utils.LoggerWithTrace(ctx).Warn("gemini_quota_lock_skip_to_grok")
		return c.grokTranslateFallback(ctx, prompt)
	}

	var lastErr error
	client, err := c.getGenaiClient(ctx)
	if err != nil {
		utils.LoggerWithTrace(ctx).Warn("translate_genai_client_failed", slog.Any("error", err))
		lastErr = err
		goto grokFallback
	}

	for _, modelName := range c.getModels() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, unavailable := c.unavailableModels.Load(modelName); unavailable {
			continue
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
				if c.quotaLocked.CompareAndSwap(false, true) {
					utils.LoggerWithTrace(ctx).Warn("gemini_quota_lock_enabled", slog.Any("error", err))
				}
			}
			lastErr = err
			if isModelUnavailableError(err) {
				c.unavailableModels.Store(modelName, true)
				utils.LoggerWithTrace(ctx).Warn("gemini_model_disabled", slog.String("model", modelName))
			}
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
		// Порожні Candidates — можливий safety block або content filter
		lastErr = fmt.Errorf("model %s: empty candidates (possible safety block)", modelName)
		utils.LoggerWithTrace(ctx).Warn("bulk_translate_empty_candidates",
			slog.String("model", modelName))
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

grokFallback:
	// Grok fallback after all Gemini models failed.
	result, grokErr := c.grokTranslateFallback(ctx, prompt)
	if grokErr == nil {
		return result, nil
	}
	utils.LoggerWithTrace(ctx).Warn("grok_translate_fallback_failed", slog.Any("error", grokErr))
	return nil, fmt.Errorf("all AI models unavailable for translation (incl. Grok): gemini=%w; grok=%s", lastErr, grokErr)
}

// grokRecognizeFallback calls Grok as a fallback for bulk file recognition.
func (c *Client) grokRecognizeFallback(ctx context.Context, prompt string) ([]RecognizedTitle, error) {
	if c.cfg.GrokAPIKey == "" {
		return nil, fmt.Errorf("grok: not configured")
	}
	utils.LoggerWithTrace(ctx).Info("grok_recognize_fallback")
	raw, err := c.callGrok(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("grok recognize: %w", err)
	}
	result, err := parseRecognizeResponse(raw)
	if err != nil {
		return nil, fmt.Errorf("grok recognize parse: %w", err)
	}
	for index := range result {
		result[index].Provider = "grok"
		result[index].Model = c.cfg.GrokModel
		if result[index].Model == "" {
			result[index].Model = "grok-3-mini"
		}
	}
	utils.LoggerWithTrace(ctx).Info("grok_recognize_success", slog.Int("results_count", len(result)))
	return result, nil
}

// grokTranslateFallback calls Grok as a fallback for bulk translation.
func (c *Client) grokTranslateFallback(ctx context.Context, prompt string) ([]BulkTranslateItem, error) {
	if c.cfg.GrokAPIKey == "" {
		return nil, fmt.Errorf("grok: not configured")
	}
	utils.LoggerWithTrace(ctx).Info("grok_translate_fallback")
	raw, err := c.callGrok(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("grok translate: %w", err)
	}
	var results []BulkTranslateItem
	if err := json.Unmarshal([]byte(cleanJSON(raw)), &results); err != nil {
		return nil, fmt.Errorf("grok translate parse: %w", err)
	}
	utils.LoggerWithTrace(ctx).Info("grok_translate_success", slog.Int("results_count", len(results)))
	return results, nil
}
