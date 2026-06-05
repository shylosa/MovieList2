# MovieList Improvement Implementation Plan

This plan addresses all 5 phases of improvements specified in [CHECKLIST.md](file:///d:/movielist2/movielist-app/CHECKLIST.md) to optimize TMDB searching, error handling, scoring, localization logic, and trace ID propagation.

## Proposed Changes

---

### Phase 1 — Gemini Quota Lock

We will implement a global lock in [internal/ai/gemini.go](file:///d:/movielist2/movielist-app/internal/ai/gemini.go) to bypass Gemini calls after the first quota exhaustion error is encountered.

#### [MODIFY] [internal/ai/gemini.go](file:///d:/movielist2/movielist-app/internal/ai/gemini.go)
- Add `isQuotaExhaustedError(err error) bool` checking for standard quota messages, HTTP 429, `RESOURCE_EXHAUSTED`, `quota exceeded`, `generate_content_free_tier`.
- Add a package-level or `Client`-level `atomic.Bool` to lock Gemini. (We can add `quotaLocked atomic.Bool` to `Client` struct in `Client`).
- In `NewClient`, verify/initialize the atomic bool is at default false.
- At the start of `RecognizeBulk` and `TranslateBulk`, check if the quota lock is active.
- If locked, do not execute the HTTP call, and return immediately with a controlled error. Log `gemini_quota_lock_skip`.
- If an API call fails, check `isQuotaExhaustedError(err)`. If true, set the atomic bool to true, log warning `gemini_quota_lock_enabled`, and return.

---

### Phase 2 — isGoodUkrainian Fix

We will refine the definition of valid Ukrainian localization to avoid false positives (e.g. Russian text being accepted as Ukrainian due to shared letters).

#### [MODIFY] [internal/tmdb/search.go](file:///d:/movielist2/movielist-app/internal/tmdb/search.go)
- Document current `isGoodUkrainian()` logic in a comment before modifying it.
- Implement the new criteria:
  - Must not be empty.
  - Must contain Cyrillic character(s).
  - Must NOT contain any of: `ы`, `э`, `ъ`, `ё` (and their uppercase equivalents `Ы`, `Э`, `Ъ`, `Ё`).
- Verify standard test cases in comments/unit tests if needed.

---

### Phase 3 — TMDB Cascade Optimization

We will optimize the TMDB detail fetching cascade so that if the Ukrainian details are valid, we immediately break without calling `ru-RU` or `en-US` APIs.

#### [MODIFY] [internal/tmdb/details.go](file:///d:/movielist2/movielist-app/internal/tmdb/details.go)
- In `getMovieDetails` and `getTVDetails`, check if the retrieved `TitleUA` is a valid Ukrainian title using `isGoodUkrainian(finalInfo.TitleUA)`.
- If it is valid Ukrainian, log debug message `localization_selected` with fields: `language`, `title`, `movie_id`.
- Exit the loop via `break` immediately so `ru-RU` and `en-US` endpoints are not queried.

---

### Phase 4 — Trace ID Propagation

We will generate a `scanTraceID` when scanning starts and propagate it through context to all background worker goroutines.

#### [MODIFY] [app.go](file:///d:/movielist2/movielist-app/app.go)
- Generate a unique `scanTraceID` at the start of `RunScan()` using UUID or similar, and wrap it into `scanCtx := utils.ContextWithTrace(ctx, scanTraceID)`.
- Propagate `scanCtx` to all asynchronous procedures and loops: `processGeminiQueue`, `processTranslationQueue`, `fetchAIModels`, etc.
- Replace any generic `context.Background()` with correct context propagation where relevant, ensuring no `trace_id=unknown` logs are emitted during the scanning process.

---

### Phase 5 — Search Scoring Safety Review

We will add comprehensive diagnostic logging to trace candidate scores and hard rejections during TMDB searches.

#### [MODIFY] [internal/tmdb/search.go](file:///d:/movielist2/movielist-app/internal/tmdb/search.go)
- Locate `matchScore()` and where `score = -1000` is assigned.
- In `scoreResult()`, add debug/info logging `candidate_score_breakdown` containing `titleScore`, `yearScore`, `penalties`, `finalScore`.
- If a candidate is hard rejected (score = -1000 due to titleScore == 0), log the reason as `candidate_hard_rejected`.
- Do not modify the actual scoring algorithm.

---

## Verification Plan

### Automated Tests
- Run `go build ./...` after each change.
- Run `go vet ./...` and `gofmt -l .` at the end.
- Execute existing test suite using `go test ./...` to verify no regressions.
