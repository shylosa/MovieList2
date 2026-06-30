# AGENTS.md — MovieList App

## Project Overview

MovieList App — desktop application for cataloging local movie/TV collections.

**Tech stack:** Go 1.26+, Wails v2, SQLite, TMDB API, Google Gemini (`google.golang.org/genai`), Grok (`grok-3-mini`).

---

## Architecture

### Core Files

| Package | Role |
|---------|------|
| `main.go` | Wails bootstrap, global panic handler |
| `app.go` | Main orchestration layer and Wails API |
| `internal/ai/` | Gemini & Grok integration |
| `internal/config/` | `.env` loading |
| `internal/scanner/` | File scanning |
| `internal/storage/` | SQLite layer |
| `internal/tmdb/` | Parsing, search, scoring, metadata |
| `internal/sheets/` | Google Sheets sync |
| `internal/utils/` | Logging, language helpers, path display |
| `internal/web/` | Static showcase generator |

### Showcase & GitHub Pages

| File | Role |
|------|------|
| `local_index.html` | Local PC showcase (`HTML_PATH` default); posters from `posters/` (`LocalPosterPath`). Gitignored. |
| `index.html` | Mobile showcase for GitHub Pages; TMDB CDN posters (`PosterURL`, `isMobile: true`). Committed via `SyncToGitHub` only. |

* **Deploy trigger:** `SyncToGitHub()` from UI only — no background sync.
* **Git:** System `git` via `os/exec`; no tokens in code. `git rev-parse --show-toplevel` sets `cmd.Dir` and `index.html` output path. `git push origin <cfg.GitHubPagesBranch>` (default: `"main"`, env: `GITHUB_PAGES_BRANCH`).
* **`git commit` exit code:** Non-zero when nothing to commit is **not** treated as failure; `git push` still runs.

### Recognition Pipeline

**Strict execution order:**

1. Filename parsing
2. IMDB ID lookup
3. Typed TMDB search (`/search/movie` → `/search/tv`)
4. Transliteration fallback
5. Parent directory fallback
6. TMDB scoring and verification
7. Metadata waterfall (`uk-UA → ru-RU → en-US`)
8. Merge TMDB + Gemini (bypass Gemini if TMDB has valid Cyrillic `TitleUA`)
9. SQLite batch save
10. Gemini fallback queue
11. Translation queue

### AI Agent Priorities

1. Correct recognition
2. Deterministic behavior
3. Cancellation responsiveness
4. Minimal API usage
5. Throughput / performance

---

## Critical Invariants

* **Recognition:** `TmdbID > 0` is the only valid "recognised" state.
* **Trust:** Gemini-generated IDs are **never** trusted directly.
* **Language Cascade:** Always preserve: `uk-UA → ru-RU → en-US`.
* **TMDB Search:** Use `/search/movie` or `/search/tv`. **Never** use `/search/multi`.
* **Translation Queue:** Only verified entries may be translated. If TMDB provides an official Cyrillic title, **bypass Gemini completely**.
* **Batch Persistence:** Always use transactional `SaveMoviesBatch()`. Never replace with N individual saves.
* **Cancellation:** Always check `ctx.Err()` before network requests and at loop boundaries.
* **Primary Key:** `movies.filename` is the primary key (full relative path). Never use rowid as identifier externally.
* **Unresolved records:** If Gemini fails to recognise a file, save a placeholder with `TmdbID=0` so the record is never silently dropped.

---

## AI Rules (Gemini & Grok)

* **Gemini SDK:** Use only `google.golang.org/genai`. Never use raw HTTP clients.
* **Execution:** Gemini pipeline is intentionally sequential (batch size = 5/10) to respect RPM limits. Do not introduce aggressive parallelism.
* **Model Loading:** Uses `singleflight.Group`. Do not duplicate concurrent requests.
* **Quota Lock:** `quotaLocked atomic.Bool` is a field on `Client` (not a package-level var). Call `ResetQuotaLock()` at the start of each `RunScan`.
* **Grok Fallback:** `grok-3-mini` (configurable via `GROK_MODEL`) activates when `geminiQuotaLocked` is set, and as a last resort after all Gemini models fail. Uses `grokLimiter` (30 RPM, burst=1). Reasoning tokens disabled (`ReasoningEffort: "none"`). Never wrap JSON in markdown.
* **Translation Safety:** If official Ukrainian title is unknown, preserve the original title (do not hallucinate localisation).
* **TMDB Title Trust:** If `movie.TitleUA` is validated as good Ukrainian, Gemini must not override it.

---

## Scoring Rules

| Criterion | Score |
|-----------|-------|
| Exact title match | `+200` |
| Contains match | `+70` |
| Exact year | `+150` |
| Year ±1 | `+80` |
| Year > ±2 | `−400` |
| Media type match | `+30` |
| UA language | `+20` |
| EN language | `+10` |
| RU recent | `−50` |
| RU old | `−300` |

* **Hard rejection:** `titleScore == 0` → reject immediately.
* **Default threshold:** `200`.
* **AI Verification:** Use Jaro-Winkler similarity (`geminiTMDBVerifyMinJW`) to reject AI results that deviate significantly from TMDB titles.
* **UA→RU early exit:** If `uk-UA` yields a candidate with `score >= 140`, skip `ru-RU` query entirely.

