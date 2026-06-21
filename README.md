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
GEMINI_MODELS=gemini-2.5-flash,gemini-2.0-flash,gemini-2.5-flash-lite

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
