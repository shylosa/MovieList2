---
Creator: Serhii Shylo
Tags: Go, Golang, Wails, JavaScript, Movie Library, TMDB, Gemini AI, Grok AI, SQLite, Local Media
Requires at least: Go 1.26.2+, Node.js (для збірки), TMDB API Key, Gemini API Key
License: MIT License
Version: 2.1.0
---

# 🍿 MovieList 2.1 — Менеджер локальної медіатеки

**MovieList 2.1** — високопродуктивний десктопний додаток на **Go 1.26.2 та Wails v2** для перетворення хаотичних папок із відеофайлами на структурований кінокаталог. Програма автоматично збирає метадані (постери, описи, рейтинги), підтримує хмарну синхронізацію та генерує статичну HTML-вітрину для перегляду на мобільних пристроях.

---

## ✨ Ключові можливості

### 🧠 Дворівневий AI-каскад (Gemini + Grok)

Система розпізнавання назв відеофайлів використовує каскад із двох AI-провайдерів:

- **Gemini** (Flash → Pro → Lite) — основний провайдер із автоматичним перемиканням між моделями.
- **Grok** (`grok-3-mini`, конфігурується через `GROK_MODEL`) — повноцінний резервний бекенд:
  - **primary**: активується одразу при блокуванні квоти Gemini (`geminiQuotaLocked`), оминаючи увесь каскад.
  - **last-resort**: підхоплює після вичерпання всіх Gemini-моделей.
  - Rate limiter: 30 RPM, burst=1.
- На початку кожного сканування квота Gemini скидається (`ResetQuotaLock`), тому відновлена квота буде використана в наступній сесії.

### 🔍 Pipeline розпізнавання (11 кроків)

1. Парсинг імені файлу (`go-ptn` + власні regex: `reNakedLang`, `reLangTag`)
2. Пошук за IMDB ID (якщо присутній у назві)
3. Типізований пошук TMDB (`/movie` або `/tv`, ніколи `/multi`)
4. Транслітераційний fallback (кирилиця → латиниця)
5. Fallback за батьківською папкою (generic-назви фільтруються)
6. Скоринг та верифікація через Jaro-Winkler
7. Водоспад метаданих: `uk-UA → ru-RU → en-US`
8. Merge TMDB + Gemini (Gemini пропускається якщо TMDB вже дав валідний кириличний `TitleUA`)
9. Batch-збереження через `SaveMoviesBatch` (єдина транзакція)
10. Черга Gemini-розпізнавання (нерозпізнані файли)
11. Черга перекладу (локалізація через AI)

### 📁 Розумний FileLabel для UI

Замість технічного відносного шляху (`Breaking Bad/Season 1/S01E01.mkv`) інтерфейс показує людино-читаний лейбл:

| Тип | Відображення |
|-----|-------------|
| Фільм | `Dune.mkv` (basename) |
| Серіал | `Breaking Bad` (перша папка) |
| Нерозпізнаний | евристика через `S\d{2}E\d{2}` / `Season \d+` |

Повний шлях зберігається як `title`-tooltip і залишається незмінним primary key у БД.

### 🌐 Два режими HTML-вітрини

| Файл | Призначення | Постери |
|------|-------------|---------|
| `local_index.html` | Офлайн-перегляд на ПК | Локальні (`posters/`) |
| `index.html` | GitHub Pages (мобільний) | TMDB CDN |

`index.html` публікується через `SyncToGitHub` → `git push origin <GITHUB_PAGES_BRANCH>`.

### ⚡ Production-Grade архітектура

- **Shutdown-safe:** `RunScan` і `FixSelected` відстежуються через `a.wg` → `db.Close()` викликається тільки після повного завершення всіх горутин.
- **SQLite WAL:** `journal_mode=WAL`, `busy_timeout=5000`, `SetMaxOpenConns(1)`.
- **Batch DB:** `SaveMoviesBatch` — єдина транзакція на batch, `filenameChunkSize=500` для `IN` запитів.
- **Structured logging:** `log/slog` у форматі JSON із Trace ID на кожен файл.
- **Кешування (L1/L2):** L1 — `sync.Map` для TMDB-результатів у межах сесії; L2 — SQLite `ai_resolutions` для Gemini-рішень між сесіями.

