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
│   │                           # isGoodUkrainian() — детекція автентичної української
│   │                           # searchAndFetch() — двоїстий пошук UA+RU для кириличних запитів
│   └── details.go             # Отримання деталей /movie/{id} та /tv/{id}
│                               # Водоспад метаданих: UA → RU → EN каскад
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
[3] searchAndFetch() — ДВОЇСТИЙ ПОШУК (UA + RU каскад) ← search.go
    │  Для кириличних запитів: пошук спочатку в uk-UA, потім ru-RU індексах
    │  Порівняння балів з обох індексів, вибір найкращого результату
    │  Розширює покриття для транслітів та локалізацій
    │
    ▼
[4] rankResults() / scoreResult() ← search.go
    │  Scoring: ExactMatch(200), Contains(70), Jaro-Winkler fuzzy
    │  Year: exact(+150), ±1(+80), ±2 ok, >±2(-400)
    │  Lang: uk(+20), en(+10), ru>=2010(-50), ru<2010(-300)
    │  MediaType match: +30
    │  Popularity: min(pop/5, 50)
    │  titleScore==0 → score=-1000 (жорстке відхилення)
    │  Поріг прийняття: ScoreThreshold=200 (або 190 якщо year_diff==1)
    │
    ▼
[5] GetDetails() — ВОДОСПАД МЕТАДАНИХ (UA → RU → EN) ← details.go
    │  Каскад: uk-UA → ru-RU → en-US для отримання повних деталей
    │  Дозаповнення порожніх полів з наступних мов
    │  isGoodUkrainian() — детекція автентичної української (і,ї,є,ґ)
    │  Ранній вихід при наявності якісного українського контенту
    │
    ▼
[6] Merge TMDB + Gemini           ← app.go mergeGeminiWithTMDB()
    │  TMDB завжди має пріоритет для непорожніх полів
    │  Gemini — fallback для порожніх полів (plot, genres, cast, title_ua)
    │
    ▼
[7] SaveMoviesBatch()             ← storage.go
    │  SQLite транзакція, ctx.Err() check в циклі, skip on error
    │
    ▼
[8] Gemini-черга (якщо TMDB не знайшов)    ← app.go processGeminiQueue()
    │  Батч по 10 файлів, послідовна обробка через один контекст
    │  RecognizeBulk() → ENTitle → FetchByCleanTitle() → Merge → Save
    │  Повертає список файлів у яких movie.TmdbID > 0 (верифіковані через TMDB)
    │  L2 кеш: SaveAIResolution() після кожного успішного розпізнавання
    │  Файли з Confidence < 0.5 залишаються як нерозпізнані (TmdbID=0)
    │
    ▼
[9] processTranslationQueue()     ← app.go
    │  Фаза перекладу: файли з англійськими/порожніми полями → Gemini TranslateBulk()
    │  Послідовна обробка батчів по 10 файлів
    │  Оновлення title_ua/plot, якщо потрібно
    │  Семафор 2 паралельних запити (але реального паралелізму немає через rate.Limiter)
