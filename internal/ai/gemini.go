package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"movielist-app/internal/config"
	"movielist-app/internal/utils"
)

// RecognizedTitle — відповідь Gemini для одного файлу.
//
// Стратегія мержу з TMDB (пріоритет завжди у TMDB):
//
//	TMDB поле непорожнє → беремо TMDB
//	TMDB поле порожнє   → беремо Gemini як fallback
//
// Виняток: en_title та year — тільки для пошуку в TMDB, не зберігаємо напряму.
type RecognizedTitle struct {
	OriginalFile string `json:"original_file"` // ім'я файлу як є — для маппінгу
	ENTitle      string `json:"en_title"`      // EN назва для пошуку в TMDB
	TitleUA      string `json:"title_ua"`      // UA назва — fallback якщо TMDB порожній
	Year         *int   `json:"year"`          // рік — тільки якщо впевнений, інакше null
	MediaType    string `json:"media_type"`    // "movie" або "tv"
	Plot         string `json:"plot"`          // опис UA — fallback якщо TMDB порожній
	Genres       string `json:"genres"`        // жанри UA — fallback якщо TMDB порожній
	Cast         string `json:"cast"`          // актори — fallback якщо TMDB порожній
	Confidence   float64 `json:"confidence"`   // Оцінка впевненості 0.0-1.0, 0 якщо не вказано
}

// Структури для анмаршалінгу помилок Google RPC (RetryInfo)
type geminiErrDetail struct {
	Type       string `json:"@type"`
	RetryDelay string `json:"retryDelay"`
}

type geminiErrResp struct {
	Error struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Status  string            `json:"status"`
		Details []geminiErrDetail `json:"details"`
	} `json:"error"`
}



