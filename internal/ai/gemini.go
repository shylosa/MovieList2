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
	"time"

	"movielist-app/internal/config"
	"movielist-app/internal/tmdb"
	"movielist-app/internal/utils"

	"golang.org/x/time/rate"
	"google.golang.org/genai"
)

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
	cfg     *config.Config
	limiter *rate.Limiter
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
		limiter:        rate.NewLimiter(rate.Every(4*time.Second), 1),
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
		// Exclude embedding, audio, robotics, computer-use
		if strings.Contains(mLower, "embedding") ||
			strings.Contains(mLower, "audio") ||
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

	prompt := buildPrompt(contexts)
	utils.LoggerWithTrace(ctx).Info("gemini_recognition_start", slog.Int("file_count", len(contexts)))

	return c.requestWithRetry(ctx, prompt)
}

func (c *Client) requestWithRetry(ctx context.Context, prompt string) ([]RecognizedTitle, error) {
	var lastErr error
	models := c.getModels() // 🟢 ВИПРАВЛЕННЯ: Беремо актуальний каскад

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
		return nil, fmt.Errorf("помилка API Gemini: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("модель повернула порожню відповідь")
	}

	return parseRecognizeResponse(resp.Text())
}

func parseRecognizeResponse(raw string) ([]RecognizedTitle, error) {
	var results []RecognizedTitle
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return nil, fmt.Errorf("неможливо розпарсити JSON від моделі: %w", err)
	}
	return results, nil
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

// buildPrompt — промпт з прикладами транслітератів і правилами мержу
func buildPrompt(contexts []FileRecognitionContext) string {
	// safe to ignore: FileRecognitionContext contains JSON-marshalable primitive fields.
	filesJSON, _ := json.Marshal(contexts)

	return fmt.Sprintf(`You are a movie database expert. Your goal is to find the OFFICIAL ORIGINAL (English) title of the movie on IMDB/TMDB based on the provided transliterated or localized name and year.

PRE-PARSED FILE CONTEXT (from our local parser — TRUST these fields, do not re-guess year/media_type):
%s
For each item, "original_file" in your response MUST exactly match "original_file" from the input JSON.

MERGE STRATEGY (important to understand your role):
- "en_title" → used to search TMDB. Must be exact TMDB-searchable title.
- TMDB data always wins over your data when both exist.
- If uncertain, return an empty "en_title" instead of guessing.

TRANSLITERATION EXAMPLES (Russian-dubbed titles stored in Latin script):
- "Vrag" → en_title: "Enemy" (2013, Denis Villeneuve)
- "Banshi Inisherina" → en_title: "The Banshees of Inisherin"
- "Nochnoj Rejs" → en_title: "Red Eye" (2005, Wes Craven)
- "Убийца 2. Против всех" → en_title: "Sicario: Day of the Soldado"
- "Иллюзия обмана 3" → en_title: "Now You See Me 3"
- "Отпуск на двоих" (2026) → en_title: "People We Meet on Vacation"

CRITICAL TRANSLATION RULES:
1. Do not translate localized titles literally; find the actual global movie release matching that title and year.
2. For transliterated files, reverse-engineer the original Cyrillic title and find the exact TMDB movie.

FIELD RULES:
1. en_title: exact TMDB title. For non-English originals use the most common TMDB search title. Empty string "" if uncertain.
2. year: use parsed_year from input if present, otherwise from filename, null if uncertain.
3. media_type: use "parsed_media_type" from input. If absent, use "tv" only for explicit series markers (S01, Season N). Default: "movie".
IMPORTANT: Return ONLY a raw JSON array. No markdown, no code blocks, no explanation.`, string(filesJSON))
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

	client, err := c.getGenaiClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("помилка клієнта genai: %w", err)
	}

	inputJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("bulk translate marshal error: %w", err)
	}

	prompt := fmt.Sprintf(`Твоя задача — масово знайти офіційні українські назви та описи для списку медіафайлів.

ПРАВИЛА:
1. Якщо title не є офіційною українською назвою (наприклад: російська, піратська, трансліт), знайди правильну українську назву з прокату/стрімінгів. Орієнтуйся на "original_title" для точного пошуку.
2. Якщо plot порожній або не українською — знайди або переклади офіційний опис українською.
3. КЛЮЧОВЕ: Збережи значення поля "filename" без змін, щоб я міг співставити результати!
4. ЗАХИСТ ВІД ГАЛЮЦИНАЦІЙ: Якщо офіційної української прокатної назви не існує або ти її не знаєш, ЗАЛИШ "original_title" БЕЗ ЗМІН у полі "title". Не вигадуй назви.

Вхідні дані:
%s
IMPORTANT: Return ONLY a raw JSON array. No markdown, no code blocks, no explanation.`, string(inputJSON))

	var lastErr error
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
		if err == nil && len(resp.Candidates) > 0 {
			var results []BulkTranslateItem
			unmarshalErr := json.Unmarshal([]byte(resp.Text()), &results)
			if unmarshalErr == nil {
				return results, nil
			}
			lastErr = fmt.Errorf("parse error on %s: %w", modelName, unmarshalErr)
			continue
		}

		lastErr = err
		utils.LoggerWithTrace(ctx).Warn("bulk_translate_failed", slog.String("model", modelName), slog.Any("error", err))
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
			if parseErr := json.Unmarshal([]byte(raw), &results); parseErr == nil {
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
	return nil, fmt.Errorf("all AI models unavailable for translation (incl. Grok): %w", lastErr)
}
