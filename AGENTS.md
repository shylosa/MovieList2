# AGENTS.md — MovieList App

## Project Overview

MovieList App — desktop application for cataloging local movie/TV collections.

Tech stack: Go 1.26+, Wails v2, SQLite, TMDB API, Google Gemini (`google.golang.org/genai`).

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

### Showcase & GitHub Pages

| File | Role |
|------|------|
| `local_index.html` | Local PC showcase (`HTML_PATH` default); posters from `posters/` (`LocalPosterPath`). Gitignored. |
| `index.html` | Mobile showcase for GitHub Pages; TMDB CDN posters (`PosterURL` when `web.Generate(..., isMobile: true)`). Committed via `SyncToGitHub` only. |

* **Deploy trigger:** `SyncToGitHub()` from UI only — no background sync.
* **Git:** System `git` via `os/exec`; no tokens in code. `git rev-parse --show-toplevel` sets `cmd.Dir` and `index.html` output path. `git push origin <cfg.GitHubPagesBranch>` (default: "main", env: `GITHUB_PAGES_BRANCH`) deploys site root on the configured branch.
* **`git commit` exit code:** Non-zero when there is nothing to commit is **not** treated as failure (showcase unchanged); `git push` still runs.

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
* **Grok Fallback:** `grok-3-mini` (configurable via `GROK_MODEL`) acts as a primary backend when `geminiQuotaLocked` is active, and as a last-resort fallback after all Gemini models fail. Uses `grokLimiter` (30 RPM, burst=1). Reasoning tokens must remain disabled (`ReasoningEffort: "none"`). Never wrap JSON in markdown.
* **Translation Safety:** If official Ukrainian title is unknown, preserve original title (do not hallucinate localization).
* **TMDB Title Trust:** If `movie.TitleUA` is already validated as good Ukrainian, Gemini must not override it, even with Cyrillic calques or alternative transliterations.

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