```

---

## Критичні правила для агентів

### ❌ НІКОЛИ не робити

- **Не змінювати порядок спроб у pipeline** — Спроба 0 (IMDB ID) завжди першою, потім типізовані endpoints, потім транслітерація.
- **Не прибирати `ctx.Err()` перевірки** — вони потрібні для реакції на кнопку "Стоп".
- **Не перевіряти `ctx.Err()` після виклику `cancel()`** — стан скасування треба фіксувати до закриття контексту, щоб не отримати хибне "перервано користувачем".
- **Не замінювати `SaveMoviesBatch` на N окремих `SaveMovie`** — це регрес по продуктивності.
- **Не використовувати `/search/multi`** коли MediaType відомий — використовувати `/search/movie` або `/search/tv`.
- **Не компілювати regexp всередині функцій** що викликаються у циклах — виносити в `var` на рівні пакету.
- **Не довіряти `tmdb_id` від Gemini** — Gemini галюцинує ID. ID завжди верифікується через реальний TMDB запит.
- **Не зберігати `confidence < 0.5` від Gemini** — файл залишається нерозпізнаним для повторної спроби.
- **Не дублювати поріг confidence у кількох місцях** — перевірка зберігається в одному місці, через константу `geminiMinConfidence`.
- **Не використовувати `http.Client` напряму для Gemini** — тільки `google.golang.org/genai` SDK.
- **Не ускладнювати процес Gemini паралелізмом** — `processGeminiQueue()` працює послідовно (batching без `errgroup`), оскільки обмеження rate limiter робить багато паралельних горутин марними.
- **Не використовувати `time.After` в циклах** — завжди `timer := time.NewTimer(d); defer timer.Stop()`.
- **Не мутувати `movieMap` всередині горутин** — він read-only після ініціалізації.
- **Не видаляти `defer tx.Rollback()`** — навіть після успішного Commit (no-op в SQLite, але захист від паніки).
- **Не додавати файли до `translationQueue` без перевірки розпізнавання** — тільки файли з `movie.TmdbID > 0` повинні йти в переклад.
- **Не додавати всі результати `UpdateMovie` до черги без перевірки** — додавати тільки якщо повернула `nil` (сукцес).
- **Не дублювати логіку отримання моделей** — використовувати `singleflight.Group.Do()` для дедубліювання.
- **Не пропускати HTML елементи в пошуку** — `id="noResults"`, `id="filteredCount"`, `id="filteredNum"` обов'язкові.
- **Не змінювати каскад мов у `searchAndFetch()`** — завжди uk-UA → ru-RU для кириличних запитів.
- **Не використовувати `hasCyrillicChars()` замість `isGoodUkrainian()`** — `isGoodUkrainian()` більш точний для детекції автентичної української.
- **Не змінювати порядок мов у `GetDetails()`** — завжди uk-UA → ru-RU → en-US для отримання метаданих.

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
- **HTML елементи для пошуку** — `index.html` та `generator.go` шаблон мають містити:
  - `<span id="filteredCount">` — показ кількості знайдених результатів
  - `<div id="noResults">` — повідомлення про відсутність результатів
  - `<strong id="filteredNum">` — динамічне оновлення числа знайдених (без цих елементів будуть помилки у консолі браузера)
- **`processGeminiQueue()` повертає список верифікованих файлів** — тільки файли з `movie.TmdbID > 0` додаються до `translationQueue`, щоб не перекладати порожні записи.
- **`FixSelected()` перевіряє помилку `UpdateMovie`** — файли додаються до `translationQueue` лише якщо `UpdateMovie` повернула `nil` (сукцес).
- **`GetAIModels(ctx context.Context)` використовує `singleflight.Group`** — одночасні запити дедубліюються, перший отримує від API, рештві чекають результату без додаткових HTTP запитів.
- У `RunScan` warmup-горутині виклик змінено на `a.GetAIModels(ctx)`.
- **`processTranslationQueue()` спрощено до послідовного батчингу** — без `errgroup`, але зі семафором 2 паралельних запити (реального паралелізму немає через `rate.Limiter` в SDK).
- **`isGoodUkrainian()` для детекції української** — перевіряє наявність і,ї,є,ґ замість загальної кириличної перевірки.
- **Двоїстий пошук для кириличних запитів** — uk-UA → ru-RU з вибором найкращого результату за балом.
- **Водоспад метаданих** — uk-UA → ru-RU → en-US з дозаповненням порожніх полів.

---

## Конфігурація (.env)

```env
APP_VERSION=2.0
GEMINI_API_KEY=<required — panics on startup if missing>
GEMINI_MODELS=gemini-2.5-flash,gemini-2.0-flash,gemini-2.5-flash-lite   # cascade, comma-separated (removed gemini-3-flash-preview)
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
**Pragmas:** `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`
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

