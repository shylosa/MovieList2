# AGENTS.md — MovieList App

## Project Overview

MovieList App — desktop application for cataloging local movie/TV collections.

Main responsibilities:
- Scan local media folders
- Parse filenames
- Match media through TMDB
- Use Gemini as fallback recognition
- Store metadata in SQLite
- Generate static HTML showcase

Tech stack:
- Go 1.26+
- Wails v2
- SQLite
- TMDB API
- Google Gemini (`google.golang.org/genai`)

---

## Architecture

### Core Files
```text
main.go                — Wails bootstrap, panic handler
app.go                 — Main orchestration layer and Wails API

internal/
├── ai/                — Gemini integration
├── config/            — .env loading
├── scanner/           — File scanning
├── storage/           — SQLite layer
├── tmdb/              — Parsing, search, scoring, metadata
├── sheets/            — Google Sheets sync
├── utils/             — Logging/system utilities
└── web/               — Static showcase generator

```

### Recognition Pipeline

Strict execution order:

1. Filename parsing
2. IMDB ID lookup
3. Typed TMDB search (movie / tv)
4. Transliteration fallback
5. Parent directory fallback
6. TMDB scoring and verification
7. Metadata waterfall (uk-UA → ru-RU → en-US)
8. Merge TMDB + Gemini
9. SQLite batch save
10. Gemini fallback queue
11. Translation queue

### AI Agent Priorities

Priority order:

1. Correct recognition
2. Deterministic behavior
3. Cancellation responsiveness
4. Minimal API usage
5. Throughput/performance

---

## Critical Invariants

### Recognition

* `TmdbID > 0` is the only valid "recognized" state.
* Only TMDB-verified entries may enter translation pipeline.
* Gemini-generated IDs are **never** trusted directly.
* `confidence < geminiMinConfidence` keeps entry unrecognized.

### Search Order

Never change recognition order:

1. IMDB ID
2. Typed TMDB search
3. Transliteration
4. Parent folder
5. Gemini fallback

### Language Cascade

Always preserve: `uk-UA → ru-RU → en-US`.
Used in TMDB details waterfall and Cyrillic dual-index search.

### TMDB Search

If media type is known, use `/search/movie` or `/search/tv`.
**Never** use `/search/multi`.

### Translation Queue

Only verified entries may be translated: `movie.TmdbID > 0`.

### Batch Persistence

Always use transactional batch saving: `SaveMoviesBatch()`.
**Never** replace with N individual saves.

### Cancellation

Always check `ctx.Err()`:

* before network requests
* at loop boundaries
Cancellation state must be checked BEFORE calling `cancel()`.

---

## Gemini Processing

* Gemini pipeline is intentionally sequential.
* Do not introduce aggressive parallelism.
* Reason: Gemini RPM limits, Shared rate limiter, Predictable API behavior.

---

## SQLite

Always preserve:

* `PRAGMA journal_mode=WAL`
* `PRAGMA synchronous=NORMAL`
* `PRAGMA busy_timeout=5000`
* SQLite connection count: `SetMaxOpenConns(1)`

---

## Stable Interfaces

Do not rename without explicit migration/update:

* Wails events
* HTML element IDs (Critical: `id="noResults"`, `id="filteredCount"`, `id="filteredNum"`)
* Database column names
* JSON response fields
* Public Wails methods

---

## TMDB Rules

**Required Parameters:** Always use `language=uk-UA` and `append_to_response=credits` for movie/TV details requests.
**Ukrainian Detection:** Use `isGoodUkrainian()`. Never replace with generic Cyrillic checks.

---

## Gemini Rules

* **SDK:** Use only `google.golang.org/genai`. Never use raw HTTP clients.
* **Initialization:** Must support retry. Use `sync.Mutex`. Do NOT use `sync.Once`.
* **Model Loading:** Uses `singleflight.Group`. Do not duplicate concurrent requests.
* **Batching:** Recognition and translation batch size = 10 (sequential execution, shared limiter).
* **Translation Safety:** If official Ukrainian title is unknown, preserve original title (do not hallucinate localization).

---

## Scoring Rules

Core scoring logic:

* Exact title match: `+200`
* Contains match: `+70`
* Exact year: `+150`
* Year ±1: `+80`
* Year > ±2: `-400`
* Media type match: `+30`
* UA language: `+20`
* EN language: `+10`
* RU recent: `-50`
* RU old: `-300`

**Hard rejection:** `titleScore == 0` → reject.
**Default threshold:** `ScoreThreshold = 200`.