* Wails events: `scan-started`, `scan-progress`, `scan-finished`, `log-message`, `github-sync-started`, `github-sync-finished`.
* HTML element IDs: `id="noResults"`, `id="filteredCount"`, `id="filteredNum"`.
* Public methods: `RunScan()`, `StopScan()`, `FixSelected()`, `GetMovies()`, `UpdateMovie()`, `SyncToCloud()`, `SyncToGitHub()`, etc.

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
HTML_PATH=local_index.html
POSTERS_DIR=posters
```

---

## Changelog

| File | Method | Change |
|------|--------|--------|
| `internal/tmdb/parser.go` | `ParseFilename` | Added `reNakedLang` regex to strip naked language counters (e.g., 3xRus, 2xUkr) before `go-ptn`; updated `parser_test.go` with new cases. |
| `internal/ai/gemini.go` | `Client` | Added `grokLimiter *rate.Limiter` and initialized it in `NewClient()` to throttle Grok fallback calls. |
| `internal/storage/storage.go` | `New` | Add `_busy_timeout=5000&_journal_mode=WAL` to SQLite DSN in `New()` to reduce "database is locked" errors and enable WAL by default. |
| `internal/ai/grok.go` | `callGrok` | Wait on `c.grokLimiter` before making the HTTP request to Grok, preventing bursty fallback calls. |
| `app.go` | `processTranslationQueue` | Added diagnostic logging for translation candidates that appear to need translation but were not queued, to aid debugging of 429/skip cases. |
| `internal/tmdb/client.go` | `searchDirectly` | Removed unused `searchDirectly` helper — `runPipeline` is the canonical path. |
| `internal/storage/storage.go` | `GetAllMovies, GetMoviesByFilenames, GetMovieByFilename` | Populate `Movie.ID` from SQLite `rowid` to provide stable numeric IDs for APIs that require them. |
| `internal/storage/storage.go` | `PatchMovie`, `SaveMovie`, `SaveMoviesBatch` | Replaced `INSERT OR REPLACE` with merge upsert (`movieUpsertQuery`); `PatchMovie` updates only non-empty fields. |
| `app.go` | `processGeminiQueue` | On batch error or empty recognition: log only; no `SaveMoviesBatch` with filename-only structs. |
| `internal/storage/storage.go` | `SaveMoviesBatch` | Any failed `Exec` aborts the batch; `defer tx.Rollback()` on error paths. |
| `internal/storage/storage.go` | `CleanMissingMovies` | Wraps missing-movie cleanup deletes in a single transaction with `defer tx.Rollback()` and commits on success. |
| `internal/utils/lang.go` | `IsCyrillic, HasCyrillic, IsGoodUkrainian` | Added shared Cyrillic/Ukrainian language helpers for app and TMDB parsing. |
| `app.go` | various | Replaced local Cyrillic detection with shared `utils` helpers; removed duplicate local helper. |
| `internal/tmdb/parser.go` | `detectLanguage` | Uses shared `utils.HasCyrillic`; preserves package wrapper `isCyrillic` for internal tmdb compatibility. |
| `internal/storage/storage.go` | `GetAllMovies` | `rows.Scan` errors propagate via `fmt.Errorf` instead of silent `continue`. |
| `app.go` | `filterUnprocessed` | On `GetAllMovies` error: log and return `nil` to avoid mass rescan. |
| `app.go` | `OpenShowcase`, `SyncToCloud` | Already abort on `GetAllMovies` failure (log + `return`). |
| `app.go` | `RunScan` | Scan body runs in a goroutine; `isScanning` and lifecycle events stay inside it. |
| `app.go` | `RunScan`, `finalizeScan` | Single `scan-finished` emit via `finalizeScan` (`scanFinished` flag prevents double emit). |
| `app.go` | `RunScan` | Deferred scan finalization now calls `finalizeScan(a.ctx, msg)` so post-scan showcase generation does not use the canceled scan context. |
| `app.go` | `UpdateMovie`, `updateMovie` | Public `UpdateMovie(filename, hint)` for Wails; `updateMovie(ctx, ...)` for internal callers. |
| `frontend/wailsjs` | `UpdateMovie` | Regenerated via `wails generate module` (no `context.Context` arg). |
| `app.go` | `logFront` | `log_front_fallback` only when `a.ctx == nil` or `EventsEmit` panics. |
| `app.go` | `fetchAIModels` | Safe `[]string` type assertion; warmup goroutine logs failures at Debug. |
| `app.go` | `FixSelected` | Safe type assertions for `filename` / `hint` map fields. |
| `main.go` | `main` | Panic recover logs `debug.Stack()` (already present). |
| `internal/tmdb/client.go`, `internal/ai/grok.go` | HTTP helpers | `defer resp.Body.Close()` after successful `Do` (already present). |
| `internal/config/config.go`, `internal/ai/gemini.go` | `Load`, `getGenaiClient` | `GEMINI_API_KEY` optional at startup; validated before Gemini calls. |
| `internal/ai/gemini.go`, `internal/ai/gemini_test.go` | `getModels`, `TestGetModelsFiltersTTS` | Gemini model filtering now excludes TTS models before text recognition and verifies only supported text models remain. |
| `internal/ai/gemini.go` | `buildPrompt` | Shorter prompt (3 translit examples); handles `json.Marshal` errors. |
| `app.go`, `internal/ai/gemini.go` | `processTranslationQueue`, `TranslateBulk` | Translation batch size 5; plot omitted when not needed; shorter translate prompt; add instruction to use `original_title` as context. |
| `internal/ai/gemini.go` | `buildGrokRecognitionPrompt` | Compact Grok recognition fallback prompt. |
| `app.go` | `processScanResults`, `movieInfoNeedsTranslation` | Translation queue only for titles/plots that need localization; add context cancellation check and channel draining. |
| `internal/tmdb/search.go` | `searchAndFetch` | Unknown type: sequential `/search/movie` then `/search/tv` (no `/multi`). |
| `app.go` | `runTMDBScan` | Results channel buffer size 10 (matches semaphore pool); make sem write context-aware and handle cancel. |
| `app.go` | `runTMDBScan` | Wrap `wg.Wait()` and `close(resultsChan)` in a background goroutine so result consumption can start immediately and workers cannot deadlock on the small channel buffer. |
| `internal/ai/gemini.go` | `buildPrompt` | Change signature to `(string, error)` and restore detailed prompt template. |
| `internal/ai/gemini.go` | `RecognizeBulk` | Handle error returned by `buildPrompt`. |
| `internal/ai/gemini.go` | `requestWithRetry` | Remove `contexts` parameter and use `prompt` directly for Grok. |
| `internal/ai/gemini.go` | `buildGrokRecognitionPrompt` | Delete the obsolete function entirely. |
| `internal/tmdb/client.go` | `doRequestWithRetry` | Replace unreachable return nil after loop with explicit error return. |
| `internal/ai/gemini.go` | `TranslateBulk` | Ignore json.Marshal error with safe-to-ignore comment. |
| `internal/ai/gemini.go` | `TranslateBulk` | After Gemini 429 or quota lock, fall back to Grok using a text prompt and `grokLimiter`. |
| `internal/config/config.go` | `Load` | Default `HTML_PATH` changed to `local_index.html` (local PC showcase); `index.html` reserved for mobile GitHub Pages sync. |
| `.gitignore` | — | Ignore `local_index.html`; remove `index.html` from ignore so mobile showcase can be committed; add `*.env`. |
| `internal/web/generator.go` | `Generate` | Added `isMobile bool`: TMDB `PosterURL` for mobile, `filepath.ToSlash` only for local posters. |
| `app.go` | `finalizeScan`, `OpenShowcase` | `web.Generate(..., false)` for local PC showcase. |
| `app.go` | `SyncToGitHub` | On-demand mobile showcase: `index.html` + `web.Generate(..., true)`; events `github-sync-started` / `github-sync-finished`; `isGitHubSyncing` double-click guard. |
| `app.go` | `deployToGitHubPages` | `git add -f index.html`, commit (ignore empty), `git push origin <cfg.GitHubPagesBranch>` (env: `GITHUB_PAGES_BRANCH`, default: `main`) via `os/exec` with explicit `cmd.Dir`. |
| `app.go` | `FixSelected` | Use stable app context `a.ctx` for `finalizeScan`, avoiding canceled local scan context propagation. |
| `internal/tmdb/search.go` | `buildAttempts` | Filter generic parent folder names such as `series`, `movies`, `кіно` before creating a parent-directory search attempt. |
| `app.go` | `OpenGoogleSheet, OpenGitHubRepo, OpenGitHubPage` | Added config-driven backend browser-open methods for Google Sheet, GitHub repo, and GitHub Pages. |
| `frontend/src/main.js` | toolbar buttons | Replaced hardcoded repository URL with Wails methods and added a GitHub Pages open button. |
| `frontend/src/main.js`, `frontend/src/style.css` | toolbar layout and project link | Grouped Google Sheets controls into a horizontal toolbar row and updated the project URL to <https://github.com/shylosa/MovieList2>. |
| `frontend/src/main.js` | `btn-sync-github` | Button «Sync GitHub»; `github-sync-started` / `github-sync-finished` disable + spinner; result in console. |
| `AGENTS.md` | Showcase & GitHub Pages | Documented `local_index.html` vs `index.html` and ignored `git commit` exit on empty tree. |
| `app.go` | `gitRepoRoot` | Resolve repo root via `git rev-parse --show-toplevel` for `index.html` generation and deploy `cmd.Dir`. |
| `internal/web/generator.go` | `htmlLayout` | Mobile showcase step 1: `@media (max-width: 600px)` global padding `12px 16px` + `box-sizing: border-box` on `body` and `.container`. |
| `internal/web/generator.go` | `htmlLayout` | Mobile showcase step 2: `.filters-row` wrapper; `@media (max-width: 600px)` horizontal flex row for search/sort/reset (desktop `display: contents`). |
| `internal/web/generator.go` | `htmlLayout` | Mobile step 3: filter widths 60/25/15, compact `btn-reset` icon, placeholders/`title` on controls. |
| `internal/web/generator.go` | `htmlLayout` | Mobile step 4: `normalizeSearchValue`/`initSearchInput`; fix literal `null` from localStorage; `card.style.display` restores CSS grid. |
| `internal/web/generator.go` | `htmlLayout` | Mobile steps 5–7: `@media (max-width: 600px)` two-column `.card` grid, poster max 120px, `.info` padding, plot typography. |
| `internal/web/generator.go` | `htmlLayout` | Mobile filters step 1: `#viewToggle` inside `.filters-row`; one row (50/25/12.5/12.5), `height: 38px`, icon-only toggle on mobile (`.btn-view-label` hidden). |
| `internal/web/generator.go` | `htmlLayout` | Mobile grid/list: `@media (max-width: 600px)` `.grid-mode` 2-column container; vertical cards (poster + title + year); `.list-mode` row layout with plot `line-clamp: 3`. |
| `internal/web/generator.go` | `htmlLayout` | Mobile list-mode step 1: refactored CSS layout under `@media (max-width: 600px)` using grid + `display: contents` to place details/actors next to poster and wrap plot under poster (100% width, 12px margin, line-height 1.45, 4 lines clamp). |
| `internal/web/generator.go` | `htmlLayout` | Step 2: Added autonomous SVG data-URI favicon inside head tag. |
| `internal/web/generator.go` | `htmlLayout` | Step 3: Limited movie titles in mobile grid-mode to exactly 2 lines (`-webkit-line-clamp: 2` with height `2.5em`). |
| `internal/web/generator.go` | `htmlLayout`, `appIconBase64` | Final mobile optimization step 1: restored original PNG app favicon as autonomous Base64 `data:image/png` from `build/appicon.png`. |
| `internal/web/generator.go` | `htmlLayout` | Final mobile optimization step 2: compact mobile sort select to a 38px `⇅` icon button and let search fill the remaining filter row width. |
| `internal/web/generator.go` | `htmlLayout` | Final mobile optimization step 3: mobile list cards expand freely (`height:auto`, `max-height:none`), plot spacing reduced to 6px, plot clamp raised to 5 lines. |
| `internal/web/generator.go` | `htmlLayout` | Final mobile optimization step 4: mobile header title scrolls normally with `padding-top:16px`; only `.filters-row` is sticky at `top:0` with translucent blur background. |
| `internal/web/generator.go` | `htmlLayout` | Mobile sort select checklist step 1: dark mobile `.sort-select` with reset appearance and dark `option` colors. |
| `internal/web/generator.go` | `htmlLayout` | Desktop list width checklist step 2: `.movie-list.list-mode` capped at `max-width: 1200px` with centered `margin: 0 auto`. |
| `CHECKLIST.md` | — | Marked mobile select styling, dark options, desktop list width, and final build validation complete. |

