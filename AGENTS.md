# AGENTS.md — MovieList App

## Project Overview
MovieList App — desktop application for cataloging local movie/TV collections.

Tech stack: Go 1.26+, Wails v2, SQLite, TMDB API, Google Gemini (`google.golang.org/genai`).
*Note on toolchain: go 1.26.2 in go.mod — verify this is the intended toolchain version. Latest stable as of audit date is 1.24.x. If using pre-release, document why.*

---

## Architecture

### Core Files
* `main.go` — Wails bootstrap, global panic handler
* `app.go` — Main orchestration layer and Wails API
* `internal/ai/` — Gemini & Grok integration
* `internal/config/` — .env loading
* `internal/scanner/` — File scanning
* `internal/storage/` — SQLite layer
* `internal/tmdb/` — Parsing, search, scoring, metadata
* `internal/sheets/` — Google Sheets sync
* `internal/utils/` — Logging/system utilities
* `internal/web/` — Static showcase generator

### Recognition Pipeline
**Strict execution order:**
1. Filename parsing
2. IMDB ID lookup
3. Typed TMDB search (movie / tv)
4. Transliteration fallback
5. Parent directory fallback
6. TMDB scoring and verification
7. Metadata waterfall (uk-UA → ru-RU → en-US)
8. Merge TMDB + Gemini (Bypass Gemini if TMDB has valid Cyrillic `TitleUA`)
9. SQLite batch save
10. Gemini fallback queue
11. Translation queue

### AI Agent Priorities
1. Correct recognition
2. Deterministic behavior
3. Cancellation responsiveness
4. Minimal API usage
5. Throughput/performance

---

## Critical Invariants

* **Recognition:** `TmdbID > 0` is the only valid "recognized" state.
* **Trust:** Gemini-generated IDs are **never** trusted directly.
* **Language Cascade:** Always preserve: `uk-UA → ru-RU → en-US`.
* **TMDB Search:** Use `/search/movie` or `/search/tv`. **Never** use `/search/multi`.
* **Translation Queue:** Only verified entries may be translated. If TMDB provides an official Cyrillic title, **bypass Gemini completely**.
* **Batch Persistence:** Always use transactional `SaveMoviesBatch()`. Never replace with N individual saves.
* **Cancellation:** Always check `ctx.Err()` before network requests and at loop boundaries.

---

## AI Rules (Gemini & Grok)

* **Gemini SDK:** Use only `google.golang.org/genai`. Never use raw HTTP clients.
* **Execution:** Gemini pipeline is intentionally sequential (batch size = 5/10) to respect RPM limits and shared rate limiters. Do not introduce aggressive parallelism.
* **Model Loading:** Uses `singleflight.Group`. Do not duplicate concurrent requests.
* **Grok Fallback:** `grok-3-mini` is used strictly as a last-resort fallback. It has no rate limiter. Reasoning tokens must remain disabled (`ReasoningEffort: "none"`). Never wrap JSON in markdown.
* **Translation Safety:** If official Ukrainian title is unknown, preserve original title (do not hallucinate localization).

---

## Scoring Rules

* Exact match: `+200` | Contains match: `+70`
* Exact year: `+150` | Year ±1: `+80` | Year > ±2: `-400`
* Media type match: `+30` | UA lang: `+20` | EN lang: `+10`
* RU recent: `-50` | RU old: `-300`
* **Hard rejection:** `titleScore == 0` → reject. Default threshold: `200`.
* **AI Verification:** Use Jaro-Winkler similarity (`geminiTMDBVerifyMinJW`) to reject AI results that deviate significantly from official TMDB titles.

---

## Transliteration
Runs only after failed direct search. Digraphs must be processed before single-character replacements.
* `Vrag` → `Враг`, `Nochnoj Rejs` → `Ночной Рейс`.

---

## SQLite & Storage
* **Primary key:** `filename` (Uses relative media path as identifier).
* **AI Cache (`ai_resolutions`):** Stores L2 Gemini recognition cache.
* **Pragmas required:** `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`.
* **Connection pool:** `SetMaxOpenConns(1)`.

---

## Performance & Concurrency Rules

**Always:**
* Use `strings.Builder` for large string generation.
* Use package-level regexp variables and `strings.NewReplacer`.
* Use chunking for large SQL `IN` queries (e.g., `filenameChunkSize = 500`).
* Use structured logging (`slog`).

**Never:**
* Compile regexp inside loops.
* Mutate shared maps (`movieMap`) from goroutines after initialization.
* Spam Wails IPC progress events.

---

## Stable Interfaces & Public API

**Do not rename without explicit migration/update:**
* Wails events: `scan-started`, `scan-progress`, `scan-finished`, `log-message`.
* HTML element IDs: `id="noResults"`, `id="filteredCount"`, `id="filteredNum"`.
* Public methods: `RunScan()`, `StopScan()`, `FixSelected()`, `GetMovies()`, `UpdateMovie()`, `SyncToCloud()`, etc.