---

## 🗄️ Структура SQLite

За замовчуванням база зберігається у `movies.db` (шлях налаштовується через `DB_PATH`). Застосунок створює три власні таблиці:

| Таблиця | Призначення | Первинний ключ |
|---------|-------------|----------------|
| `movies` | Основний каталог локальних файлів і перевірених метаданих TMDB | `filename` |
| `ai_resolutions` | L2-кеш результатів Gemini для повторного TMDB-пошуку без нового AI-запиту | `original_filename` |
| `app_state` | Службові значення стану застосунку у форматі `key → value` | `key` |

### `movies`

Один рядок відповідає одному локальному медіафайлу. `filename` містить повний відносний шлях і є єдиним стабільним ідентифікатором запису. Таблиця також зберігає `tmdb_id`, тип `movie`/`tv`, українську й оригінальну назви, рік, жанри, акторів, опис та шляхи до постера.

- `tmdb_id > 0` — запис перевірено через TMDB і він вважається розпізнаним.
- `tmdb_id = 0` — unresolved placeholder, який показується в «Потребує уваги» і не губиться між скануваннями.
- Поле `Movie.ID` у Go читається зі SQLite `rowid`, але не є зовнішнім ідентифікатором і не повинно замінювати `filename`.
- Merge-upsert не дозволяє нерозпізнаному результату стерти назву та рік уже підтвердженого запису.

З `movies` формуються список у UI, статистика, локальна й мобільна HTML-вітрини та дані для Google Sheets.

### `ai_resolutions`

Зберігає розпізнану Gemini назву, рік, media type і confidence за ключем `original_filename`. При повторній обробці програма може використати цей результат як кешовану пошукову підказку, але все одно перевіряє її через TMDB: сам запис у кеші не робить файл розпізнаним і не є джерелом `TmdbID`.

Під час видалення медіазапису через UI або очищення відсутніх на диску файлів відповідний AI-кеш також видаляється. Foreign key між таблицями немає; узгодженість підтримує storage layer за значенням filename.

Кеш версіонується номером recognition pipeline. Записи іншої версії вважаються cache miss, а не помилкою; успішний AI→TMDB результат зберігає provider, model і час оновлення. Ручне підтвердження IMDb/TMDB або кандидата інвалідує попередню AI-підказку.

### `app_state`

Містить невеликі службові параметри. Зараз використовується ключ `last_scan_at` — час останнього **успішного** сканування. Помилка або зупинка користувачем не оновлює це значення.

### Перевірка розпізнавання

У редакторі ручна підказка працює разом із selector `Авто / Фільм / Серіал`. Кнопка `Знайти варіанти` показує до п’яти детерміновано впорядкованих TMDB-кандидатів (назва, original title, рік, тип та ID) без постерів. Повні details і постер завантажуються лише після дії `Обрати`.

`Нерозпізнаний` означає `tmdb_id = 0`. `Сумнівний` уже має перевірений TMDB ID, але потребує перегляду; ці стани рахуються окремо. Ручне підтвердження очищає стан сумнівності.

Для неоднозначних кандидатів дія `Уточнити` за запитом показує легкий popover із TMDB CDN-постером, точною датою, тривалістю, жанрами, описом, рейтингом і кількістю голосів. Дані кешуються на сесію; credits, aliases і локальне завантаження постера не виконуються. Фільми до 40 хв позначаються як короткометражні.

`movies.vote_average` і `movies.vote_count` зберігають рейтинг TMDB. Він відображається в редакторі, локальній та мобільній Вітрині й синхронізується з Google Sheets. Перший scan після оновлення дозаповнює рейтинг старих розпізнаних записів одним batch save; наступні scan пропускають уже заповнені записи.

Gemini використовує задані у `GEMINI_MODELS` production-моделі у вказаному порядку та discovered `generateContent` моделі. Типовий каскад: `gemini-2.5-flash,gemini-flash-lite-latest`; після вичерпання Gemini доступний послідовний Grok fallback.

### Індекси та службові файли

- `idx_tmdb_id` прискорює вибірки за TMDB ID і підрахунок unresolved-записів.
- `idx_title_en` індексує оригінальну/англійську назву.
- `movies.db-wal` і `movies.db-shm` — службові файли SQLite WAL, а не додаткові таблиці; їх не слід видаляти під час роботи програми.

