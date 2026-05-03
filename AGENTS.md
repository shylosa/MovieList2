# AGENTS.md — MovieList App

Цей файл описує архітектуру, правила роботи з кодом та критичний контекст для AI-агентів що працюють з цим проектом.

---

## Проект: MovieList App

Десктопний застосунок для каталогізації локальної медіатеки. Сканує відео-файли на диску, автоматично розпізнає їх через TMDB API та Google Gemini AI, зберігає метадані в локальній SQLite базі.

**Stack:** Go 1.24+ · Wails v2 (desktop framework) · SQLite · TMDB API · Google Gemini AI SDK (`google.golang.org/genai`)

---

## Структура проекту

```
movielist-app/
├── main.go                        # Точка входу, Wails bootstrap, глобальний panic handler
├── app.go                         # Головний контролер: RunScan, FixSelected, UpdateMovie та всі Wails-методи
├── internal/
│   ├── ai/
│   │   └── gemini.go              # Gemini AI клієнт: розпізнавання назв та переклад
│   ├── config/
│   │   └── config.go              # Завантаження .env конфігурації
│   ├── scanner/
│   │   └── scanner.go             # Сканування диску: пошук відео-файлів
│   ├── sheets/
│   │   └── sheets.go              # Синхронізація з Google Sheets
│   ├── storage/
│   │   └── storage.go             # SQLite: CRUD для movies та ai_resolutions таблиць
│   ├── tmdb/
│   │   ├── models.go              # Типи: ParsedFile, MovieInfo, MediaType, scoring константи
│   │   ├── parser.go              # Парсинг імен файлів через go-ptn + власні доповнення
│   │   ├── client.go              # TMDB HTTP клієнт: pipeline пошуку, транслітерація, кеш
│   │   ├── search.go              # Scoring, ранжування результатів TMDB, Jaro-Winkler fuzzy
│   │   └── details.go             # Отримання деталей /movie/{id} та /tv/{id}
│   ├── utils/
│   │   ├── logger.go              # slog-based структурований логер з файлом
│   │   └── system.go              # Системні утиліти
│   └── web/
│       └── generator.go           # Генерація статичного HTML showcase
└── frontend/                      # Wails фронтенд (JS/HTML/CSS)
```

---

## Pipeline розпізнавання фільмів

Це найкритичніша частина проекту. Агент має розуміти повний flow:

```
Filename
    │
    ▼
[1] ParseFilename()               ← parser.go
    │  go-ptn парсер + власний wrapper
    │  Витягує: CleanTitle, Year, MediaType, TitleLang, IMDBID, ParentDir
    │  resolveHomoglyphs() — нормалізує змішані кирилиця/латиниця
    │
    ▼
[2] FetchFromFilename()           ← client.go
    │
    ├─ Спроба 0: IMDB ID → /find/{id}?external_source=imdb_id  (якщо є tt\d{7,8})
    │
    ├─ Спроба 1-2: /search/movie або /search/tv (залежно від MediaType)
    │              з роком і без року
    │
    ├─ Спроба 3: latinToCyrillic(title) → /search/movie або /search/tv
    │            (для транслітератів: "Vrag"→"Враг", "Banshi"→"Банши")
    │
    ├─ Спроба 4 (папка): ParseFilename(ParentDir) → пошук за назвою папки
    │
    └─ Якщо все провалилось → nil (файл іде в Gemini-чергу)
    │
    ▼
[3] rankResults() / scoreResult() ← search.go
    │  Scoring: ExactMatch(200), Contains(70), Jaro-Winkler fuzzy
    │  Year: exact(+150), ±1(+80), ±2 ok, >±2(-400)
    │  Lang: uk(+20), en(+10), ru>=2010(-50), ru<2010(-300)
    │  MediaType match: +30
    │  Popularity: min(pop/5, 50)
    │  titleScore==0 → score=-1000 (жорстке відхилення)
    │  Поріг прийняття: ScoreThreshold=200 (або 190 якщо year_diff==1)
    │
    ▼
[4] GetDetails()                  ← details.go
    │  GET /movie/{id} або /tv/{id}?language=uk-UA&append_to_response=credits
    │
    ▼
[5] Merge TMDB + Gemini           ← app.go mergeGeminiWithTMDB()
    │  TMDB завжди має пріоритет для непорожніх полів
    │  Gemini — fallback для порожніх полів (plot, genres, cast, title_ua)
    │
    ▼
[6] SaveMoviesBatch()             ← storage.go
    │  SQLite транзакція, ctx.Err() check в циклі, skip on error
    │
    ▼
[7] Gemini-черга (якщо TMDB не знайшов)    ← app.go processGeminiQueue()
    │  Батч по 10 файлів, семафор 2 паралельних запити
    │  RecognizeBulk() → ENTitle → FetchByCleanTitle() → Merge → Save
    │  L2 кеш: SaveAIResolution() після кожного успішного розпізнавання
    │
    ▼
[8] processTranslationQueue()     ← app.go
    │  Фаза перекладу: фільми з англійськими полями → Gemini TranslateBulk()
    │  Семафор 2 паралельних запити
```

