# CHANGELOG — MovieList App

All notable changes to this project. Dates are approximate (session-based).

## Червень 2026 — Audit Round 8 Fix Patch

* `app.go` / `updateMovie()`: Варіант 1.5 (прямий пошук за hint) більше не робить безумовний `return`, коли TMDB знаходить запис без українського перекладу (`TitleUA` порожній або не кириличний). Тепер потік провалюється у Варіант 2 (Gemini) для локалізації — раніше Gemini fallback був недосяжний у цьому сценарії, попри лог-повідомлення що обіцяло протилежне.
* `internal/config/config.go`: Видалено невикористану функцію `getEnvRequired`.
* `internal/utils/system.go`: Видалено `utils.OpenLogsFolder` — мертвий дублікат `App.OpenLogs` (app.go), ніде не викликався.

---

## Червень 2026 — Audit Round 7 Fix Patch

* `internal/ai/gemini.go`: `geminiQuotaLocked` перенесено з package-level `var` у поле `quotaLocked atomic.Bool` структури `Client`. Усуває shared state між інстансами та ізолює тести.
* `app.go`: `reTMDBID` змінено з `^\d+$` на `^\d{5,}$` — запобігає хибному трактуванню року (наприклад, «2024») як TMDB ID у `extractTMDBID`.
* `app.go` / `shutdown()`: Виправлено порядок — `cancelScan()` тепер викликається ДО `wg.Wait()`, щоб горутини могли завершитись замість зависання при довгому HTTP.
* `app.go` / `SyncToCloud()`: Додано mutex-захист (`isCloudSyncing` + `cloudSyncMutex`) від паралельних викликів — аналогічно до `SyncToGitHub`.
* `CHECKLIST.md`: Закрито всі виконані пункти (`- [x]`) попередньої сесії.

---

## Червень 2026 — Dead Code & Docs Cleanup Patch

* `internal/tmdb/search.go`: Removed dead function `isGoodUkrainian` (unreferenced); active replacement is `utils.IsGoodUkrainian` in `lang.go`.
* `AGENTS.md`: Marked superseded changelog entries with `_(Superseded: ...)_` note.
* `internal/storage/storage_test.go`: Added `TestPatchMovie_MergesNonEmptyFields` and `TestPatchMovie_InsertsWhenMissing` — covers merge logic and insert-on-missing behaviour of `PatchMovie`.

---

## Червень 2026 — Shutdown Safety & Generator Fix Patch

* `app.go` / `RunScan`: Added `a.wg.Add(1)` before goroutine launch and `defer a.wg.Done()` inside — `shutdown()` now waits for scan completion before `db.Close()`.
* `app.go` / `FixSelected`: Same `wg` guard added, closing the race window with `db.Close()`.
* `app.go` / `FixSelected`: Replaced `finalizeScan(ctx, ...)` with `finalizeScan(a.ctx, ...)` — prevents `GetAllMovies` from receiving a cancelled context and overwriting `local_index.html` with an empty catalog.
* `internal/web/generator.go`: Fixed movie card badge — class changed from `file-source` to `file-path-badge` (activating existing CSS rules); `{{.Filename}}` replaced with `{{.FileLabel}}`; `title="{{.Filename}}"` preserves full path as tooltip.
* `app.go`: Removed dead function `appendUnresolvedIfNeeded` — all call sites migrated to `appendUnresolvedFromMap`.
* `internal/ai/grok.go`: Updated stale comment on `callGrok` — documented actual `grokLimiter` (30 RPM, burst=1).

---

## Червень 2026 — Web Export, Contexts, Batching & Typings Patch

* `internal/web/generator.go`: Updated movie card template filename layout. _(Superseded by "Shutdown Safety & Generator Fix Patch")_
* `app.go`: Removed `context.WithoutCancel` from `fetchAIModels` to allow abort on `StopScan`.
* `app.go`: Integrated `wg sync.WaitGroup` into `App` struct — tracks background GitHub Pages operations, preventing `db.Close()` during active push.
* `app.go`: Refactored `appendUnresolvedIfNeeded` → `appendUnresolvedFromMap` using batch lookup `GetMoviesByFilenames`, eliminating O(N) SELECT queries.
* `app.go`: Defined `FixRequest` struct and typed `FixSelected` parameter signature.

---

## Червень 2026 — File Display Label Patch

**Rule:** `movies.filename` remains **primary key** (full relative path). UI uses short label `FileLabel` computed by `utils.DisplayFileLabel`.