---

## Transliteration

Runs only after failed direct search. Digraphs must be processed before single-character replacements.
Examples:

* `Vrag` → `Враг`
* `Nochnoj Rejs` → `Ночной Рейс`
* `Banshi Inisherina` → `Банши Инишерина`

---

## Database Tables

### `movies`

Stores recognized media metadata.
**Primary key:** `filename` (Uses relative media path as identifier).

### `ai_resolutions`

L2 Gemini recognition cache.
**Stores:** resolved title, year, confidence, media type.

---

## Performance Rules

**Always:**

* Use `strings.Builder` for large string generation
* Use package-level regexp variables
* Use `strings.NewReplacer` as reusable singleton
* Use chunking for large SQL `IN` queries
* Use structured logging (`slog`)
* Use transactional writes

**Never:**

* Compile regexp inside loops
* Use `time.After()` in loops
* Mutate shared maps from goroutines
* Add uncontrolled goroutine fan-out
* Spam Wails IPC progress events

---

## Concurrency Rules

* **Shared State:** `movieMap` becomes read-only after initialization. Never mutate from goroutines.
* **Scan State:** `isScanning` must be protected by mutex.
* **scanCancel:** Replacing scan context must atomically cancel previous context.

---

## Public Wails API

* `RunScan()` / `StopScan()` / `FixSelected()`
* `GetMovies()` / `GetStats()`
* `UpdateMovie()` / `DeleteMovie()`
* `GetAIModels()`
* `SyncToCloud()` / `OpenSheet()` / `OpenShowcase()` / `OpenLogs()` / `OpenURL()`

### Wails Events

`scan-started`, `scan-progress`, `scan-finished`, `log-message`

---

## Known Trade-offs

| Decision | Reason |
| --- | --- |
| Sequential Gemini | RPM limits |
| Partial batch save | Single bad row must not break scan |
| RU metadata fallback | TMDB UA coverage incomplete |
| Transliteration | Imperfect, but Gemini acts as final fallback |

---

## Environment Configuration

```env
APP_VERSION=2.0
GEMINI_API_KEY=
GEMINI_MODELS=gemini-2.5-flash,gemini-2.0-flash,gemini-2.5-flash-lite
TMDB_API_KEY=
MEDIA_FOLDER_PATH=
EXCLUDE_FOLDERS=
DB_PATH=movies.db
HTML_PATH=index.html
POSTERS_DIR=posters

```

*.env is loaded near executable.*

---

## Encoding & Formatting

**Required:**

* UTF-8 without BOM
* `gofmt` formatting
* tabs for Go indentation

**Never commit:**

* mojibake text
* mixed encodings
* malformed UTF-8

---

## Grok Fallback Integration

* **internal/config/config.go** — `Load()`: Added `GrokAPIKey` string to `Config` struct and parsed `GROK_API_KEY` from environment.
* **.env**: Verified `GROK_API_KEY` is present.
* **internal/ai/grok.go** — `callGrok()`: Created a new file implementing a private `callGrok` method on the `Client` struct to support OpenAI-compatible requests to x.ai using `grok-3-mini`, refactored HTTP client to Client-level field `grokHTTPClient`, and improved non-200 HTTP status response parsing to extract API errors.
* **internal/ai/grok.go** — `grokRequest`: Added `ReasoningEffort: "none"` to disable xAI thinking tokens for `grok-3-mini` and avoid XML thinking tags in response.
* **internal/ai/gemini.go** — `buildPrompt()`: Added JSON-fence formatting instruction at the end of the prompt to prevent Grok fallback from wrapping response in markdown code blocks.
* **internal/ai/gemini.go** — `parseRecognizeResponse()`: Extracted parsing logic from `makeRequest` to a private function `parseRecognizeResponse` so it can be reused by Grok fallback.
* **internal/ai/gemini.go** — `requestWithRetry()`: Added Grok API fallback after the loop through all Gemini models has failed, a context cancellation check, and structured logging of success (`grok_recognize_success`).
* **internal/ai/gemini.go** — `TranslateBulk()`: Added Grok API fallback after the loop through all Gemini models has failed, added a context cancellation check before executing the Grok fallback, and added formatting instructions to the translation prompt to prevent markdown wrappers from Grok.

---

## Overrides and Translation Bypasses