---

## 📦 Стандартні можливості

- 🇺🇦 Пріоритет українських даних з TMDB та AI-переклад описів
- 🧹 Захист від AI-downgrade: заповнений `TmdbID > 0` не перезаписується нерозпізнаним записом
- ☁️ Google Sheets sync (`SyncToCloud`)
- 🎨 Адаптивна HTML-вітрина (мобільний та десктопний режими)
- 🔒 API-ключі тільки через `.env`, маскування в логах

---

## 🛠️ Вимоги для збірки

- **Go 1.26.2+**
- **Node.js 18+ та npm**
- **Wails CLI:** `go install github.com/wailsapp/wails/v2/cmd/wails@latest`

**API ключі:**

- [TMDB API Key](https://www.themoviedb.org/settings/api) (v3)
- [Google Gemini API Key](https://aistudio.google.com/)
- [Grok API Key](https://console.x.ai/) — опційно, для резервного AI-розпізнавання
- `credentials.json` (Google Cloud) — для Google Sheets sync

---

## 🚀 Швидкий старт

### Для користувачів

1. Завантажте `movielist-app.exe`.
2. Створіть `.env` (шаблон нижче).
3. Запустіть додаток.

### Для розробників

```bash
git clone https://github.com/shylosa/movielist2.git
cd movielist2
# Налаштуйте .env
wails dev    # Режим розробки
wails build  # Компіляція
```

### Шаблон `.env`

```env
# Обов'язкові
TMDB_API_KEY=your_key
GEMINI_API_KEY=your_key
MEDIA_FOLDER_PATH=/path/to/your/media

# AI (опційно)
GROK_API_KEY=your_key          # Резервний AI-провайдер
GROK_MODEL=grok-3-mini         # Модель Grok (default: grok-3-mini)
GEMINI_MODELS=gemini-2.5-flash,gemini-flash-lite-latest

# Збереження
DB_PATH=movies.db
HTML_PATH=local_index.html
POSTERS_DIR=posters

# GitHub Pages (опційно)
GITHUB_PAGES_BRANCH=main       # Гілка для публікації (default: main)
EXCLUDE_FOLDERS=downloads,temp # Папки для ігнорування при скануванні
```

---

## 🧪 Тести

```bash
go test ./...          # Всі тести
go test ./... -v       # З детальним виводом
go test ./... -cover   # З покриттям коду
```

**Тестові файли:**

| Файл | Що тестує |
|------|-----------|
| `internal/tmdb/parser_test.go` | Парсинг імен файлів, homoglyph-заміна |
| `internal/tmdb/search_test.go` | Скоринг, buildAttempts, generic folder filter |
| `internal/tmdb/translit_test.go` | Транслітерація кирилиці |
| `internal/ai/gemini_test.go` | TTS-фільтр, Gemini cascade, Grok fallback при quota lock |
| `internal/ai/grok_test.go` | HTTP happy path, ReasoningEffort, error handling |
| `internal/storage/storage_test.go` | Upsert, CleanMissing, відносні шляхи як PK |
| `internal/utils/path_display_test.go` | DisplayFileLabel (movie/tv/unresolved) |
| `app_getaimodels_test.go` | Gemini models, Grok-only mode, no keys |
| `app_updatemovie_test.go` | Bypass Gemini при валідному кириличному TMDB |

---

## 📁 Архітектура

```
├── main.go                 # Wails bootstrap, global panic handler
├── app.go                  # Orchestrator (Wails API, scan lifecycle, shutdown)
└── internal/
    ├── ai/                 # Gemini + Grok (cascade, quota lock, rate limiting)
    ├── config/             # .env завантаження
    ├── scanner/            # Disk I/O, path filtering
    ├── storage/            # SQLite (WAL, upsert, L2 AI cache, PatchMovie)
    ├── tmdb/               # TMDB client (L1 cache, scoring, transliteration)
    ├── sheets/             # Google Sheets sync
    ├── utils/              # slog JSON logger, Trace context, FileLabel, Cyrillic helpers
    └── web/                # HTML generator (local + mobile showcase)
```

📜 **Ліцензія:** MIT. Створено з любов'ю до кіно та чистого коду.
