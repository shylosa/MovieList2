package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"movielist-app/internal/config"
	"movielist-app/internal/utils"

	"golang.org/x/time/rate"
)

const (
	baseURL      = "https://api.themoviedb.org/3"
	imageBaseURL = "https://image.tmdb.org/t/p/w500"
)

// SearchCacheKey — ключ для кешування результатів пошуку (запобігає подвійним запитам).
type SearchCacheKey struct {
	query     string
	year      int
	mediaType MediaType
}

var (
	ErrNotFound            = fmt.Errorf("resource not found")
	reLatinOnly            = regexp.MustCompile(`^[a-zA-Z0-9\s\-\:\.,!?']+$`)
	reInvalidFilenameChars = regexp.MustCompile(`[^\w\-]`)
	homoglyphToLatin       = strings.NewReplacer(
		"а", "a", "о", "o", "е", "e", "с", "c", "р", "p", "х", "x", "у", "y", "і", "i",
		"А", "A", "О", "O", "Е", "E", "С", "C", "Р", "P", "Х", "X", "У", "Y", "І", "I",
	)
	homoglyphToCyrillic = strings.NewReplacer(
		"a", "а", "o", "о", "e", "е", "c", "с", "p", "р", "x", "х", "y", "у", "i", "і",
		"A", "А", "O", "О", "E", "Е", "C", "С", "P", "Р", "X", "Х", "Y", "У", "I", "І",
	)
)

func maskAPIKey(rawURL string) string {
	// 🟢 ОПТИМІЗАЦІЯ: Простий Replace працює швидше ніж повний парсинг URL
	// Знаходимо api_key=... та замінюємо на маску
	if idx := strings.Index(rawURL, "api_key="); idx != -1 {
		endIdx := strings.Index(rawURL[idx:], "&")
		if endIdx == -1 {
			return rawURL[:idx+8] + "***MASKED***"
		}
		return rawURL[:idx+8] + "***MASKED***" + rawURL[idx+endIdx:]
	}
	return rawURL
}

// Client — HTTP-клієнт для TMDB API
type Client struct {
	client      *http.Client
	apiKey      string
	postersDir  string
	rateLimiter *rate.Limiter

	// altTitlesCache — кеш для аліасів, щоб не смикати API для однакових ID
	altTitlesCache sync.Map

	// searchCache — кеш результатів пошуку (query+year+type)
	searchCache sync.Map
}

func NewClient(cfg *config.Config) *Client {
	// 🟢 ХІРУРГІЧНЕ ВТРУЧАННЯ: Логуємо критичну помилку, якщо директорію не створено
	if err := os.MkdirAll(cfg.PostersDir, 0755); err != nil {
		slog.Error("failed_to_create_posters_dir", slog.String("dir", cfg.PostersDir), slog.Any("error", err))
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20, // Оптимально для TMDB
		IdleConnTimeout:     90 * time.Second,
	}

	return &Client{
		client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
		apiKey:     cfg.TMDBAPIKey,
		postersDir: cfg.PostersDir,
		// 🟡 ХІРУРГІЧНЕ ВТРУЧАННЯ: 20 req/s (1 запит кожні 50мс), burst 5.
		// Ідеальний баланс між швидкістю і безпекою від 429 помилок.
		rateLimiter: rate.NewLimiter(rate.Every(50*time.Millisecond), 5),
	}
}

func (c *Client) Close() {
	// Новий rate.Limiter не потребує явного закриття
}

// ClearCaches — безпечне очищення кешу між скануваннями
func (c *Client) ClearCaches() {
	c.searchCache.Range(func(key, value any) bool {
		c.searchCache.Delete(key)
		return true
	})
	c.altTitlesCache.Range(func(key, value any) bool {
		c.altTitlesCache.Delete(key)
		return true
	})
	slog.Info("tmdb_caches_cleared")
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	return c.rateLimiter.Wait(ctx)
}