| Type | Logic |
|------|-------|
| `movie` | file `basename` |
| `tv` | first folder relative to `MEDIA_FOLDER_PATH` (series directory) |
| `tv` at root (no folder) | file `basename` |
| unresolved (`media_type=""`) | heuristic via regexp (`S\d{2}E\d{2}`, `Season \d+`, etc.) |

* `internal/utils/path_display.go`: New function `DisplayFileLabel(relativePath, mediaType string) string`. Regexps `reSeason`/`reEpisode` compiled once at package level.
* `internal/utils/path_display_test.go`: 9 table-driven test cases — all pass.
* `internal/storage/storage.go`: Added `FileLabel string` field to `Movie` (not stored in SQLite; computed before returning to UI).
* `app.go` / `GetMovies()`: Fills `m.FileLabel` for each record before Wails IPC return.
* `internal/web/generator.go` / `Generate()`: Computes `m.FileLabel` in `displayMovies`; template uses `{{.FileLabel}}` in `.file-path-badge` with full path in `title` tooltip.
* `internal/sheets/sheets.go` / `SyncMovies()`: Column header changed from `"File Path"` to `"File"`; values use `utils.DisplayFileLabel`.
* `frontend/src/main.js`: `col-file` now shows `m.file_label || m.filename`; `data-filename` and all Wails API calls remain unchanged (PK). Editor search extended with `label.includes(q)`.

**Verification (2026-06-13):** `go build ./...` ✅ `go vet ./...` ✅ `gofmt -l .` ✅ `go test ./...` — 9/9 new tests, all existing pass ✅

---

## Червень 2026 — Audit Fix Patch

* `internal/storage/storage.go`: `movieUpsertQuery` does not overwrite `title_ua`/`title_en`/`year` if incoming `tmdb_id=0` and DB already has a recognised record (`tmdb_id > 0`).
* `app.go`: `buildUnresolvedMovie` + `appendUnresolvedIfNeeded` — skip downgrade in `processGeminiQueue`. _(Superseded: `appendUnresolvedIfNeeded` removed in "Shutdown Safety & Generator Fix Patch")_
* `internal/utils/logger.go`: `EnsureTrace` generates UUID (8 chars); empty `trace_id` treated as missing.

---

## Червень 2026 — Web Export & Trace ID Patch

* `internal/web/generator.go`: Class `.filename` → `.file-path-badge` with monospaced style and 📁 emoji prefix.
* `internal/utils/logger.go`: Added `EnsureTrace(ctx)` — checks/generates `trace_id` in context automatically.
* `app.go`: `EnsureTrace` applied in `FixSelected` and `UpdateMovie` — eliminates `trace_id: unknown` blind spot in logs.

---

## Червень 2026 — Grok Integration Patch

* `internal/ai/gemini.go`: `requestWithRetry` and `TranslateBulk` now immediately call Grok-fallback methods (`grokRecognizeFallback`/`grokTranslateFallback`) when `geminiQuotaLocked` is active, bypassing the Gemini cascade.
* `internal/ai/gemini.go`: Extracted `grokRecognizeFallback` and `grokTranslateFallback` methods to eliminate duplicated inline Grok logic. Added `ResetQuotaLock`.
* `app.go`: `a.aiClient.ResetQuotaLock()` called at the start of each `RunScan`.
* `app.go`: `fetchAIModels` updated to correctly support Grok-only mode when `GEMINI_API_KEY` is absent.
* `internal/config/config.go` + `internal/ai/grok.go`: Added `GROK_MODEL` config parameter (default: `grok-3-mini`).
* `internal/ai/gemini_test.go` + `app_getaimodels_test.go`: New tests for Grok fallback on quota lock, lock reset, and Grok-only mode.

---

## Червень 2026 — Unresolved Placeholders Patch

* `app.go` / `processGeminiQueue`: Creates `storage.Movie` placeholder with `TmdbID=0`, filename as `TitleUA`, `"Unresolved: "+filename` as `TitleEN`, and parsed year — when Gemini fails to recognise a file or when TMDB verification fails. Prevents unrecognised files from disappearing from the DB.

---

## Червень 2026 — Mobile Showcase & GitHub Pages Patch