---

## Критичні правила для агентів

### ❌ НІКОЛИ не робити

- **Не змінювати порядок спроб у pipeline** — Спроба 0 (IMDB ID) завжди першою, потім типізовані endpoints, потім транслітерація.
- **Не прибирати `ctx.Err()` перевірки** — вони потрібні для реакції на кнопку "Стоп".
- **Не замінювати `SaveMoviesBatch` на N окремих `SaveMovie`** — це регрес по продуктивності.
- **Не використовувати `/search/multi`** коли MediaType відомий — використовувати `/search/movie` або `/search/tv`.
- **Не компілювати regexp всередині функцій** що викликаються у циклах — виносити в `var` на рівні пакету.
- **Не довіряти `tmdb_id` від Gemini** — Gemini галюцинує ID. ID завжди верифікується через реальний TMDB запит.
- **Не зберігати `confidence < 0.5` від Gemini** — файл залишається нерозпізнаним для повторної спроби.
- **Не використовувати `http.Client` напряму для Gemini** — тільки `google.golang.org/genai` SDK.
- **Не використовувати `time.After` в циклах** — завжди `timer := time.NewTimer(d); defer timer.Stop()`.
- **Не мутувати `movieMap` всередині горутин** — він read-only після ініціалізації.
- **Не видаляти `defer tx.Rollback()`** — навіть після успішного Commit (no-op в SQLite, але захист від паніки).

### ✅ ЗАВЖДИ робити

- **`language=uk-UA`** у всіх TMDB запитах — `/movie/{id}`, `/tv/{id}`, `/find/{id}`.
- **`append_to_response=credits`** при запиті деталей фільму — щоб отримати акторів одним запитом.
- **`unicode.Is(unicode.Cyrillic, r)`** для перевірки кириличних символів — замість ручного перерахування діапазонів.
- **`strings.Builder`** для побудови великих рядків (промпти, списки файлів).
- **`strings.NewReplacer`** для багаторазових замін — ініціалізувати в `init()` або `var`, не всередині функції.
- **`rate.Limiter`** перед кожним зовнішнім API запитом (і TMDB і Gemini мають свої лімітери).
- **`ClearCaches()`** на початку кожного `RunScan()`.
- **Перевіряти `ctx.Err()`** перед кожним мережевим запитом і на початку кожного циклу обробки.
- **Лінива ініціалізація з retry** — використовувати `sync.Mutex` замість `sync.Once` для ініціалізації ШІ-клієнта. Це гарантує безпечний retry у разі помилки мережі без ризику data race.

---

## Конфігурація (.env)

```env
APP_VERSION=2.0
GEMINI_API_KEY=<required — panics on startup if missing>
GEMINI_MODELS=gemini-2.5-flash,gemini-2.0-flash,gemini-2.5-flash-lite   # cascade, comma-separated
TMDB_API_KEY=<required>
MEDIA_FOLDER_PATH=D:\Movies
EXCLUDE_FOLDERS=Downloads,Temp
DB_PATH=movies.db
HTML_PATH=index.html
POSTERS_DIR=posters
GOOGLE_SHEET_URL=https://docs.google.com/...
GOOGLE_SHEET_WORKSHEET_NAME=base
```