| `internal/tmdb/search.go`, `internal/tmdb/client.go` | `SearchWithFallbacks`, `buildAttempts` | Folder fallback `label: "Папка"` now skips the media scan root by comparing the full parent path to `MEDIA_FOLDER_PATH`; removed generic folder-name dictionary. |
| `internal/ai/gemini.go` | `requestWithRetry`, `makeRequest`, `TranslateBulk` | Implemented centralized Gemini quota exhaustion checks (`isQuotaExhaustedError`), global atomic quota lock, and skips further API calls once locked. |
| `internal/ai/gemini.go` | `TranslateBulk` | If `gemini_quota_lock_skip` occurs and `GROK_API_KEY` is configured, do not abort immediately; continue to Grok fallback. |
| `internal/ai/gemini.go` | `requestWithRetry` | Preserve the current Gemini cascade within the same request after a quota-exhausted model error; lock still skips future requests. |
| `internal/tmdb/search.go` | `isGoodUkrainian` | Refactored Ukrainian title/plot validation to check for non-empty Cyrillic strings excluding Russian-only characters (ы, э, ъ, ё). |
| `internal/tmdb/details.go` | `getMovieDetails`, `getTVDetails` | Optimized language cascade to break early and log `localization_selected` when a valid Ukrainian title is obtained. |
| `app.go` | `RunScan` | Phase 4 verified: `scanTraceID` + `scanCtx` propagated to all workers (`processGeminiQueue`, `processTranslationQueue`, `fetchAIModels`, `runTMDBScan`, `processScanResults`); `context.Background()` only in `fetchAIModels` nil guard (correct). No `trace_id=unknown` in Gemini pipeline. |
| `internal/tmdb/search.go` | `scoreResult` | Phase 5: added `candidate_score_breakdown` debug log (titleScore, yearScore, langScore, popBonus, finalScore) and `candidate_hard_rejected` debug log when titleScore==0. Tracked yearScore/langScore as separate variables for breakdown. Scoring algorithm unchanged. |
| `internal/tmdb/search.go` | `searchAndFetch` | Enforced early-exit of the UA→RU cascade: if `uk-UA` produces a candidate with `score >= 140`, the cascade stops and `ru-RU` is not queried (2026-06-04). |

