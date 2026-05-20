package ai

import (
	"context"
	"encoding/json"
	"errors"
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
	activeModels []string
	modelsMu     sync.RWMutex
	genaiClient  *genai.Client
	initMu       sync.Mutex
	httpClient   *http.Client // 🔴 ДОДАНО ДЛЯ ТЕСТІВ: Дозволяє мокувати відповіді API
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		// rate.Every(4 * time.Second) = 15 RPM. Burst = 1.
		limiter: rate.NewLimiter(rate.Every(4*time.Second), 1),
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

	// Пріоритет 1: Динамічний список від API
	if len(c.activeModels) > 0 {
		return append([]string(nil), c.activeModels...)
	}
	// Пріоритет 2: Конфіг
	if len(c.cfg.GeminiModels) > 0 {
		return append([]string(nil), c.cfg.GeminiModels...)
	}
	// Пріоритет 3: Хардкод-фолбек
	return []string{"gemini-2.0-flash", "gemini-2.0-pro", "gemini-1.5-flash"}
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

	// Якщо цикл завершився, значить ВСІ моделі зі списку впали
	return nil, fmt.Errorf("всі моделі ШІ недоступні: %w", lastErr)
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

	rawText := resp.Text()

	var results []RecognizedTitle
	if err := json.Unmarshal([]byte(rawText), &results); err != nil {
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
	filesJSON, _ := json.Marshal(contexts)

	return fmt.Sprintf(`You are a movie database expert. Your goal is to find the OFFICIAL ORIGINAL (English) title of the movie on IMDB/TMDB based on the provided transliterated or localized name and year.
CRITICAL: DO NOT translate localized titles literally! (e.g., DO NOT translate "Moj malenkij angel" as "My Little Angel" — find the actual release like "Foster"). Your data will be MERGED with TMDB results.

PRE-PARSED FILE CONTEXT (from our local parser — TRUST these fields, do not re-guess year/media_type):
%s
For each item, "original_file" in your response MUST exactly match "original_file" from the input JSON.

MERGE STRATEGY (important to understand your role):
- "en_title" → used to search TMDB. Must be exact TMDB-searchable title.
- TMDB data always wins over your data when both exist.
- So: provide your best knowledge, but don't worry about perfection — TMDB will override where it can.

TRANSLITERATION EXAMPLES (Russian-dubbed titles stored in Latin script):
- "Vrag" → en_title: "Enemy" (2013, Denis Villeneuve)
- "Ella Makkej" → en_title: "Elle McKee" (2025)
- "Banshi Inisherina" → en_title: "The Banshees of Inisherin"
- "Nochnoj Rejs" → en_title: "Red Eye" (2005, Wes Craven)
- "Ubiystvennyiy podkast" → find actual EN title
- "Kollektorsha" → find actual EN title
- "Uspet Do Polunochi" → en_title: "Just Before Midnight" or similar
- Russian localized titles can be completely different from the original meaning. Use the release year to find the exact US/Global movie.
Example: "Отпуск на двоих" (2026) -> "People We Meet on Vacation"
Example: "Список подозреваемых" (2024) -> "Boneyard"

CYRILLIC EXAMPLES:
- "Убийца 2. Против всех" → en_title: "Sicario: Day of the Soldado"
- "Женщины" → en_title: "The Women" (2008)
- "Иллюзия обмана 3" → en_title: "Now You See Me 3"
- "Пригоди бравого вояка Швейка" → en_title: "The Good Soldier Švejk"
- "Кошачьи мири Луиса Уэйна" → en_title: "The Electrical Life of Louis Wain"

CRITICAL TRANSLATION RULES:
1. DO NOT translate localized titles literally! If you see a Russian localized title (e.g. "Воздушное ограбление", "Решала. Агент на миллиард"), you MUST find the actual global movie release matching that title and year (e.g. "Lift", "Mercato").
2. For transliterated files (e.g. "Vrag.2013", "Sekretnyi.Agent"), reverse-engineer the original Russian title ("Враг", "Секретный агент") and find the exact TMDB movie ("Enemy", "The Secret Agent").
3. Always verify the movie release year matches the filename!
4. CRITICAL RULE: If you are not 100%% sure about the match between the localized title and the original movie, return an empty string "" for the "en_title". DO NOT guess or hallucinate movies based solely on matching genres and release years. Accuracy is strictly prioritized over returning a result.

FIELD RULES:
1. en_title: exact TMDB title. For non-English originals use the most common TMDB search title. Empty string "" if uncertain.
3. year: from filename if present. null if no year or uncertain.
7. media_type: prefer "parsed_media_type" from input when present. Use "tv" only with explicit S01/Season markers. Otherwise "movie".
8. year: prefer "parsed_year" from input when present and valid.`, string(filesJSON))
}

// translateWithRetry — універсальний метод перекладу з каскадом моделей та ретраями
func (c *Client) translateWithRetry(ctx context.Context, prompt string, fallbackText string) string {
	models := c.getModels()
	client, err := c.getGenaiClient(ctx)
	if err != nil {
		utils.LoggerWithTrace(ctx).Error("genai_client_init_failed", slog.Any("error", err))
		return fallbackText
	}

	for _, modelName := range models {
		if ctx.Err() != nil {
			utils.LoggerWithTrace(ctx).Info("translation_cancelled")
			return fallbackText
		}

		if err := c.waitForRateLimit(ctx); err != nil {
			utils.LoggerWithTrace(ctx).Warn("translation_rate_limit_wait_failed", slog.Any("error", err))
			return fallbackText
		}

		config := &genai.GenerateContentConfig{
			Temperature: genai.Ptr[float32](0.2),
		}

		resp, err := client.Models.GenerateContent(ctx, modelName, genai.Text(prompt), config)
		if err != nil {
			utils.LoggerWithTrace(ctx).Warn("model_translation_failed", slog.String("model", modelName), slog.Any("error", err))
			continue
		}

		if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
			translated := strings.TrimSpace(resp.Text())
			translated = strings.Trim(translated, `"'«»`)
			if translated != "" {
				return translated
			}
		}
	}

	utils.LoggerWithTrace(ctx).Error("all_translation_models_failed")
	return fallbackText
}

