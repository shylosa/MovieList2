---
Creator: Serhii Shylo
Tags: Go, Golang, Wails, JavaScript, Movie Library, TMDB, Gemini AI, SQLite, Local Media
Requires at least: Go 1.26.2+, Node.js (для збірки), TMDB API Key, Gemini API Key
License: MIT License
Version: 2.1.0
---

# 🍿 MovieList 2.0 - Менеджер локальної медіатеки

**MovieList 2.0** — це високопродуктивний десктопний додаток на **Go (Golang 1.26+) та Wails v2**, створений для перетворення хаотичних папок із відеофайлами на структурований кінокаталог. Програма автоматично збирає метадані (постери, описи, рейтинги) та підтримує хмарну синхронізацію.

Завдяки переходу на сучасний стек Go, додаток працює блискавично, має нульові зовнішні залежності при запуску та використовує передові AI-технології для розпізнавання медіа.

## ✨ Головні інновації та стабільність

* **⚡ High-Performance Go Engine:** Використання конкурентності Go 1.26, оптимізованих пулів воркерів та семафорів для паралельної обробки тисяч файлів без падінь.
* **🧠 Дворівневий AI Каскад (Gemini):** Розумна система розпізнавання, що автоматично перемикається між моделями (Flash/Pro) для досягнення 100% точності при мінімальних витратах.
* **🛡️ Production-Grade Observability:** Повний перехід на структуроване логування (`log/slog`) у форматі JSON із підтримкою Trace ID для відстеження життєвого циклу кожного файлу.
* **🚀 Смарт-Кешування (L1/L2):**
  * **L1 (In-memory):** Миттєвий доступ до результатів пошуку TMDB у межах сесії.
  * **L2 (SQLite):** Персистентне зберігання AI-рішень для уникнення повторних платних запитів до Gemini.
* **🔒 Security First:** Безпечне керування API-ключами через заголовки (`x-goog-api-key`), маскування чутливих даних у логах та захист від витоків пам'яті (Timer management).
* **🧹 Автоматична Гігієна:** Примусове очищення кешів на початку сканування та автоматичне видалення AI-галюцинацій при видаленні фільму користувачем.

## 📦 Можливості, що стали стандартом

* **🇺🇦 Повна UA локалізація:** Пріоритет українських даних з TMDB та інтелектуальний переклад описів через ШІ.
* **🧠 Надійна Токенізація:** Використання `go-ptn` для точного виокремлення назви, року та якості (1080p, 4K, REMUX) з імен файлів.
* **🎨 Офлайн HTML-вітрина:** Генерація статичного SPA-каталогу для перегляду колекції в будь-якому браузері без інтернету.
* **☁️ Google Sheets Sync:** Миттєва синхронізація вашої бібліотеки з хмарою для доступу з мобільних пристроїв.

---

## 🛠️ Вимоги до системи (Для збірки)

* **Go** (версії 1.26.2 або новіше).
* **Node.js 18+ та npm**.
* **Wails CLI** (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`).

**API Ключі:**

* [TMDB API Key](https://www.themoviedb.org/settings/api) (v3 auth).
* [Google Gemini API Key](https://aistudio.google.com/) (для AI розпізнавання).
* `credentials.json` (Google Cloud) — для синхронізації з таблицями.

---

## 🚀 Швидкий старт

### Для користувачів

1. Завантажте `movielist-app.exe`.
2. Створіть `.env` файл (див. шаблон нижче).
3. Запустіть додаток.

### Для розробників

```bash
git clone https://github.com/shylosa/movielist2.git
cd movielist2
# Налаштуйте .env
wails dev # Запуск у режимі розробки
wails build # Компіляція бінарного файлу
```

### Шаблон .env

```env
TMDB_API_KEY=your_key
GEMINI_API_KEY=your_key
GEMINI_MODELS=gemini-2.0-flash,gemini-1.5-flash # Каскад моделей
DB_PATH=./movies.db
POSTERS_DIR=./posters
```

### 🧪 Запуск тестів

Проект включає набір юніт-тестів для перевірки функціональності основних модулів.

**Запуск всіх тестів:**

```bash
go test ./...
```

**Запуск тестів з детальним виводом:**

```bash
go test ./... -v
```

**Запуск тестів конкретного модуля:**

```bash
go test ./internal/tmdb -v          # Тести TMDB клієнта
go test ./internal/ai -v            # Тести Gemini AI
```

**Запуск тестів з покриттям коду:**

```bash
go test ./... -cover
```

**Генерація звіту про покриття:**

```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out    # Відкриє звіт у браузері
```

**Основні тестові файли:**

* `internal/tmdb/parser_test.go` — тести парсингу даних з TMDB
* `internal/tmdb/search_test.go` — тести функціональності пошуку
* `internal/tmdb/translit_test.go` — тести транслітерації
* `internal/ai/gemini_test.go` — тести Gemini AI клієнта

---

## 📁 Архітектура

```bash
├── main.go             # Entry Point (Wails Config, Window options)
├── app.go              # Orchestrator (Wails Bridge, Scan Logic)
├── internal/
│   ├── ai/             # Gemini Client (Retry logic, Cascading, Rate limiting)
│   ├── tmdb/           # TMDB Client (L1 Cache, Search fallbacks, Transliteration)
│   ├── storage/        # SQLite Layer (Migrations, L2 AI Cache, Coalesce logic)
│   ├── scanner/        # Disk I/O (Slog integrated, Path filtering)
│   ├── utils/          # Logger (JSON), Trace Context, UI Helpers
│   └── web/            # HTML Generator (Static SPA Showcase)
└── frontend/           # Vite + Vanilla JS (Reactive UI, State Management)
```

📜 **Ліцензія:** MIT. Створено з любов'ю до кіно та чистого коду.