func (c *Client) doRequestWithRetry(ctx context.Context, url string, target any) error {
	const maxRetries = 3
	backoff := 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := c.doRequest(ctx, url, target)
		if err == nil {
			return nil
		}

		// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Якщо ресурс не знайдено (404), не робимо ретраї
		if err == ErrNotFound {
			return err
		}

		// Якщо це остання спроба або контекст скасований, повертаємо помилку
		if attempt == maxRetries-1 || ctx.Err() != nil {
			return err
		}

		utils.LoggerWithTrace(ctx).Warn("tmdb_request_attempt_failed",
			slog.Int("attempt", attempt+1),
			slog.String("url", maskAPIKey(url)), // 👈 МАСКУЄМО КЛЮЧ
			slog.Any("error", err),
			slog.Duration("backoff", backoff),
		)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
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

	// 🟢 ДОДАНО: Очищуємо омогліфи одразу після парсингу
	parsed.CleanTitle = resolveHomoglyphs(parsed.CleanTitle)

	utils.LoggerWithTrace(ctx).Info("filename_parsed",
		slog.String("filename", filename),
		slog.String("clean_title", parsed.CleanTitle),
		slog.Int("year", parsed.Year),
		slog.String("media_type", string(parsed.MediaType)),
		slog.String("imdb_id", parsed.IMDBID),
	)

	// 🟢 ПЕРЕВІРКА КЕШУ: struct-ключ не потребує алокацій strings.ToLower або fmt.Sprintf
	cacheKey := SearchCacheKey{
		query:     strings.ToLower(parsed.CleanTitle),
		year:      parsed.Year,
		mediaType: parsed.MediaType,
	}
	if val, ok := c.searchCache.Load(cacheKey); ok {
		info, _ := val.(*MovieInfo)
		utils.LoggerWithTrace(ctx).Info("tmdb_l1_cache_hit", slog.String("title", parsed.CleanTitle))
		return info, nil
	}

	// 1. Спроба 0: Прямий пошук по IMDb ID (найшвидший та найточніший)
	if parsed.IMDBID != "" {
		info, err := c.tryFindByIMDB(ctx, parsed.IMDBID, filename)
		if err == nil && info != nil {
			utils.LoggerWithTrace(ctx).Info("imdb_match_found", slog.String("imdb_id", parsed.IMDBID), slog.String("title", info.TitleUA))
			return info, nil // Ранній вихід!
		}
	}

	if parsed.CleanTitle == "" {
		utils.LoggerWithTrace(ctx).Warn("empty_title_after_parsing", slog.String("filename", filename))
		return nil, nil
	}

	return c.runPipeline(ctx, parsed, filename)
}

