package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"movielist-app/internal/config"
)

const (
	baseURL      = "https://api.themoviedb.org/3"
	imageBaseURL = "https://image.tmdb.org/t/p/w500"
)

// Client — HTTP-клієнт для TMDB API
type Client struct {
	client     *http.Client
	apiKey     string
	postersDir string
	rateLimiter *time.Ticker
}

func NewClient(cfg *config.Config) *Client {
	os.MkdirAll(cfg.PostersDir, 0755)
	return &Client{
		client:      &http.Client{Timeout: 15 * time.Second},
		apiKey:      cfg.TMDBAPIKey,
		postersDir:  cfg.PostersDir,
		rateLimiter: time.NewTicker(500 * time.Millisecond), // 2 запити/сек
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

func (c *Client) doRequestWithRetry(ctx context.Context, url string, target any) error {
	const maxRetries = 3
	backoff := 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := c.doRequest(ctx, url, target)
		if err == nil {
			return nil
		}

		// Якщо це остання спроба або контекст скасований, повертаємо помилку
		if attempt == maxRetries-1 || ctx.Err() != nil {
			return err
		}

		log.Printf("[TMDB] Спроба %d/3 не вдалася: %v. Повтор через %v", attempt+1, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff *= 2 // експоненціальний backoff
	}
	return nil
}

// FetchFromFilename — точка входу для сирого імені файлу.
// Парсить ім'я, визначає стратегію і запускає каскадний пошук.
func (c *Client) FetchFromFilename(ctx context.Context, filename string) (*MovieInfo, error) {
	parsed := ParseFilename(filename)
	log.Printf("[TMDB] 📂 Парсинг '%s' → title='%s' year=%d lang=%s type=%s",
		filename, parsed.CleanTitle, parsed.Year, parsed.TitleLang, parsed.MediaType)

	if parsed.CleanTitle == "" {
		log.Printf("[TMDB] ⚠️ Порожня назва після парсингу: '%s'", filename)
		return nil, nil
	}

	return c.runPipeline(ctx, parsed, filename)
}

// FetchByCleanTitle — точка входу після Gemini.
// Назва вже чиста (EN), парсинг не потрібен.
func (c *Client) FetchByCleanTitle(ctx context.Context, title, year string, mediaType MediaType) (*MovieInfo, error) {
	if strings.TrimSpace(title) == "" {
		return nil, nil
	}

	parsed := ParsedFile{
		OriginalName: title,
		CleanTitle:   title,
		MediaType:    mediaType,
		// 🟠 ВИПРАВЛЕННЯ: Динамічне визначення мови замість хардкоду TitleLangLatin
		TitleLang:    detectLanguage(title),
	}

	if year != "" {
		parsed.Year = mustAtoi(year)
	}

	log.Printf("[TMDB] 🤖 Пошук після Gemini: title='%s' year=%d type=%s",
		title, parsed.Year, mediaType)

	// 🟠 ВИПРАВЛЕННЯ: Замість searchDirectly використовуємо runPipeline,
	// щоб відпрацювала транслітерація, якщо Gemini повернув трансліт
	return c.runPipeline(ctx, parsed, title)
}

// runPipeline — повний каскад для сирого файлу
func (c *Client) runPipeline(ctx context.Context, parsed ParsedFile, originalFilename string) (*MovieInfo, error) {
	switch parsed.TitleLang {
	case TitleLangCyrillic:
		return c.pipelineCyrillic(ctx, parsed, originalFilename)
	default:
		return c.pipelineLatin(ctx, parsed, originalFilename)
	}
}

// pipelineLatin — стратегія для латинських назв:
// 1. TMDB напряму (з роком якщо є)
// 2. TMDB без року
// 3. Транслітерація → кирилиця → TMDB (для рос транслітів типу "Vrag", "Ella Makkej")
// 4. Сигнал "потрібен Gemini" (nil, nil)
func (c *Client) pipelineLatin(ctx context.Context, parsed ParsedFile, originalFilename string) (*MovieInfo, error) {
	// Спроби 1-2: пряма EN назва
	if info := c.trySearch(ctx, parsed, originalFilename); info != nil {
		return info, nil
	}

	// Спроба 3: транслітерація латиниці → кирилиця
	cyrillicTitle := latinToCyrillic(parsed.CleanTitle)
	if cyrillicTitle != parsed.CleanTitle {
		log.Printf("[TMDB] 🔤 Транслітерація: '%s' → '%s'", parsed.CleanTitle, cyrillicTitle)

		cyrParsed := parsed
		cyrParsed.CleanTitle = cyrillicTitle
		cyrParsed.TitleLang = TitleLangCyrillic

		if info := c.trySearch(ctx, cyrParsed, originalFilename); info != nil {
			return info, nil
		}
	}

	// Спроби вичерпано — Gemini на рівні вище
	return nil, nil
}

// pipelineCyrillic — стратегія для кириличних назв:
// 1. TMDB напряму (з роком якщо є)
// 2. TMDB без року
// 3. Сигнал "потрібен Gemini" (nil, nil)
func (c *Client) pipelineCyrillic(ctx context.Context, parsed ParsedFile, originalFilename string) (*MovieInfo, error) {
	if info := c.trySearch(ctx, parsed, originalFilename); info != nil {
		return info, nil
	}
	// Gemini на рівні вище
	return nil, nil
}

// searchDirectly — пошук без транслітерації (для чистих EN назв після Gemini)
func (c *Client) searchDirectly(ctx context.Context, parsed ParsedFile, originalFilename string) (*MovieInfo, error) {
	return c.trySearch(ctx, parsed, originalFilename), nil
}

// trySearch — виконує пошук і жорстко контролює результат
func (c *Client) trySearch(ctx context.Context, parsed ParsedFile, originalFilename string) *MovieInfo {
	// SearchWithFallbacks сама робить спроби "з роком", "без року" та "інший тип"
	info, err := c.SearchWithFallbacks(ctx, parsed, originalFilename)

	if err == nil && info != nil {
		// 🟠 ФОЛЛБЕК РОКУ: Якщо TMDB повернув порожній рік — підхоплюємо з парсера
		if info.Year == "" && parsed.Year > 0 {
			info.Year = fmt.Sprintf("%d", parsed.Year)
		}

		// 🛡️ БЕТОННА СТІНА ДЛЯ ВСІХ РЕЗУЛЬТАТІВ:
		if parsed.Year > 0 {
			foundYear := mustAtoi(info.Year)
			if foundYear > 0 {
				// 🔴 ВИНЯТОК ДЛЯ СЕРІАЛІВ: TMDB зберігає рік старту шоу (напр. 2013 для Рік і Морті),
				// а в файлі — рік релізу сезону (напр. 2023). Пропускаємо якщо TMDB старший або рівний.
				if parsed.MediaType == MediaTypeTV && foundYear <= parsed.Year {
					return info
				}

				diff := foundYear - parsed.Year
				// Допускаємо похибку ±1 рік
				if diff < -1 || diff > 1 {
					log.Printf("🛡️ [БЛОКУВАННЯ] Знайдено '%s' (%d), але файл вимагає %d. Відхиляємо!", info.TitleEN, foundYear, parsed.Year)
					return nil // Жорстко відхиляємо, віддаємо файл ШІ!
				}
			}
		}
		return info // Рік збігається — приймаємо
	}

	return nil
}

// --- Транслітерація латиниця → кирилиця (рос) ---

// latinToCyrillic конвертує рядок з латинського транслітерату у кирилицю.
// Обробляє діграфи першими, потім одиночні символи.
// "Vrag" → "Враг", "Ella Makkej" → "Элла Маккей", "Banshi" → "Банши"
func latinToCyrillic(s string) string {
	// Якщо рядок вже містить кирилицю — не чіпаємо
	for _, r := range s {
		if isCyrillic(r) {
			return s
		}
	}

	lower := strings.ToLower(s)
	var result strings.Builder
	runes := []rune(lower)
	origRunes := []rune(s) // 👈 Виносимо за межі циклу — виключаємо heap-алокацію на кожній ітерації
	i := 0

	for i < len(runes) {
		// Спочатку пробуємо діграфи (2 символи)
		if i+1 < len(runes) {
			digraph := string(runes[i : i+2])
			if cyr, ok := digraphMap[digraph]; ok {
				// Зберігаємо регістр першого символу оригіналу
				if i < len(origRunes) && isUpper(origRunes[i]) {
					result.WriteString(strings.ToUpper(cyr))
				} else {
					result.WriteString(cyr)
				}
				i += 2
				continue
			}
		}

		// Одиночний символ
		ch := string(runes[i])
		if cyr, ok := monographMap[ch]; ok {
			if i < len(origRunes) && isUpper(origRunes[i]) {
				result.WriteString(strings.ToUpper(cyr))
			} else {
				result.WriteString(cyr)
			}
		} else {
			// Невідомий символ (пробіл, цифра, пунктуація) — залишаємо як є
			result.WriteRune(runes[i])
		}
		i++
	}

	converted := result.String()

	// 🔴 ВИПРАВЛЕННЯ: Пост-корекція типових втрат (напр. malenkij -> маленкий -> маленький)
	for k, v := range cyrillicCorrections {
		// Заміна для нижнього регістру
		converted = strings.ReplaceAll(converted, k, v)

		// Заміна для Title Case (якщо слово з великої літери)
		if len(k) > 0 {
			titleK := strings.ToUpper(string([]rune(k)[0])) + string([]rune(k)[1:])
			titleV := strings.ToUpper(string([]rune(v)[0])) + string([]rune(v)[1:])
			converted = strings.ReplaceAll(converted, titleK, titleV)
		}
	}

	// Якщо конвертація не дала кирилиці — повертаємо оригінал
	hasCyr := false
	for _, r := range converted {
		if isCyrillic(r) {
			hasCyr = true
			break
		}
	}
	if !hasCyr {
		return s
	}
	return converted
}

// digraphMap — двосимвольні комбінації (порядок важливий: обробляються першими)
var digraphMap = map[string]string{
	"sh": "ш",
	"ch": "ч",
	"zh": "ж",
	"kh": "х",
	"ts": "ц",
	"ya": "я",
	"yu": "ю",
	"yo": "ё",
	"ye": "е",
	"iy": "ий",
	"yy": "ый",
	"ej": "ей", // Makkej → Маккей
	"ck": "кк", // Makkej → Маккей
}

// monographMap — одиночні символи
var monographMap = map[string]string{
	"a": "а",
	"b": "б",
	"v": "в",
	"g": "г",
	"d": "д",
	"e": "е",
	"z": "з",
	"i": "и",
	"j": "й",
	"k": "к",
	"l": "л",
	"m": "м",
	"n": "н",
	"o": "о",
	"p": "п",
	"r": "р",
	"s": "с",
	"t": "т",
	"u": "у",
	"f": "ф",
	"y": "ы",
	"x": "кс",
	"q": "к",
	"w": "в",
	"h": "х",
	"c": "к",
	"'": "ь",
}

// cyrillicCorrections — пост-корекція популярних "піратських" транслітів
// (повернення втрачених м'яких знаків тощо)
var cyrillicCorrections = map[string]string{
	"маленк": "маленьк",
	"болш":   "больш",
	"силн":   "сильн",
	"филм":   "фильм",
	"ден ":   "день ",
	"тма":    "тьма",
	"цар ":   "царь ",
}


func isUpper(r rune) bool {
	return r >= 'A' && r <= 'Z'
}

// --- HTTP helpers ---

func (c *Client) doRequest(ctx context.Context, url string, target any) error {
	if err := c.waitForRateLimit(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("doRequest: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("doRequest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("TMDB HTTP %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return err
	}
	// Дренуємо залишки тіла — Go перевикористає TCP-з'єднання (keep-alive)
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) DownloadPoster(ctx context.Context, posterURL, filename string) (string, error) {
	// Безпечне ім'я файлу — прибираємо все крім букв, цифр і дефісу
	safeName := regexp.MustCompile(`[^\w\-]`).ReplaceAllString(filepath.Base(filename), "_")
	path := filepath.Join(c.postersDir, safeName+".jpg")

	// Вже є на диску — не качаємо повторно
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", posterURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download poster: HTTP %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err = io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return path, nil
}

// mustAtoi — strconv.Atoi без помилки (повертає 0 при невалідному вводі)
func mustAtoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