// Нові правила для двоїстого пошуку:
// - Для кириличних запитів порівнюються результати з uk-UA та ru-RU індексів
// - Обирається результат з вищим загальним score
// - Розширює покриття для транслітів та локальних назв
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
**HTTP клієнт в тестах:** якщо `httpClient` заданий, він використовується для genai.
**Каскад моделей:** `GetAIModels()` → одночасні запити дедубліюються через `singleflight.Group` → перший запит отримує список від API → наступні запити одночасно чекають результату без додаткових HTTP-запитів → активний список (`SetModels`) → конфіг (`GeminiModels`) → хардкод fallback.
**Warmup:** `RunScan()` асинхронно викликає `GetAIModels()` щоб `aiClient` отримав актуальні моделі до Gemini пачки.
**Rate limiting:** `rate.Limiter` спільний для всього клієнта.
**Батчинг:** 10 файлів на запит, послідовна обробка через один контекст (без `errgroup`).
**Переклад:** послідовна обробка батчів з семафором 2 паралельних запити (`chan struct{}`), але реального паралелізму немає через `rate.Limiter` всередині SDK.
**Перекладна безпека:** якщо офіційної UA назви немає — Gemini залишає `original_title` без змін.
**Переключення режимів:** `movieMap` у `processTranslationQueue()` — read-only; не мутувати всередині горутин.
**Персистенція:** `SaveAIResolution()` зберігає L2-кеш після кожного успішного розпізнавання.

### Структура результатів processGeminiQueue

`processGeminiQueue()` повертає `[]string` — список успішно розпізнаних файлів (тих, у яких `movie.TmdbID > 0` після мержу з TMDB). Файли з `Confidence < 0.5` або ті, що TMDB не верифікував, залишають як нерозпізнані (`TmdbID=0`) і не потрапляють у `translationQueue`.

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

**Примітка:** цей поріг зберігається в коді через константу `geminiMinConfidence`, щоб перевірка була однією і надійною.

---

## "Лінгвістичний комбайн" — система UA/RU/EN каскадів

### Двоїстий пошук (searchAndFetch)

Для кириличних запитів система виконує послідовний пошук в обох індексах TMDB:

1. **uk-UA індекс** — пріоритетний пошук в українській локалізації
2. **ru-RU індекс** — fallback для фільмів, що відсутні в UA але доступні в RU

**Логіка вибору:** порівнюються бали результатів з обох індексів, обирається найкращий за загальним score.

**Переваги:**

- Розширює покриття для транслітів ("Vrag" → знаходить "Enemy/Враг" в RU індексі)
- Ловить локалізації, що існують тільки в російській версії TMDB
- Підтримує фільми з різними назвами в різних мовах

### Водоспад метаданих (GetDetails)

Після отримання TMDB ID система витягує повні деталі каскадом мов:

1. **uk-UA** — пріоритетна українська локалізація
2. **ru-RU** — дозаповнення порожніх полів російською (якщо UA порожня)
3. **en-US** — фінальний fallback на англійську

**isGoodUkrainian()** — спеціальна функція детекції автентичної української:

- Перевіряє наявність літер і,ї,є,ґ (і,І,ї,Ї,є,Є,ґ,Ґ)
- Відрізняє справжню українську від загальної кириличної

**Ранній вихід:** якщо знайдено якісну українську назву + опис, цикл переривається.

**Результат:** максимальне використання TMDB перед зверненням до Gemini. Якщо в RU знайдено багатий опис з "ыэъ", Gemini отримує якісний матеріал для перекладу замість англійського оригіналу.

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

---

## Ревізія 4 — виправлення та оптимізації

### Зміни в `app.go`

- **`GetAIModels()`** тепер використовує `singleflight.Group` замість простого double-checked locking. Одночасні запити дедубліюються — перший робить HTTP запит, рештві чекають результату без додаткових запитів до API.
- **`processGeminiQueue()`** тепер повертає `[]string` — список файлів у яких `movie.TmdbID > 0` (верифіковані через TMDB). Тільки ці файли потрапляють у `translationQueue`.
- **`processTranslationQueue()`** спрощено — послідовне батчинг без `errgroup`, але зі семафором на 2 паралельних запити. Реального паралелізму немає через `rate.Limiter` всередину SDK.
- **`FixSelected()`** тепер перевіряє помилку `UpdateMovie` перед додаванням у `translationQueue`. Файли додаються лише якщо `err == nil`.
- **`mergeGeminiWithTMDB()`** перевіряє `movie.TmdbID > 0` перед додаванням до `recognizedFiles` (був баг: файли з confidence<0.5 йшли на переклад порожніх записів).