| `internal/web/generator.go` | `htmlLayout` | (2026-06-05) Mobile CSS tweaks: hide selected `sort` control text on small screens (icon-only closed state), preserve full `<option>` labels in dropdown; expand `plot` visibility in grid and list mobile cards (increased `-webkit-line-clamp`), and reduce visual weight of `genre`/`details` to free vertical space. |
| `app.go` | `RunScan`, `processGeminiQueue`, `FixSelected` | Fixed silent cleanup of missing media/poster records; restored trace-aware logging for AI batches and added `batch_save_success` events for transactional saves. |

### Patch — Червень 2026 (аудит #2)

* `app.go` / `FixSelected`: `finalizeScan` тепер отримує `a.ctx` замість локального скасованого `ctx` (аналог BUG-01 з RunScan)

* `go.mod`: відновлено версію `go 1.26.2` (навмисна версія проєкту, підтверджена власником)
* `config.go` + `app.go`: `deployToGitHubPages` тепер використовує `cfg.GitHubPagesBranch` (env: `GITHUB_PAGES_BRANCH`, default: "main") замість захардкодженого "main"
* `search.go`: `buildAttempts` більше не надсилає generic-імена папок (`series`, `films`, `downloads` тощо) як пошукові запити до TMDB

**CHECKLIST complete (2026-06-02):** Restructured mobile list-mode layout, added SVG data-URI favicon, limited mobile grid-mode title to 2 lines, and verified all compilation steps pass.