func (c *Client) tryFindByIMDB(ctx context.Context, imdbID, originalFilename string) (*MovieInfo, error) {
	// 🟡 ХІРУРГІЧНЕ ВТРУЧАННЯ: Додаємо language=uk-UA для отримання офіційної української назви
	url := fmt.Sprintf("%s/find/%s?api_key=%s&external_source=imdb_id&language=uk-UA", baseURL, imdbID, c.apiKey)

	var resp struct {
		MovieResults []tmdbSearchResult `json:"movie_results"`
		TvResults    []tmdbSearchResult `json:"tv_results"`
	}

	if err := c.doRequestWithRetry(ctx, url, &resp); err != nil {
		return nil, err
	}

	if len(resp.MovieResults) > 0 {
		return c.getMovieDetails(ctx, resp.MovieResults[0].ID, originalFilename)
	}
	if len(resp.TvResults) > 0 {
		return c.getTVDetails(ctx, resp.TvResults[0].ID, originalFilename)
	}
	return nil, nil
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
		TitleLang: detectLanguage(title),
	}

	if year != "" {
		parsed.Year = mustAtoi(year)
	}

	utils.LoggerWithTrace(ctx).Info("gemini_resolve_search",
		slog.String("title", title),
		slog.Int("year", parsed.Year),
		slog.String("type", string(mediaType)),
	)

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
	// 🟢 ВИПРАВЛЕННЯ: Прибрано ненадійний isLikelyEnglish. Якщо трансліт відрізняється — завжди пробуємо.
	cyrillicTitle := latinToCyrillic(parsed.CleanTitle)
	if cyrillicTitle != parsed.CleanTitle {
		utils.LoggerWithTrace(ctx).Info("transliteration_applied",
			slog.String("original", parsed.CleanTitle),
			slog.String("converted", cyrillicTitle),
		)

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
// 3. Транслітерація кирилиця → латиниця → TMDB (en-US індекс)
// 4. Сигнал "потрібен Gemini" (nil, nil)
func (c *Client) pipelineCyrillic(ctx context.Context, parsed ParsedFile, originalFilename string) (*MovieInfo, error) {
	if info := c.trySearch(ctx, parsed, originalFilename); info != nil {
		return info, nil
	}

	latinTitle := cyrillicToLatin(parsed.CleanTitle)
	if latinTitle != parsed.CleanTitle {
		utils.LoggerWithTrace(ctx).Info("cyrillic_to_latin_translit",
			slog.String("original", parsed.CleanTitle),
			slog.String("converted", latinTitle),
		)

		latinParsed := parsed
		latinParsed.CleanTitle = latinTitle
		latinParsed.TitleLang = TitleLangLatin

		if info := c.trySearch(ctx, latinParsed, originalFilename); info != nil {
			return info, nil
		}
	}

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
				// 🟢 ВИПРАВЛЕННЯ: Допускаємо похибку ±2 роки (фестивальні прем'єри vs широкий прокат)
				if diff < -2 || diff > 2 {
					utils.LoggerWithTrace(ctx).Warn("year_mismatch_blocking",
						slog.String("title", info.TitleEN),
						slog.Int("found_year", foundYear),
						slog.Int("expected_year", parsed.Year),
					)
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

	// 🟢 Використовуємо глобальний скомпільований Replacer (O(n) складність, нуль зайвих аллокацій)
	converted = cyrillicReplacer.Replace(converted)

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

// cyrillicToLatinMap — транслітерація кирилиці в латиницю для пошуку в en-US індексі TMDB.
var cyrillicToLatinMap = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'і': "i", 'ї': "i", 'є': "e", 'ґ': "g",
	'А': "A", 'Б': "B", 'В': "V", 'Г': "G", 'Д': "D", 'Е': "E", 'Ё': "Yo", 'Ж': "Zh",
	'З': "Z", 'И': "I", 'Й': "Y", 'К': "K", 'Л': "L", 'М': "M", 'Н': "N", 'О': "O",
	'П': "P", 'Р': "R", 'С': "S", 'Т': "T", 'У': "U", 'Ф': "F", 'Х': "Kh", 'Ц': "Ts",
	'Ч': "Ch", 'Ш': "Sh", 'Щ': "Shch", 'Ъ': "", 'Ы': "Y", 'Ь': "", 'Э': "E", 'Ю': "Yu", 'Я': "Ya",
	'І': "I", 'Ї': "I", 'Є': "E", 'Ґ': "G",
}

// cyrillicToLatin конвертує кириличну назву в латиницю для пошуку TMDB.
// "Слово Пацана" → "Slovo Patsana"
func cyrillicToLatin(s string) string {
	hasCyr := false
	for _, r := range s {
		if isCyrillic(r) {
			hasCyr = true
			break
		}
	}
	if !hasCyr {
		return s
	}

	var result strings.Builder
	for _, r := range s {
		if lat, ok := cyrillicToLatinMap[r]; ok {
			result.WriteString(lat)
		} else {
			result.WriteRune(r)
		}
	}
	return strings.TrimSpace(reSpaceFallback.ReplaceAllString(result.String(), " "))
}

// digraphMap — двосимвольні комбінації (порядок важливий: обробляються першими)
var digraphMap = map[string]string{
	"sh": "ш",
	"ch": "ч",
	"zh": "ж",
	"kh": "х",
	"ts": "ц",
	"ya": "я",
	"ju": "ю",
	"ja": "я",
	"jo": "ё",
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
	// 🟡 ХІРУРГІЧНЕ ВТРУЧАННЯ: 'c' у трансліті це майже завжди 'ц'. Для 'к' у нас і так спрацюють 'k' та 'q'.
	"c": "ц",
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

var cyrillicReplacer *strings.Replacer

func init() {
	// Ініціалізуємо Replacer один раз на старті програми
	args := make([]string, 0, len(cyrillicCorrections)*4)
	for k, v := range cyrillicCorrections {
		args = append(args, k, v)
		if len(k) > 0 {
			// Title Case (для слів з великої літери)
			titleK := strings.ToUpper(string([]rune(k)[0])) + string([]rune(k)[1:])
			titleV := strings.ToUpper(string([]rune(v)[0])) + string([]rune(v)[1:])
			args = append(args, titleK, titleV)
		}
	}
	cyrillicReplacer = strings.NewReplacer(args...)
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
		if resp.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("TMDB HTTP %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return err
	}
	return nil
}

func (c *Client) DownloadPoster(ctx context.Context, posterURL, filename string) (string, error) {
	flatName := strings.NewReplacer("\\", "_", "/", "_").Replace(filename)
	// Безпечне ім'я файлу — прибираємо все крім букв, цифр і дефісу
	safeName := reInvalidFilenameChars.ReplaceAllString(filepath.Base(flatName), "_")
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
	if err != nil {
		return "", fmt.Errorf("download poster error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download poster: HTTP %d", resp.StatusCode)
	}

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

// resolveHomoglyphs нормалізує суміш кирилиці та латиниці (омогліфи).
// Зводить рядок до домінуючої абетки.
func resolveHomoglyphs(s string) string {
	lat, cyr := 0, 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			lat++
		} else if unicode.Is(unicode.Cyrillic, r) {
			cyr++
		}
	}

	// Якщо є суміш, застосовуємо заміну
	if lat > 0 && cyr > 0 {
		if lat >= cyr {
			return homoglyphToLatin.Replace(s)
		}
		return homoglyphToCyrillic.Replace(s)
	}
	return s
}

type tmdbMovieAltTitles struct {
	Titles []struct {
		Title string `json:"title"`
	} `json:"titles"`
}

type tmdbTVAltTitles struct {
	Results []struct {
		Title string `json:"title"`
	} `json:"results"`
}

// getAlternativeTitles робить швидкий запит для отримання всіх аліасів
func (c *Client) getAlternativeTitles(ctx context.Context, id int, mediaType MediaType) ([]string, error) {
	// Спочатку перевіряємо кеш
	if val, ok := c.altTitlesCache.Load(id); ok {
		return val.([]string), nil
	}

	var titles []string

	if mediaType == MediaTypeMovie {
		url := fmt.Sprintf("%s/movie/%d/alternative_titles?api_key=%s", baseURL, id, c.apiKey)
		var resp tmdbMovieAltTitles
		if err := c.doRequestWithRetry(ctx, url, &resp); err == nil {
			for _, t := range resp.Titles {
				titles = append(titles, t.Title)
			}
		} else {
			return nil, err
		}
	} else {
		url := fmt.Sprintf("%s/tv/%d/alternative_titles?api_key=%s", baseURL, id, c.apiKey)
		var resp tmdbTVAltTitles
		if err := c.doRequestWithRetry(ctx, url, &resp); err == nil {
			for _, t := range resp.Results {
				titles = append(titles, t.Title)
			}
		} else {
			return nil, err
		}
	}

	// Зберігаємо в кеш перед поверненням
	c.altTitlesCache.Store(id, titles)
	return titles, nil
}