### Зміни в `internal/config/config.go`

- **Дефолтний список моделей** — замінено з `"gemini-3-flash-preview,gemini-2.5-flash,..."` на `"gemini-2.5-flash,gemini-2.0-flash,gemini-2.5-flash-lite"` (видалена неіснуюча модель).

### Зміни в `internal/web/generator.go` та `index.html`

- **Додано HTML елементи для пошуку**:
  - `<span id="filteredCount">` — контейнер для показу кількості
  - `<strong id="filteredNum">` — число знайдених результатів
  - `<div id="noResults">` — повідомлення коли нічого не знайдено
  - Без цих елементів JavaScript падає з "Cannot set properties of null" помилкою при пошуку.

### Дедубліювання запитів (singleflight)

```go
type App struct {
    // ...
    modelsGroup   singleflight.Group  // Нове
}

func (a *App) GetAIModels(ctx context.Context) ([]string, error) {
    // 1. Перевіріємо L1 кеш під RLock (швидко)
    a.modelsMutex.RLock()
    if len(a.aiModelsCache) > 0 {
        cache := append([]string(nil), a.aiModelsCache...)
        a.modelsMutex.RUnlock()
        return cache, nil
    }
    a.modelsMutex.RUnlock()

    // 2. Одночасні запити дедубліюються
    value, err, _ := a.modelsGroup.Do("ai_models", func() (interface{}, error) {
        // Робимо HTTP запит, але тільки перший потік його виконує
        // Рештві потоки чекають результату від a.modelsGroup
        // ...
    })
    // ...
}
```

**Результат:** При 10 одночасних запитах до `GetAIModels()` робиться лише 1 HTTP запит замість 10.

### Критичні поправки

| # | Проблема | Статус |
|---|---|---|
| P0 | HTML елементи для пошуку | ✅ Додано |
| P0 | `recognizedFiles` без перевірки `TmdbID` | ✅ Виправлено |
| P1 | `FixSelected` додає файли без перевірки | ✅ Виправлено |
| P1 | `GetAIModels` робить подвійні запити | ✅ Виправлено (singleflight) |
| P1 | `processTranslationQueue` зі складністю errgroup | ✅ Спрощено |
| P2 | Дефолт містить `gemini-3-flash-preview` | ✅ Видалено |
| P3 | `cleanDeletedFiles` дублює роботу `CleanMissingMovies` | ✅ Видалено |

### Ревізія 8 — Виправлення помилок та оптимізації

#### Крок 1 — Видалення cleanDeletedFiles

- **[app.go / RunScan]** Видалено виклик `cleanDeletedFiles` та оновлено коментар поряд з `CleanMissingMovies`.
- **[app.go]** Видалено функцію `cleanDeletedFiles` повністю, оскільки її логіку повністю покриває `CleanMissingMovies`.

Решта проблем залишена як низькопріоритетна або прийнятна:

- L2 кеш має різні пороги (0.6 vs 0.5) — свідомо
- `Movie.ID` не заповнюється — поле невикористовується, можна видалити в P3
- `searchDirectly` — мертвий код, можна видалити в P3

---

## Ревізія 5 — логічні оптимізації пайплайну

### Зміни в ранжуванні TMDB (`search.go`)

- **Перевірка аліасів:** Алгоритм тепер перевіряє альтернативні назви (аліаси) **до** жорсткого відхилення результату (`titleScore == 0`). Раніше фільми, які в TMDB мали зовсім іншу оригінальну назву (і нульовий базовий score), відкидалися до перевірки їх аліасів.

### Зміни в логіці застосунку (`app.go` та `internal/tmdb/search.go`)