* `internal/web/generator.go` / `Generate()`: Added `isMobile bool` — uses TMDB `PosterURL` for mobile, local `filepath.ToSlash` for desktop.
* `app.go` / `finalizeScan`, `OpenShowcase`: `web.Generate(..., false)` for local PC showcase.
* `app.go` / `SyncToGitHub`: On-demand mobile showcase: generates `index.html` + `web.Generate(..., true)`; emits `github-sync-started`/`github-sync-finished`; `isGitHubSyncing` double-click guard.
* `app.go` / `deployToGitHubPages`: `git add -f index.html`, commit (ignore empty), `git push origin <cfg.GitHubPagesBranch>` via `os/exec` with explicit `cmd.Dir`.
* `app.go` / `gitRepoRoot`: Resolves repo root via `git rev-parse --show-toplevel`.
* Multiple mobile CSS iterations in `htmlLayout` (responsive grid, sticky filters, icon-only sort button, dark select styling, desktop list `max-width: 1200px`, PNG favicon as Base64 data URI).

---

## Червень 2026 — Core Reliability & Tooling Patch

| File | Method | Change |
|------|--------|--------|
| `internal/tmdb/parser.go` | `ParseFilename` | Added `reNakedLang` regex to strip naked language counters (e.g., `3xRus`, `2xUkr`) before `go-ptn`. |
| `internal/ai/gemini.go` | `Client` | Added `grokLimiter *rate.Limiter` initialized in `NewClient()`. |
| `internal/storage/storage.go` | `New` | Added `_busy_timeout=5000&_journal_mode=WAL` to SQLite DSN. |
| `internal/ai/grok.go` | `callGrok` | Waits on `c.grokLimiter` before HTTP request. |
| `internal/storage/storage.go` | `GetAllMovies` etc. | Populate `Movie.ID` from SQLite `rowid`. |
| `internal/storage/storage.go` | `PatchMovie`, `SaveMovie`, `SaveMoviesBatch` | Replaced `INSERT OR REPLACE` with merge upsert (`movieUpsertQuery`). |
| `internal/storage/storage.go` | `SaveMoviesBatch` | Failed `Exec` aborts batch; `defer tx.Rollback()` on error. |
| `internal/storage/storage.go` | `CleanMissingMovies` | Wraps deletes in single transaction. |
| `internal/utils/lang.go` | `IsCyrillic`, `HasCyrillic`, `IsGoodUkrainian` | Shared Cyrillic/Ukrainian helpers for app and TMDB packages. |
| `internal/tmdb/search.go` | `searchAndFetch` | Sequential `/search/movie` then `/search/tv` (no `/multi`). |
| `internal/tmdb/search.go` | `buildAttempts` | Skips media scan root folder; skips generic folder names. |
| `internal/tmdb/search.go` | `scoreResult` | Added `candidate_score_breakdown` debug log. |
| `internal/tmdb/search.go` | `searchAndFetch` | Early-exit UA→RU cascade if `uk-UA` score `>= 140`. |
| `internal/tmdb/details.go` | `getMovieDetails`, `getTVDetails` | Early-break language cascade on valid Ukrainian title. |
| `internal/ai/gemini.go` | `requestWithRetry`, `makeRequest`, `TranslateBulk` | Centralized Gemini quota exhaustion checks + global atomic quota lock. |
| `internal/ai/gemini.go` | `TranslateBulk` | Grok fallback after Gemini 429 / quota lock. |
| `internal/ai/gemini.go` | `buildPrompt` | Changed signature to `(string, error)`; restored detailed template. |
| `internal/ai/gemini.go` | `getModels` | Excludes TTS models before text recognition. |
| `internal/config/config.go` | `Load` | `GEMINI_API_KEY` optional at startup; `HTML_PATH` default `local_index.html`. |
| `app.go` | `runTMDBScan` | Results channel buffer 10; `wg.Wait()`+`close` in background goroutine. |
| `app.go` | `RunScan` | Scan body runs in goroutine; single `scan-finished` via `finalizeScan`. |
| `app.go` | `UpdateMovie` | Split into public `UpdateMovie(filename, hint)` for Wails and internal `updateMovie(ctx, ...)`. |
| `app.go` | `OpenGoogleSheet`, `OpenGitHubRepo`, `OpenGitHubPage` | Config-driven browser-open methods. |
| `frontend/src/main.js` | toolbar | Replaced hardcoded URLs with Wails methods; added GitHub Pages button and Sync GitHub button. |
| `.gitignore` | — | Ignore `local_index.html`; track `index.html`; add `*.env`. |

**Final verification (2026-06-04):** `go build ./...` ✅ `go vet ./...` ✅ `gofmt -l .` ✅