* **app.go** — `UpdateMovie()`: Added a check for official Cyrillic translation (`TitleUA` contains Cyrillic) from TMDB. If found, saved immediately to database and returned `nil` bypassing Gemini recognition.
* **app.go** — `FixSelected()`: Bypassed adding a movie to the translation queue if a manual fix was specified and the TMDB details already contain a Cyrillic TitleUA and a Plot.
* **app.go** — `mergeGeminiWithTMDB()`: Implemented post-verification check using Jaro-Winkler similarity threshold (`geminiTMDBVerifyMinJW`) to reject Gemini results that deviate significantly from official TMDB titles.
* **internal/ai/gemini.go** — `getModels()`: Filtered the active models list to include only flash, pro, and lite line text models, and excluded embedding, audio, robotics, and computer-use models to prevent 404/400 errors.
* **internal/ai/grok_test.go** — `TestCallGrok_EmptyAPIKey()`: Added test verifying that `callGrok` returns an error when `GrokAPIKey` is empty.
* **internal/ai/grok_test.go** — `TestCallGrok_CancelledContext()`: Added test verifying that `callGrok` returns an error when the context is already cancelled before the call.
* **internal/ai/grok.go** — `callGrok()`: Added special `rate_limited` error format for HTTP 429 responses so callers can log rate-limit events distinctly via slog.
* **internal/ai/grok.go** — `callGrok()`: Added NOTE comment documenting that callGrok has no rate limiter and is only used as a last-resort fallback.


* **app.go** — file: Converted line endings from CRLF to LF and verified UTF-8 decoding.
* **app_getaimodels_test.go** — file: Converted line endings from CRLF to LF and verified UTF-8 decoding.
* **internal/ai/gemini_test.go** — file: Converted line endings from CRLF to LF and verified UTF-8 decoding.
* **internal/sheets/sheets.go** — file: Converted line endings from CRLF to LF and verified UTF-8 decoding.
* **internal/tmdb/parser_test.go** — file: Converted line endings from CRLF to LF and verified UTF-8 decoding.
* **.editorconfig** — file: Added root Go formatting rules for UTF-8, LF endings, tab indentation, trailing whitespace trimming, and final newline.
* **.gitattributes** — file: Added repository text normalization rules for Go and Markdown LF endings.
* **app_getaimodels_test.go**, **internal/ai/gemini.go**, **internal/ai/gemini_test.go**, **internal/ai/grok_test.go**, **internal/tmdb/parser_test.go** — file: Applied `gofmt` to clear final formatting check output.


## Checklist Updates