Конфіг завантажується з `.env` поруч з `.exe`. При `wails dev` — з поточної директорії.

---

## База даних (SQLite)

### Таблиця `movies`

| Колонка | Тип | Опис |
|---|---|---|
| `filename` | TEXT PK | Ім'я файлу (базове, без шляху) |
| `tmdb_id` | INTEGER | TMDB ID (0 якщо не знайдено) |
| `title_ua` | TEXT | Українська назва |
| `title_en` | TEXT | Оригінальна назва |
| `year` | TEXT | Рік випуску |
| `genres` | TEXT | Жанри через кому |
| `cast` | TEXT | Актори через кому |
| `plot` | TEXT | Опис |
| `poster_url` | TEXT | URL постера на TMDB |
| `local_poster_path` | TEXT | Шлях до локального постера |
| `media_type` | TEXT | `"movie"` або `"tv"` |

### Таблиця `ai_resolutions` (L2 кеш)

| Колонка | Тип | Опис |
|---|---|---|
| `original_filename` | TEXT PK | Ім'я файлу |
| `resolved_title` | TEXT | EN назва знайдена Gemini |
| `year` | INTEGER | Рік |
| `media_type` | TEXT | `"movie"` або `"tv"` |
| `confidence` | REAL | Впевненість Gemini (0.0–1.0) |

**Індекси:** `idx_tmdb_id`, `idx_title_en`
**Pragmas:** `journal_mode=WAL`, `synchronous=NORMAL`
**Міграція:** лінива через `ALTER TABLE ... ADD COLUMN` з ігноруванням "duplicate column" помилки.

---

## Критерій "розпізнано"

Файл вважається розпізнаним якщо `TmdbID > 0`. Перевіряється в `filterUnprocessed()`. Файли з `TmdbID == 0` повторно обробляються при кожному скані.

---

## Scoring деталі

```go
const (
    ScoreExactMatch     = 200   // Точний збіг назви
    ScoreContainsMatch  = 70    // Часткове входження
    ScoreYearExact      = 150   // Рік збігається точно
    ScoreYearDiffOne    = 80    // Різниця ±1 рік
    ScoreYearDiffTooFar = -400  // Різниця >±2 роки
    ScoreMediaTypeMatch = 30    // Тип медіа співпадає
    ScoreLangUA         = 20    // Оригінал українською
    ScoreLangEN         = 10    // Оригінал англійською
    ScoreLangRURecent   = -50   // Рос. виробництво після 2010
    ScoreLangRUOld      = -300  // Рос. виробництво до 2010
    ScorePopularityLimit = 50   // Макс бонус від popularity
    ScoreThreshold      = 200   // Мінімальний поріг прийняття
)
// Якщо year_diff == 1: поріг знижується до ScoreThreshold - 10
// Якщо titleScore == 0: score = -1000 (жорстке відхилення)
```

---

## Транслітерація (latinToCyrillic)

Застосовується як Спроба 3 якщо прямий EN пошук провалився. Обробляє діграфи першими (`sh→ш`, `ch→ч`, `zh→ж`, `kh→х`, `ts→ц`, `ya→я`, `yu→ю`, `ej→ей`), потім одиночні символи.

**Приклади:**
- `"Vrag"` → `"Враг"` → TMDB знаходить "Enemy"
- `"Banshi Inisherina"` → `"Банши Инишерина"` → "The Banshees of Inisherin"
- `"Nochnoj Rejs"` → `"Ночной Рейс"` → "Red Eye"

**`cyrillicReplacer`** — пост-корекція помилок транслітерації, ініціалізується в `init()`.

---

## Gemini інтеграція

**SDK:** `google.golang.org/genai` (не HTTP вручну)
**Singleton:** `initMu sync.Mutex` + `getGenaiClient()` — ліниво ініціалізується з можливістю безпечного retry у разі помилки.
**Каскад моделей:** `getModels()` повертає пріоритет: динамічний список (`SetModels`) → конфіг (`GeminiModels`) → хардкод fallback.
**Rate limiting:** `rate.Limiter` спільний для всього клієнта.
**Батчинг:** 10 файлів на запит, семафор `chan struct{}` з буфером 2 для обмеження паралелізму.

### Structured output schema (RecognizeBulk)