---

## Encoding & Formatting
* **Required:** UTF-8 without BOM, LF (Unix) line endings, `gofmt` formatting, tabs for Go indentation.
* **Never commit:** Mixed encodings, malformed UTF-8, CRLF endings.

---

## Environment Configuration
```env
APP_VERSION=2.0
GEMINI_API_KEY=
GEMINI_MODELS=gemini-2.5-flash,gemini-2.0-flash,gemini-2.5-flash-lite
GROK_API_KEY=
TMDB_API_KEY=
MEDIA_FOLDER_PATH=
EXCLUDE_FOLDERS=
DB_PATH=movies.db
HTML_PATH=index.html
POSTERS_DIR=posters
```

---

## Changelog

| File | Method | Change |
|------|--------|--------|
| `internal/storage/storage.go` | `PatchMovie`, `SaveMovie`, `SaveMoviesBatch` | Replaced `INSERT OR REPLACE` with merge upsert (`movieUpsertQuery`); `PatchMovie` updates only non-empty fields. |
| `app.go` | `processGeminiQueue` | On batch error or empty recognition: log only; no `SaveMoviesBatch` with filename-only structs. |
| `internal/storage/storage.go` | `SaveMoviesBatch` | Any failed `Exec` aborts the batch; `defer tx.Rollback()` on error paths. |
| `internal/storage/storage.go` | `GetAllMovies` | `rows.Scan` errors propagate via `fmt.Errorf` instead of silent `continue`. |
| `app.go` | `filterUnprocessed` | On `GetAllMovies` error: log and return `nil` to avoid mass rescan. |
| `app.go` | `OpenShowcase`, `SyncToCloud` | Already abort on `GetAllMovies` failure (log + `return`). |
| `app.go` | `RunScan` | Scan body runs in a goroutine; `isScanning` and lifecycle events stay inside it. |
| `app.go` | `RunScan`, `finalizeScan` | Single `scan-finished` emit via `finalizeScan` (`scanFinished` flag prevents double emit). |
| `app.go` | `UpdateMovie`, `updateMovie` | Public `UpdateMovie(filename, hint)` for Wails; `updateMovie(ctx, ...)` for internal callers. |
| `frontend/wailsjs` | `UpdateMovie` | Regenerated via `wails generate module` (no `context.Context` arg). |
| `app.go` | `logFront` | `log_front_fallback` only when `a.ctx == nil` or `EventsEmit` panics. |
| `app.go` | `fetchAIModels` | Safe `[]string` type assertion; warmup goroutine logs failures at Debug. |
| `app.go` | `FixSelected` | Safe type assertions for `filename` / `hint` map fields. |
| `main.go` | `main` | Panic recover logs `debug.Stack()` (already present). |
| `internal/tmdb/client.go`, `internal/ai/grok.go` | HTTP helpers | `defer resp.Body.Close()` after successful `Do` (already present). |
| `internal/config/config.go`, `internal/ai/gemini.go` | `Load`, `getGenaiClient` | `GEMINI_API_KEY` optional at startup; validated before Gemini calls. |
| `internal/ai/gemini.go` | `buildPrompt` | Shorter prompt (3 translit examples); handles `json.Marshal` errors. |
| `app.go`, `internal/ai/gemini.go` | `processTranslationQueue`, `TranslateBulk` | Translation batch size 5; plot omitted when not needed; shorter translate prompt; add instruction to use `original_title` as context. |
| `internal/ai/gemini.go` | `buildGrokRecognitionPrompt` | Compact Grok recognition fallback prompt. |
| `app.go` | `processScanResults`, `movieInfoNeedsTranslation` | Translation queue only for titles/plots that need localization; add context cancellation check and channel draining. |
| `internal/tmdb/search.go` | `searchAndFetch` | Unknown type: sequential `/search/movie` then `/search/tv` (no `/multi`). |
| `app.go` | `runTMDBScan` | Results channel buffer size 10 (matches semaphore pool); make sem write context-aware and handle cancel. |
| `internal/ai/gemini.go` | `buildPrompt` | Change signature to `(string, error)` and restore detailed prompt template. |
| `internal/ai/gemini.go` | `RecognizeBulk` | Handle error returned by `buildPrompt`. |
| `internal/ai/gemini.go` | `requestWithRetry` | Remove `contexts` parameter and use `prompt` directly for Grok. |
| `internal/ai/gemini.go` | `buildGrokRecognitionPrompt` | Delete the obsolete function entirely. |
| `internal/tmdb/client.go` | `doRequestWithRetry` | Replace unreachable return nil after loop with explicit error return. |
| `internal/ai/gemini.go` | `TranslateBulk` | Ignore json.Marshal error with safe-to-ignore comment. |



**CHECKLIST complete (2026-06-01):** Replaced unreachable return nil in doRequestWithRetry, ignored JSON marshal error in TranslateBulk with comment, documented Go 1.26.2 toolchain version note, and verified all build/vet/fmt/test steps pass.