* **final validation** — `go build ./...`: Completed successfully.
* **.cursorrules** — `Formatting & Encoding`: Added LF-only line ending rule and verification command after the UTF-8 requirement.
* **app.go** — `RunScan()`: Verified after decomposition that no inline blocks over 20 lines remain; TMDB worker logic and scan-result handling are delegated to private methods.
* **app.go** — `processScanResults()`: Extracted `resultsChan` handling from `RunScan` into `func (a *App) processScanResults(ctx context.Context, results <-chan scanResult) (toSave []storage.Movie, geminiQueue []string, translationQueue []string)`; verified `go build ./...`.
* **app.go** — `runTMDBScan()`: Extracted the TMDB parallel worker block from `RunScan` into `func (a *App) runTMDBScan(ctx context.Context, paths []string) <-chan scanResult` and promoted `scanResult` to package scope; verified `go build ./...`.
* **internal/ai/gemini.go** — `buildPrompt()`: Prompt size baseline measured after reductions: 3221 → 1942 chars (saved 1279 chars).
* **internal/ai/gemini.go** — `buildPrompt()`: Reduced `TRANSLITERATION EXAMPLES` to 7 varied examples covering Latin transliteration, Cyrillic titles, numeric titles, and localized-title mismatches; verified `go build ./...`.
* **internal/ai/gemini.go** — `buildPrompt()`: Removed duplicated localized-title, year-verification, uncertainty, and TMDB-merge rules across `MERGE STRATEGY`, `TRANSLITERATION EXAMPLES`, and `CRITICAL TRANSLATION RULES`; verified `go build ./...`.
* **internal/ai/gemini.go** — `buildPrompt()`: Measured current prompt baseline size with empty context JSON (`[]`): 3221 chars.
* **internal/storage/storage.go** — `GetMoviesByFilenames()`: Moved hardcoded chunk size `500` to package constant `filenameChunkSize`; verified `go build ./...`.
* **internal/storage/storage.go** — `InitSchema()`: Added `PRAGMA foreign_keys = ON;` after existing SQLite PRAGMA setup; verified `go build ./...`.
* **internal/utils/logger.go** — `safeWriter.Write()`: Added `// Write errors are intentionally ignored to prevent log recursion` comment for the deliberate ignored write error; verified `go build ./...`.
* **internal/utils/logger.go** — `InitLogger()`: Replaced ignored `os.MkdirAll("logs", 0755)` error with `slog.Error("failed_to_create_logs_dir", ...)`; verified `go build ./...`.
* **project ignored errors** — `app.go`, `internal/ai/gemini.go`, `internal/ai/grok.go`, `internal/tmdb/client.go`, `internal/tmdb/details.go`, `internal/tmdb/search.go`, `internal/storage/storage.go`, tests: `rg "\\w+, _ :=" .` reviewed all matches; critical DB/path/HTTP/body/poster errors now log or return errors, intentional ignores have `// safe to ignore` comments; verified `go build ./...`.
* **project HTTP calls** — `app.go`, `internal/ai/grok.go`, `internal/tmdb/client.go`, `internal/sheets/sheets.go`: `rg "http\\.Get\\(|\\.Do\\(" .` found no `http.Get`; direct HTTP `client.Do` calls all defer `resp.Body.Close()` after success, while `sheets.go` uses Google API `.Do()` without a direct response body.
* **internal/tmdb/client.go** — `Close()`: Added `c.transport.CloseIdleConnections()` guard to release idle TMDB HTTP connections; verified `go build ./...`.
* **internal/tmdb/client.go** — `Client` / `NewClient()`: Added `transport *http.Transport`, initialized it in `NewClient`, and passed it into `http.Client{Transport: transport}`; verified `go build ./...`.
* **app.go** — `RunScan()`: Added `runtime/debug` stacktrace logging to the TMDB worker goroutine `recover()` block; verified `go build ./...`.
* **main.go** — `main()`: Added `runtime/debug` stacktrace logging to the global `recover()` block with `unhandled_panic`; verified `go build ./...`.
* **app.go** — `fetchAIModels()`: Added an explanatory comment and `reqCtx` variable for `context.WithoutCancel(ctx)` inside the singleflight closure; verified `go build ./...`.
* **go.mod** — file: Removed the commented local Wails `replace` directive for `github.com/wailsapp/wails/v2 v2.12.0`.
* **internal/utils/system.go** — `OpenLogsFolder()`: Replaced folder creation/open failure `log.Printf` calls with structured `slog.Error` events and removed the unused `log` import; verified `go build ./...`.
* **internal/ai/gemini.go** — `translateWithRetry()`, `TranslateTitle()`, `TranslatePlot()`: `rg "TranslateTitle|TranslatePlot|translateWithRetry" .` showed only definitions and internal calls among these dead functions; removed all three and verified `go build ./...`.
* **.gitattributes** — file: Added LF normalization rules for JSON and env files.
* **.editorconfig** — file: Added Markdown UTF-8, LF endings, trailing whitespace trimming, and final newline rules.
* **internal/scanner/scanner.go** — `getLargestVideoInDir()`: Renamed `getFirstVideoInDir` and updated the `GetDiskFiles()` call site to reflect largest-file selection.
* **app.go** — `logFront()`: Replaced `log.Output` fallback with structured `slog.Info` and removed the unused `log` import.
* **app.go** — `processTranslationQueue()`: Replaced broad `thought` title filtering with a `<think>` prefix check on trimmed titles.
* **app.go** — `shutdown()`: Added `a.cancelScan()` before closing TMDB and database resources.
* **internal/ai/gemini_test.go** — `mockTransport.RoundTrip()`: Cloned the request before rewriting the target URL for test transport forwarding.
* **app.go** — `RunScan()`: Logged `CleanMissingMovies` failures with `slog.Warn` instead of discarding the error.
* **app.go** — `RunScan()`: Logged `CleanOrphanPosters` failures with `slog.Warn` instead of discarding the error.
* **app.go** — `finalizeScan()`: Logged static showcase generation failures with `slog.Error`.
* **app.go** — `processGeminiQueue()`: Logged `SaveAIResolution` failures with the related filename.
* **app.go** — `UpdateMovie()`: Logged `SaveAIResolution` failures with the related filename.
* **final validation** — `go vet ./...`: Passed with no warnings. `gofmt -l .`: Applied `gofmt -w` to `internal/tmdb/client.go`; list now empty. `go test ./... -count=1`: All tests green (movielist-app, ai, storage, tmdb). Prompt size: 3221 → 1942 chars (saved 1279 chars). Dead code removed: `translateWithRetry`, `TranslateTitle`, `TranslatePlot`. Checklist fully completed.