type Client struct {
	cfg         *config.Config
	httpClient  *http.Client
	rateLimiter *time.Ticker
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: 120 * time.Second},
		rateLimiter: time.NewTicker(2 * time.Second), // 1 запит/2сек для Gemini
	}
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	select {
	case <-c.rateLimiter.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecognizeBulk — пакетне розпізнавання імен файлів через Gemini.
// Повертає дані для пошуку в TMDB + fallback-поля для мержу.
func (c *Client) RecognizeBulk(ctx context.Context, filenames []string) ([]RecognizedTitle, error) {
	if len(filenames) == 0 {
		return nil, nil
	}

	prompt := buildPrompt(filenames)
	utils.LoggerWithTrace(ctx).Info("gemini_recognition_start", slog.Int("file_count", len(filenames)))

	return c.requestWithRetry(ctx, prompt)
}

func (c *Client) requestWithRetry(ctx context.Context, prompt string) ([]RecognizedTitle, error) {
	var lastErr error

	// Йдемо по списку моделей з конфігу (каскад). Пауз немає!
	for i, modelName := range c.cfg.GeminiModels {
		// Перевіряємо чи не скасовано контекст користувачем
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("контекст скасовано")
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
	utils.LoggerWithTrace(ctx).Error("gemini_all_models_failed",
		slog.Int("model_count", len(c.cfg.GeminiModels)),
		slog.Any("last_error", lastErr),
	)
	return nil, fmt.Errorf("всі моделі ШІ недоступні. Остання помилка: %v", lastErr)
}

func (c *Client) makeRequest(ctx context.Context, prompt, modelName string) ([]RecognizedTitle, error) {
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		modelName, c.cfg.GeminiAPIKey,
	)

	payload := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"response_mime_type": "application/json",
			"response_schema":    responseSchema(),
			"temperature":        0.05,
			"max_output_tokens":  8192,
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	for {
		if err := c.waitForRateLimit(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payloadBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			return parseResponse(respBody)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			var apiErr geminiErrResp
			if err := json.Unmarshal(respBody, &apiErr); err == nil {
				for _, detail := range apiErr.Error.Details {
					if detail.Type == "type.googleapis.com/google.rpc.RetryInfo" && detail.RetryDelay != "" {
						delay, parseErr := time.ParseDuration(detail.RetryDelay)
						if parseErr == nil {
							utils.LoggerWithTrace(ctx).Warn("api_error",
								slog.String("stage", "gemini"),
								slog.String("error_type", "rate_limit"),
								slog.Int("http_code", 429),
								slog.Bool("retryable", true),
								slog.Duration("retry_after", delay),
								slog.String("model", modelName),
							)
							select {
							case <-time.After(delay):
								continue // Повторюємо запит до тієї ж моделі
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
					}
				}
			}
			// Якщо RetryInfo немає або парсинг впав — повертаємо помилку (для переходу на іншу модель у каскаді)
			return nil, fmt.Errorf("HTTP 429: %s", string(respBody))
		}

		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
}

// responseSchema — повна схема з fallback-полями для мержу з TMDB
func responseSchema() map[string]any {
	return map[string]any{
		"type": "ARRAY",
		"items": map[string]any{
			"type": "OBJECT",
			"properties": map[string]any{
				"original_file": map[string]any{"type": "STRING","description": "exact original filename as provided, unchanged"},
				"en_title": map[string]any{"type": "STRING","description": "original English title for TMDB search. Must be the international release title, not a translation."},
				"title_ua": map[string]any{"type": "STRING","description": "official Ukrainian title. Empty string if unknown — do not invent."},
				"year": map[string]any{"type": "INTEGER","nullable": true,"description": "release year. null if uncertain."},
				"media_type": map[string]any{"type": "STRING","description": "\"movie\" or \"tv\". Use \"tv\" only for clear series markers."},
				"plot": map[string]any{"type": "STRING","description": "2-3 sentence plot summary in Ukrainian."},
				"genres": map[string]any{"type": "STRING","description": "comma-separated genres in Ukrainian."},
				"cast": map[string]any{"type": "STRING","description": "3-5 main actor names."},
				"confidence": map[string]any{"type": "NUMBER","description": "Confidence score 0.0-1.0, 0 if not provided."},
			},
			"required": []string{"original_file", "en_title", "media_type"},
		},
	}
}


// buildPrompt — промпт з прикладами транслітератів і правилами мержу
func buildPrompt(filenames []string) string {
	list := ""
	for _, f := range filenames {
		list += fmt.Sprintf("- %s\n", f)
	}

	return fmt.Sprintf(`You are a movie database expert. Your goal is to find the OFFICIAL ORIGINAL (English) title of the movie on IMDB/TMDB based on the provided transliterated or localized name and year.
CRITICAL: DO NOT translate localized titles literally! (e.g., DO NOT translate "Moj malenkij angel" as "My Little Angel" — find the actual release like "Foster"). Your data will be MERGED with TMDB results.

MERGE STRATEGY (important to understand your role):
- "en_title" → used to search TMDB. Must be exact TMDB-searchable title.
- "title_ua", "plot", "genres", "cast" → used as FALLBACK only when TMDB returns empty or English-only fields.
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
- "Кошачьи миры Луиса Уэйна" → en_title: "The Electrical Life of Louis Wain"

CRITICAL TRANSLATION RULES:
1. DO NOT translate localized titles literally! If you see a Russian localized title (e.g. "Воздушное ограбление", "Решала. Агент на миллиард"), you MUST find the actual global movie release matching that title and year (e.g. "Lift", "Mercato").
2. For transliterated files (e.g. "Vrag.2013", "Sekretnyi.Agent"), reverse-engineer the original Russian title ("Враг", "Секретный агент") and find the exact TMDB movie ("Enemy", "The Secret Agent").
3. Always verify the movie release year matches the filename!

FIELD RULES:
1. en_title: exact TMDB title. For non-English originals use the most common TMDB search title.
2. title_ua: official Ukrainian localization only. Empty string "" if unknown.
3. year: from filename if present. null if no year or uncertain.
4. plot: Ukrainian, 2-3 sentences. Empty string "" if you don't know this film.
5. genres: Ukrainian names only. Empty string "" if uncertain.
6. cast: real names, 3-5 actors. Empty string "" if uncertain.
7. media_type: "tv" only with explicit S01/Season markers. Otherwise "movie".

Files to process:
%s`, list)
}

// parseResponse витягує масив результатів з відповіді Gemini
func parseResponse(body []byte) ([]RecognizedTitle, error) {
	var googleResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(body, &googleResp); err != nil {
		return nil, fmt.Errorf("розбір відповіді Gemini: %w", err)
	}

	if len(googleResp.Candidates) == 0 ||
		len(googleResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("Gemini повернув порожню відповідь")
	}

	rawText := googleResp.Candidates[0].Content.Parts[0].Text

	var results []RecognizedTitle
	if err := json.Unmarshal([]byte(rawText), &results); err == nil {
		return results, nil
	}

	// Fallback: масив всередині об'єкта-обгортки
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawText), &wrapper); err != nil {
		limit := 200
		if len(rawText) < limit {
			limit = len(rawText)
		}
		return nil, fmt.Errorf("неможливо розпарсити відповідь: %s", rawText[:limit])
	}

	for _, val := range wrapper {
		var results []RecognizedTitle
		if err := json.Unmarshal(val, &results); err == nil && len(results) > 0 {
			return results, nil
		}
	}

	return nil, fmt.Errorf("масив результатів не знайдено у відповіді Gemini")
}

// translateWithRetry — універсальний метод перекладу з каскадом моделей та ретраями
func (c *Client) translateWithRetry(ctx context.Context, prompt string, fallbackText string) string {
	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]interface{}{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"temperature": 0.2,
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)


	// 🛡️ ПРАВИЛЬНЕ ВИПРАВЛЕННЯ: Беремо моделі з нашого центрального конфігу
	models := c.cfg.GeminiModels

	// Якщо в .env нічого не вказали (або змінна порожня), ставимо безпечний дефолт
	if len(models) == 0 {
		models = []string{"gemini-2.5-flash", "gemini-2.5-pro", "gemini-flash-latest", "gemini-2.5-flash-lite"}
	}

	for _, model := range models {
		url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, c.cfg.GeminiAPIKey)

		for attempt := 1; attempt <= 3; attempt++ {
			_ = c.waitForRateLimit(ctx)

			req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			resp, err := c.httpClient.Do(req)

			// ⚡ Реакція на ліміти (429 Too Many Requests) з RetryInfo
			if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				var apiErr geminiErrResp
				if err := json.Unmarshal(body, &apiErr); err == nil {
					for _, detail := range apiErr.Error.Details {
						if detail.Type == "type.googleapis.com/google.rpc.RetryInfo" && detail.RetryDelay != "" {
							delay, parseErr := time.ParseDuration(detail.RetryDelay)
							if parseErr == nil {
								utils.LoggerWithTrace(ctx).Warn("api_error",
									slog.String("stage", "gemini_translation"),
									slog.String("error_type", "rate_limit"),
									slog.Int("http_code", 429),
									slog.Bool("retryable", true),
									slog.Duration("retry_after", delay),
									slog.String("model", model),
								)
								select {
								case <-time.After(delay):
									continue // Ретрай тієї ж моделі (спробує знову в циклі attempt)
								case <-ctx.Done():
									return fallbackText
								}
							}
						}
					}
				}

				utils.LoggerWithTrace(ctx).Warn("api_error_no_retry_info",
					slog.String("stage", "gemini_translation"),
					slog.String("model", model),
					slog.Int("http_code", 429),
				)
				break // Виходимо з циклу спроб для цієї моделі і пробуємо наступну з конфігу
			}

			if err == nil && resp.StatusCode == http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				var googleResp struct {
					Candidates []struct {
						Content struct {
							Parts []struct {
								Text string `json:"text"`
							} `json:"parts"`
						} `json:"content"`
					} `json:"candidates"`
				}

				if err := json.Unmarshal(body, &googleResp); err == nil && len(googleResp.Candidates) > 0 && len(googleResp.Candidates[0].Content.Parts) > 0 {
					translated := strings.TrimSpace(googleResp.Candidates[0].Content.Parts[0].Text)
					translated = strings.Trim(translated, `"'«»`)
					if translated != "" {
						return translated // Успішний переклад!
					}
				}
			}

			if resp != nil {
				resp.Body.Close()
			}

			// Затримка з повагою до контексту (кнопки "Стоп")
			select {
			case <-ctx.Done():
				utils.LoggerWithTrace(ctx).Info("translation_cancelled")
				return fallbackText
			case <-time.After(time.Second * 2):
				// продовжуємо цикл
			}
		}
		utils.LoggerWithTrace(ctx).Warn("model_translation_failed", slog.String("model", model))
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
	Filename string `json:"filename"`
	Title    string `json:"title"`
	Plot     string `json:"plot"`
}