- **Анти-галюцинаційний фільтр року:** Пом'якшено контроль року від Gemini. Якщо Gemini повертає рік, що відрізняється від `parsed.Year` більше ніж на 1 рік, система більше **не перезаписує** його жорстко роком з файлу. Натомість вона логує попередження та дозволяє TMDB спробувати знайти фільм з роком від Gemini. (Захистом виступає жорстка перевірка `TitleSimilarity >= 0.85` на наступному кроці).
- **Подвійна перевірка назви (Post-verify):** Для неангломовних фільмів `tmdbInfo.TitleEN` насправді містить оригінальну назву (наприклад, корейську "기생충"), через що порівняння з `rec.ENTitle` ("Parasite") давало Similarity ~0 і блокувало правильні результати. Тепер `searchAndFetch` зберігає локалізовану англійську назву в `tmdbInfo.SearchTitle`, і алгоритм звіряє Jaro-Winkler з обома варіантами.
- **Логіка перекладу (`needsTranslation`):** Видалено "мертвий код" (блок перевірки `hasCyrillic && !hasRussianLetter`), який блокував переклад кириличних назв без специфічних російських маркерів ('ы', 'э'). Тепер будь-яка кирилична назва, яка не містить суто українських літер ('і', 'ї', 'є', 'ґ'), буде відправлена до Gemini на переклад.
- **Маппінг файлів від Gemini:** Ключі для співставлення результатів від `RecognizeBulk` з іменами файлів тепер приводяться до нижнього регістру (`strings.ToLower()`). Це захищає від випадків, коли LLM змінює регістр у назві (наприклад, з `.MKV` на `.mkv`).
- **UpdateMovie:** При ручному розпізнаванні з текстовою підказкою (напр. "hint"), парсер `FileRecognitionContextFromPath` тепер працює тільки з чистим іменем файлу, а підказка дописується лише у поле `OriginalFile` для відома Gemini. Це запобігає збоям `go-ptn` при наявності року в підказці.

---

## Ревізія 6 — Точність ідентифікації та оптимізація ШІ

### 1. Маппінг Gemini через ID (Захист від галюцинацій)

- Замість маппінгу результатів Gemini за полем `original_file` (яке могло бути спотворене ШІ), запроваджено жорсткий цілочисельний `ID`.
- Схема генерації Gemini очищена від невикористовуваних полів (`title_ua`, `plot`, `genres`, `cast`), що економить токени. Поле `confidence` зроблено обов'язковим.

### 2. Relative Path як первинний ключ бази даних

- Для вирішення проблеми конфліктів імен (коли різні серіали мають `S01E01.mkv`), `app.go` тепер використовує відносний шлях (`Relative Path`) до файлу в межах медіатеки.
- Збережено зворотну сумісність: відносний шлях просто записується в існуючу колонку `filename`, усуваючи необхідність міграції БД та фронтенду. `filepath.Base` замінено на `a.getFileIdentifier(path)`.

### 3. Уніфікація ключів L1-кешу

- `searchCache` тепер скрізь використовує структуру `SearchCacheKey` замість різних способів форматування (рядків та структур).

### 4. Alias-Aware Post-Verify

- Коли `scoreResult` знаходить кращий збіг завдяки альтернативній назві (аліасу), цей аліас зберігається в `MatchedAlias`.
- `mergeGeminiWithTMDB` тепер звіряє Jaro-Winkler не тільки з `TitleEN` і `SearchTitle`, але й з `MatchedAlias`, що запобігає хибним відхиленням правильних розпізнавань, знайдених за аліасами.

---

## Ревізія 7 — Виправлення помилок та оптимізації

### 1. Захист `isScanning` у `FixSelected`

- Додано перевірку та встановлення прапорця `a.isScanning` під `a.scanMutex` на початку `FixSelected`, а також скидання прапорця у `defer` блоці. Це запобігає одночасному запуску сканування та виправлення.

### 2. Запобігання висячим горутинам у `FixSelected`

- Перед викликом `a.setScanCancel(cancel)` у `FixSelected` додано перевірку, чи не встановлено вже попередній `a.scanCancel`, і якщо так — викликається попередній cancel для запобігання сиротливим горутинам.
- `setScanCancel` тепер виконує старий `scanCancel()` під тим самим `a.scanMutex.Lock()` перед збереженням нового `cancel`, щоб скасування і оновлення було атомарним.

### 3. Виправлення шляху у `FixSelected`

- Замінено `geminiQueue = append(geminiQueue, filename)` на `geminiQueue = append(geminiQueue, filepath.Join(a.cfg.MediaFolderPath, filename))` щоб `processGeminiQueue` отримував повний шлях, як і в `RunScan`.