```
original_file  string   — ім'я файлу без змін (для маппінгу)
en_title       string   — EN назва для TMDB пошуку, "" якщо невпевнений
title_ua       string   — офіційна UA назва, "" якщо невідома
year           int|null — рік, null якщо невпевнений
media_type     string   — "movie" або "tv"
plot           string   — опис українською, "" якщо невідомий
genres         string   — жанри через кому українською
cast           string   — 3-5 акторів, "" якщо невідомо
confidence     float    — 0.0-1.0, 0 якщо не вказано
```

**Правило:** `confidence < 0.5` → файл зберігається як нерозпізнаний (`TmdbID=0`) для повторної спроби.

---

## TMDB Rate Limits

**Поточні налаштування:** `rate.Every(50*time.Millisecond), burst 5` (≈20 req/s)
**Офіційний ліміт TMDB:** 40 req/10s = 4 req/s. Але API толерує до 20 req/s.
**При 429:** `doRequestWithRetry` з exponential backoff.
**`ErrNotFound`** — ранній вихід на 404, без retry.

---

## Wails методи (публічний API для фронтенду)

```go
// Сканування
RunScan()                                   // Запускає повне сканування
StopScan()                                  // Перериває поточне сканування
FixSelected(selected []map[string]interface{}) // Виправити вибрані записи

// CRUD
GetMovies() ([]storage.Movie, error)
GetStats() map[string]interface{}
UpdateMovie(filename, hint string) error
DeleteMovie(filename string) error

// AI моделі
GetAIModels() ([]string, error)

// Інтеграції
SyncToCloud()
OpenSheet()
OpenShowcase()
OpenLogs()
OpenURL(url string)
```

**Wails events (backend → frontend):**
- `scan-started` — початок сканування
- `scan-progress` `{current, total, filename}` — прогрес
- `scan-finished` `message` — завершення
- `log-message` `string` — лог-повідомлення для UI

---

## Відомі обмеження та свідомі trade-offs

| Питання | Рішення | Причина |
|---|---|---|
| Паралельний Gemini | Семафор=2, не більше | RPM ліміт Gemini API |
| `SaveMoviesBatch` skip on error | `continue` + `slog.Warn` | Один поганий запис не ламає пачку |
| Рос. фільми | Штраф -50/-300, не блокування | Деякі рос. фільми є в колекції навмисно |
| `c` → `ц` в транслітерації | Спірно для деяких слів | Є Gemini як наступний крок |

---

## Залежності

```
github.com/wailsapp/wails/v2        — desktop framework
google.golang.org/genai             — Gemini AI SDK
github.com/razsteinmetz/go-ptn      — парсер імен торрент-файлів
github.com/xrash/smetrics           — Jaro-Winkler для fuzzy matching
modernc.org/sqlite                  — SQLite драйвер (CGO-free)
golang.org/x/time/rate              — rate limiter
golang.org/x/sync/errgroup          — structured concurrency
github.com/joho/godotenv            — .env завантаження
google.golang.org/api               — Google Sheets API
```

---

## Типові помилки при рефакторингу

**Помилка 1:** Замінити `FetchByCleanTitle` на `FetchFromFilename` після Gemini.
`FetchByCleanTitle` приймає вже чисту EN назву і не запускає парсинг файлу. `FetchFromFilename` парсить файл заново — це неправильно для Gemini-результатів.

**Помилка 2:** Прибрати перевірку `TmdbID > 0` в `filterUnprocessed`.
Якщо замінити на `TitleEN != ""` — файли де Gemini вгадав неправильну назву більше не перепробовуються.

**Помилка 3:** Додати `language=uk-UA` до `/search/movie` запиту.
TMDB search endpoint ігнорує `language` для scoring. `language` потрібен тільки для `/movie/{id}` та `/tv/{id}` (details).

**Помилка 4:** Паралельні запити до Gemini без семафора.
Gemini має RPM (requests per minute) ліміт, не RPS. Паралельні запити дадуть масові 429.

**Помилка 5:** Видалити `defer tx.Rollback()` бо "після Commit воно все одно no-op".
`defer` спрацює навіть якщо функція повернулась через panic — це важливий захист.