// TranslateTitle адаптує назву українською мовою.
func (c *Client) TranslateTitle(ctx context.Context, title string, fallback string) string {
	if title == "" {
		return fallback
	}

	prompt := fmt.Sprintf("Переклади назву фільму українською мовою. Поверни ТІЛЬКИ переклад. Жодних пояснень, роздумів, лапок чи додаткових слів.\n\nНазва: %s", title)

	translated := c.translateWithRetry(ctx, prompt, fallback)

	lowerTrans := strings.ToLower(translated)
	if len(translated) > 100 || strings.Contains(lowerTrans, "thought") || strings.Contains(lowerTrans, "original:") {
		utils.LoggerWithTrace(ctx).Warn("gemini_monologue_detected",
			slog.String("translated", translated),
			slog.String("fallback", fallback),
		)
		return fallback // ВИПРАВЛЕННЯ: Повертаємо оригінал (російський текст), а не англійський фолбек
	}

	return translated
}

func (c *Client) TranslatePlot(ctx context.Context, text string) string {
	if text == "" {
		return ""
	}

	prompt := "Переклади цей опис фільму на гарну літературну українську мову. Поверни ТІЛЬКИ перекладений текст, без жодних додаткових коментарів, лапок чи пояснень:\n\n" + text
	return c.translateWithRetry(ctx, prompt, text)
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

	inputJSON, _ := json.Marshal(items)

	prompt := fmt.Sprintf(`Твоя задача — масово знайти офіційні українські назви та описи для списку медіафайлів.

ПРАВИЛА:
1. Якщо title не є офіційною українською назвою (наприклад: російська, піратська, трансліт), знайди правильну українську назву з прокату/стрімінгів. Орієнтуйся на "original_title" для точного пошуку.
2. Якщо plot порожній або не українською — знайди або переклади офіційний опис українською.
3. КЛЮЧОВЕ: Збережи значення поля "filename" без змін, щоб я міг співставити результати!
4. ЗАХИСТ ВІД ГАЛЮЦИНАЦІЙ: Якщо офіційної української прокатної назви не існує або ти її не знаєш, ЗАЛИШ "original_title" БЕЗ ЗМІН у полі "title". Не вигадуй назви.

Вхідні дані:
%s`, string(inputJSON))

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

	if lastErr == nil {
		lastErr = errors.New("невідома помилка")
	}
	return nil, fmt.Errorf("всі моделі для масового перекладу недоступні: %w", lastErr)
}