### 4. Виправлення `singleflight closure` у `fetchAIModels`

- Замінено `http.NewRequestWithContext(ctx, "GET", url, nil)` на `http.NewRequestWithContext(context.WithoutCancel(ctx), "GET", url, nil)` щоб скасування scan-ctx не переривало глобальний кеш моделей для Wails-binding.

### 5. Очищення L2 кешу в `CleanMissingMovies`

- Додано видалення записів з таблиці `ai_resolutions` при очищенні відсутніх файлів: `DELETE FROM ai_resolutions WHERE original_filename = ?`

### 6. Оптимізація `GetMoviesByFilenames`

- Додано метод `GetMoviesByFilenames(ctx context.Context, filenames []string) (map[string]Movie, error)` з запитом `WHERE filename IN (?)` для масового отримання фільмів за списком імен файлів.

### 7. Використання `GetMoviesByFilenames` у `processTranslationQueue`

- Замінено цикл з N викликами `GetMovieByFilename` на один виклик `a.db.GetMoviesByFilenames(ctx, filenames)`. Заповнюється `movieMap` з результату. Прибрано `a.emitProgress` всередині фільтрувального циклу (він вже є у основному циклі батчів).

### 3. Передача `ctx` у `filterUnprocessed`

- Змінено сигнатуру та реалізацію методу `filterUnprocessed`, який тепер приймає `ctx context.Context` та використовує його при виклику `a.db.GetAllMovies(ctx)`. Також оновлено всі виклики у `RunScan`.

### 4. Передача `ctx` у `finalizeScan`

- Змінено сигнатуру та реалізацію методу `finalizeScan`, який тепер приймає `ctx context.Context` та використовує його при виклику `a.db.GetAllMovies(ctx)`. Оновлено всі місця виклику у `RunScan` та `FixSelected`.

### 5. Керування ресурсом файлу логів у `logger.go`

- Перенесено дескриптор `logFile` на рівень пакету (`var logFile *os.File`), а у функцію `CloseLogger` додано його скидання на диск (`Sync()`) та закриття (`Close()`) для уникнення витоку дескрипторів файлів.

### 6. Очищення дренування тіла в `client.go`

- Прибрано ручний виклик `io.Copy` після `Decode` у методі `doRequest`, оскільки `defer resp.Body.Close()` вже надійно дренує та закриває з'єднання.
- `requestWithRetry` тепер повертає `ctx.Err()` при відміні контексту, зберігаючи семантику `context.Canceled` / `context.DeadlineExceeded`.
- `TranslateBulk` використовує `json.Marshal(items)` замість `json.MarshalIndent`, щоб зменшити розмір промпту для Gemini.
- `buildPrompt` більше не містить мертвих інструкцій про `title_ua`, `plot`, `genres` та `cast`, оскільки ці поля відсутні у `buildGenAISchema`.
- `extractTMDBID` тепер використовує package-level regexp змінні `reTMDBURL` і `reTMDBID` замість компіляції всередині виклику.
- `storage.New` встановлює `conn.SetMaxOpenConns(1)` для SQLite, щоб уникнути конкурентних з'єднань.
- `InitSchema` використовує `QueryRowContext` і `Scan(&mode)` для підтвердження активації `PRAGMA journal_mode = WAL`.

### 7. Обробка помилок ітерації rows у `CleanMissingMovies`

- Додано перевірку `rows.Err()` після завершення циклу `rows.Next()` у методі `CleanMissingMovies` для вчасного виявлення збоїв під час читання результатів запиту.
- Додано перевірку `rows.Err()` у `GetAllFilenames` для правильного оброблення помилок ітерації SQL-рядків.
- Додано перевірку `rows.Err()` у `CleanOrphanPosters` для вчасного виявлення помилок при читанні `movies` перед скануванням постерів.

### 8. Роздільне виконання PRAGMA-запитів в `InitSchema`

- Винесено налаштування SQLite `PRAGMA journal_mode`, `PRAGMA synchronous` та `PRAGMA busy_timeout` з основного багаторядкового SQL-скрипту створення таблиць в окремі виклики `ExecContext` з індивідуальною перевіркою помилок.