// TranslateBulk виконує пакетний переклад назв та описів за один HTTP-запит.
func (c *Client) TranslateBulk(ctx context.Context, items []BulkTranslateItem) ([]BulkTranslateItem, error) {
	if len(items) == 0 {
		return nil, nil
	}

	inputJSON, _ := json.MarshalIndent(items, "", "  ")

	prompt := fmt.Sprintf(`Твоя задача — масово знайти офіційні українські назви та описи для списку медіафайлів.

ПРАВИЛА:
1. Якщо title не є офіційною українською назвою (наприклад: російська, піратська, трансліт), знайди правильну українську назву з прокату/стрімінгів.
2. Якщо plot порожній або не українською — знайди або переклади офіційний опис українською.
3. Поверни результат ВИКЛЮЧНО у форматі JSON масиву об'єктів (ідентично до вхідного формату).
4. КЛЮЧОВЕ: Збережи значення поля "filename" без змін, щоб я міг співставити результати!

Вхідні дані:
%s`, string(inputJSON))

	resp := c.translateWithRetry(ctx, prompt, "")
	if resp == "" {
		return nil, fmt.Errorf("ШІ повернув порожню відповідь")
	}

	// Очищаємо від можливого Markdown-форматування
	resp = strings.TrimSpace(resp)
	resp = strings.TrimPrefix(resp, "```json")
	resp = strings.TrimPrefix(resp, "```")
	resp = strings.TrimSuffix(resp, "```")
	resp = strings.TrimSpace(resp)

	var results []BulkTranslateItem
	if err := json.Unmarshal([]byte(resp), &results); err != nil {
		utils.LoggerWithTrace(ctx).Error("bulk_translate_parse_error",
			slog.Any("error", err),
			slog.String("response", resp),
		)
		return nil, fmt.Errorf("помилка парсингу JSON: %w", err)
	}

	return results, nil
}