**Final verification (2026-06-04):** `go build ./...`, `go vet ./...`, `gofmt -l .` all passed. Phases 4-5 completed.

### Grok Integration Patch — Червень 2026

* `internal/ai/gemini.go`: `requestWithRetry` та `TranslateBulk` тепер при активному `geminiQuotaLocked` одразу викликають відповідні Grok-fallback методи (`grokRecognizeFallback`/`grokTranslateFallback`), оминаючи каскад Gemini.
* `internal/ai/gemini.go`: Виділено методи `grokRecognizeFallback` та `grokTranslateFallback` для усунення дублювання логіки та inline-викликів Grok. Додано метод `ResetQuotaLock` для скидання блокування квоти Gemini.
* `app.go`: Додано скидання блокування квоти через `a.aiClient.ResetQuotaLock()` на початку сканування у `RunScan`.
* `app.go`: Оновлено `fetchAIModels` для коректної підтримки Grok-only режиму при відсутності `GEMINI_API_KEY`, а також додано індикацію fallback-моделі Grok у списку.
* `internal/config/config.go` та `internal/ai/grok.go`: Додано параметр `GROK_MODEL` (дефолт: `grok-3-mini`) в структуру конфігурації замість захардкодженної константи моделі Grok.
* `internal/ai/gemini_test.go` та `app_getaimodels_test.go`: Додано нові тести для перевірки Grok fallback при quota lock, скидання блокування квоти та роботи в Grok-only режимі.

### Unresolved Placeholders Patch — Червень 2026

* `app.go`: У методі `processGeminiQueue` додано створення заготовок (placeholders) `storage.Movie` з `TmdbID = 0`, назвою файлу як `TitleUA`, `"Unresolved: " + filename` як `TitleEN` та розпарсеним роком, якщо Gemini не зміг розпізнати файл або якщо виникла загальна помилка виконання батчу (наприклад, перевищено квоту). Також заповнюються ці поля, якщо фільм не пройшов верифікацію TMDB після розпізнавання ШІ. Це запобігає зникненню нерозпізнаних файлів з бази даних.

### Web Export & Trace ID Patch — Червень 2026

* `internal/web/generator.go`: Замінено клас `.filename` на `.file-path-badge` з використанням monospaced стилю та префіксу емодзі папки (📁) відповідно до дизайну `CHECKLIST.md`. Налаштовано поведінку блоку на мобільних пристроях для запобігання розриву сітки.
* `internal/utils/logger.go`: Додано допоміжну функцію `EnsureTrace(ctx)` для перевірки наявності `trace_id` у контексті та його автоматичної генерації за потреби.
* `app.go`: Забезпечено генерацію та передачу `trace_id` при інтерактивних діях користувача (`FixSelected` та `UpdateMovie`) через `EnsureTrace`, ліквідувавши сліпу зону `trace_id: unknown` у логах.

### Audit Fix Patch — Червень 2026

* `internal/storage/storage.go`: `movieUpsertQuery` не перезаписує `title_ua`/`title_en`/`year`, якщо incoming `tmdb_id=0`, а в БД уже є розпізнаний запис (`tmdb_id > 0`).
* `app.go`: `buildUnresolvedMovie` + `appendUnresolvedIfNeeded` — skip downgrade у `processGeminiQueue`; `FixSelected` викликає `finalizeScan` при STOP і передає trace-aware `ctx`.
* `internal/utils/logger.go`: `EnsureTrace` генерує UUID (8 символів); порожній `trace_id` трактується як відсутній.