### 9. Логування відсутніх обов'язкових env-змінних

- У `getEnvRequired` замінено `log.Panicf` на структурований `slog.Error("missing_required_env", slog.String("key", key))` з подальшим `panic`, щоб помилка потрапляла у JSONL-лог перед аварійним завершенням.

### 10. Компактний JSON у Gemini recognition prompt

- У `buildPrompt` замінено `json.MarshalIndent(contexts, "", "  ")` на `json.Marshal(contexts)`, щоб не витрачати вхідні токени на пробіли та відступи.

### 11. Очищення FIELD RULES у Gemini recognition prompt

- З `FIELD RULES` у `buildPrompt` прибрано описи `title_ua`, `plot`, `genres` і `cast`, оскільки ці поля не входять до `buildGenAISchema`.

### 12. Захист від nil lastErr у `TranslateBulk`

- Перед фінальним `fmt.Errorf` у `TranslateBulk` додано fallback `errors.New("невідома помилка")`, якщо каскад моделей завершується без конкретної помилки.

### 13. Повторне використання HTTP-клієнта для `GetAIModels`

- `App` отримав поле `aiModelsHTTPClient`, яке ініціалізується в `NewApp` і використовується в `GetAIModels` замість створення нового `http.Client` на кожен запит.

### 14. Пом'якшення сірої зони у `needsTranslation`

- У гілці без специфічних українських літер `needsTranslation` тепер повертає `true` лише для рядків довших за 5 символів; короткі кириличні назви без російських маркерів не відправляються на переклад.

### 15. Документування partial save у `SaveMoviesBatch`

- У `SaveMoviesBatch` додано коментар `// Partial save: errors per-row are logged but batch commit succeeds`, який фіксує рішення комітити batch навіть після per-row помилок.

### Крок 1 — Контекстне скасування в RunScan

- [app.go / RunScan] Використано контекст `ctx` замість `a.ctx` для викликів `CleanMissingMovies` та `CleanOrphanPosters`, що повертає коректне припинення операцій при ручному скасуванні сканування.

### Крок 2 — Перейменування getAIModels → fetchAIModels

- [app.go / GetAIModels, fetchAIModels] Перейменовано приватний метод getAIModels на fetchAIModels. Оновлено всі виклики: GetAIModels делегує до fetchAIModels(a.ctx), RunScan використовує fetchAIModels(ctx).

### Крок 3 — rows.Err() у GetAllMovies

- [internal/storage/storage.go / GetAllMovies] Додано перевірку rows.Err() після циклу for rows.Next() для виявлення помилок ітерації.

### Крок 4 — Винесення regexp у package-level змінну

- [internal/tmdb/client.go / DownloadPoster] Винесено `regexp.MustCompile(\`[^\w\-]\`)` у package-level змінну `reInvalidFilenameChars`. Замінено inline компіляцію на використання змінної.

### Крок 5 — Оптимізація GetStats

- [app.go / GetStats] Замінено `a.db.GetAllMovies(a.ctx)` на два окремі SQL-запити через `db.db.QueryRowContext`:
  `SELECT COUNT(*) FROM movies` → `total`
  `SELECT COUNT(*) FROM movies WHERE tmdb_id = 0` → `unrec`
- [internal/storage/storage.go / GetStatsCounts] Додано приватний метод `func (db *DB) GetStatsCounts(ctx context.Context) (total, unrec int, err error)` для оптимізації запитів.

### Крок 1 — Shadowing у `needsTranslation`

- [app.go / needsTranslation] Перевірено та підтверджено, що локальні змінні вже названі `foundCyrillic`, `foundRussianLetter`, `foundUkrainianLetter`; додаткових змін у коді не потрібно.

### Крок 2 — Повний шлях у UpdateMovie

- [app.go / UpdateMovie] Перевірено, що виклик mergeGeminiWithTMDB вже отримує filepath.Join(a.cfg.MediaFolderPath, filename). Додаткових змін у коді не потрібно.

### Крок 3 — Логування помилок SaveMoviesBatch