---

## Transliteration

Runs only after a failed direct search. Digraphs must be processed **before** single-character replacements.

Examples: `Vrag` → `Враг`, `Nochnoj Rejs` → `Ночной Рейс`.

---

## File Display Label

`movies.filename` remains the primary key (full relative path). UI uses a short `FileLabel` computed by `utils.DisplayFileLabel(relativePath, mediaType)`.

| Media type | Logic |
|------------|-------|
| `movie` | file `basename` |
| `tv` | first folder relative to `MEDIA_FOLDER_PATH` |
| `tv` at root | file `basename` |
| unresolved (`""`) | heuristic via regexp (`S\d{2}E\d{2}`, `Season \d+`, etc.) |

`FileLabel` is **not** stored in SQLite — computed in `GetMovies()` before returning to UI.

---

## SQLite & Storage

* **Primary key:** `filename` (relative media path).
* **AI Cache (`ai_resolutions`):** L2 Gemini recognition cache.
* **Required pragmas:** `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`.
* **Connection pool:** `SetMaxOpenConns(1)`.
* **Upsert:** Always use `movieUpsertQuery` (merge upsert). `PatchMovie` updates only non-empty fields. A record with `tmdb_id=0` must never overwrite `title_ua`/`title_en`/`year` of a record that already has `tmdb_id > 0`.

---

## Performance & Concurrency Rules

**Always:**
* Use `strings.Builder` for large string generation.
* Use package-level regexp variables and `strings.NewReplacer`.
* Use chunking for large SQL `IN` queries (`filenameChunkSize = 500`).
* Use structured logging (`slog`).
* Check `ctx.Err()` at every loop boundary and before network calls.

**Never:**
* Compile regexp inside loops.
* Mutate shared maps (`movieMap`) from goroutines after initialisation.
* Spam Wails IPC progress events.

---

## Shutdown & Lifecycle Safety

* `App.wg sync.WaitGroup` tracks all background goroutines (`RunScan`, `FixSelected`, GitHub Pages push).
* `shutdown()` calls `cancelScan()` **before** `wg.Wait()` so goroutines can exit rather than hang.
* `db.Close()` is called only after `wg.Wait()` completes.
* `finalizeScan` always receives `a.ctx` (not the local scan context) to avoid generating an empty showcase on cancellation.

---

## Stable Interfaces & Public API

**Do not rename without explicit migration:**

* **Wails events:** `scan-started`, `scan-progress`, `scan-finished`, `log-message`, `github-sync-started`, `github-sync-finished`.
* **HTML element IDs:** `id="noResults"`, `id="filteredCount"`, `id="filteredNum"`.
* **Public methods:** `RunScan()`, `StopScan()`, `FixSelected()`, `GetMovies()`, `UpdateMovie()`, `SyncToCloud()`, `SyncToGitHub()`, `OpenGoogleSheet()`, `OpenGitHubRepo()`, `OpenGitHubPage()`.
* **DB primary key:** `movies.filename` (relative path). Never replace with rowid externally.

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
GROK_MODEL=grok-3-mini
TMDB_API_KEY=
MEDIA_FOLDER_PATH=
EXCLUDE_FOLDERS=
DB_PATH=movies.db
HTML_PATH=local_index.html
POSTERS_DIR=posters
GITHUB_PAGES_BRANCH=main
```

---

## Changelog

### Audit Round 8 Fix Patch — Червень 2026

* `app.go` / `updateMovie()`: Варіант 1.5 (прямий пошук за hint) більше не робить безумовний `return`, коли TMDB знаходить запис без українського перекладу (`TitleUA` порожній або не кириличний). Тепер потік провалюється у Варіант 2 (Gemini) для локалізації — раніше Gemini fallback був недосяжний у цьому сценарії, попри лог-повідомлення що обіцяло протилежне.
* `config.go`: Видалено невикористану функцію `getEnvRequired`.
* `system.go`: Видалено `utils.OpenLogsFolder` — мертвий дублікат `App.OpenLogs` (app.go), ніде не викликався.

---

## Current State Summary (станом на 2026-06-30)

| Area | Status |
|------|--------|
| Recognition pipeline | ✅ Stable. TMDB → Gemini → Grok cascade. |
| Grok fallback | ✅ `grokRecognizeFallback` / `grokTranslateFallback` extracted; `ResetQuotaLock` on scan start. |
| Unresolved placeholders | ✅ `processGeminiQueue` always saves a record, even on failure. |
| Shutdown safety | ✅ `wg.Wait()` after `cancelScan()` before `db.Close()`. |
| File display label | ✅ `utils.DisplayFileLabel` in `GetMovies`, web generator, and Sheets sync. |
| Mobile showcase | ✅ `index.html` (TMDB CDN) for GitHub Pages; `local_index.html` for desktop. |
| Quota lock | ✅ `atomic.Bool` field on `Client`; reset at scan start. |
| Trace IDs | ✅ `EnsureTrace` in `FixSelected` and `UpdateMovie`. |
| Tests | ✅ All `go test ./...` pass (last verified 2026-06-30). |

> For full change history see [CHANGELOG.md](./CHANGELOG.md).