- [app.go / processGeminiQueue] Замінено два виклики `_ = a.db.SaveMoviesBatch(...)` на перевірку `if err := a.db.SaveMoviesBatch(...); err != nil { slog.Error("batch_save_failed", slog.Any("error", err)) }`. Виклик у RunScan вже був у правильному вигляді.

### Крок 4 — Уточнення FIELD RULES для media_type

- [internal/ai/gemini.go / buildPrompt] Оновлено пункт 3 у секції FIELD RULES: prompt тепер прямо каже використовувати `parsed_media_type` з input, а за його відсутності обирати `tv` лише для явних маркерів серіалу `(S01, Season N)`, інакше `movie`.

### Крок 5 — Прибрано зайвий progress у фільтрі перекладу

- [app.go / processTranslationQueue] Видалено `a.emitProgress(i+1, len(filenames), "🔄 Перевірка тексту: "+fname)` з початкового фільтрувального циклу перед батч-обробкою, щоб не генерувати зайвий IPC-трафік.

### Крок 6 — Chunk-логіка для GetMoviesByFilenames

- [internal/storage/storage.go / GetMoviesByFilenames] Додано chunk-обробку по 500 filename за запит, мердж результатів у спільну `map[string]Movie` та `slog.Warn("large_filenames_batch", slog.Int("count", len(filenames)))` для великих пакетів.

### Крок 7 — Логування batch-save у перекладі

- [app.go / processTranslationQueue] Замінено `_ = a.db.SaveMoviesBatch(ctx, moviesToSave)` на перевірку з `slog.Error("translation_batch_save_failed", slog.Any("error", err))`, щоб помилки збереження після `TranslateBulk` не губилися.

### Крок 8 — Перевірка помилки WalkDir

- [internal/scanner/scanner.go / getFirstVideoInDir] `filepath.WalkDir` обгорнуто в `if err := ...; err != nil` з `slog.Warn("walkdir_error", slog.String("dir", dirPath), slog.Any("error", err))`, щоб не губити помилки обходу директорії.

### Крок 9 — Closure для rows у GetMoviesByFilenames

- [internal/storage/storage.go / GetMoviesByFilenames] Тіло кожної chunk-ітерації перенесено в локальну closure з `defer rows.Close()` та `return rows.Err()`, щоб закриття `rows` гарантовано відбувалося на кожному проході циклу.

### Крок 10 — Перехід sheets на slog

- [internal/sheets/sheets.go / NewClient, SyncMovies, retry] Усі `log.Printf` і `log.Println` замінено на `slog.Info` / `slog.Warn` / `slog.Error` зі структурованими атрибутами; імпорт `log` прибрано.

### Крок 11 — Логування завантаження .env через slog

- [internal/config/config.go / Load] `log.Printf` для успішного завантаження `.env` замінено на `slog.Info("env_loaded", slog.String("path", envPath))`; імпорт `log` прибрано.

### Крок 12 — Фінальний progress при зупинці Gemini

- [app.go / processGeminiQueue] У гілку `if ctx.Err() != nil` перед `break` додано `a.emitProgress(total, total, "🛑 Зупинено")`, щоб UI коректно завершував progress-bar при ручній зупинці.


### Крок 13 — UTF-8 рядок зупинки у Gemini queue

- [app.go / processGeminiQueue] Замінено пошкоджений виклик a.emitProgress(...) на коректний UTF-8 текст 🛑 Зупинено і вирівняно відступ трьома табами.

### Крок 14 — UTF-8 коментар у SaveMoviesBatch

- [internal/storage/storage.go / SaveMoviesBatch] Замінено пошкоджений коментар над методом на коректний UTF-8 текст про масовий запис через єдину транзакцію.

### Крок 15 — Логування помилки видалення AI resolution

- [app.go / DeleteMovie] Замінено ігнорування помилки DeleteAIResolution на slog.Warn з іменем файлу та об'єктом помилки.

### Крок 16 — Правила кодування та відступів у .cursorrules

- [.cursorrules] Додано явні вимоги зберігати вихідні файли у UTF-8 без BOM і використовувати таби для відступів у Go-коді за стандартом gofmt.
