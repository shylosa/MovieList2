# Round 25 implementation handoff

> **Архівний документ.** Round 25 реалізовано. Поточні задачі, runtime-перевірки та критерії приймання містяться в `CHECKLIST.md` (Round 26), а незмінні інваріанти — в `AGENTS.md`. Не використовуйте наведений нижче baseline як актуальний стан робочого дерева.

Updated: 2026-09-12

## Objective

Implement the user-approved scope in `CHECKLIST.md`: lightweight TMDB candidate selection without posters, recognition provenance and suspicious results, versioned AI cache, safe poster cleanup, TMDB details caching, exact-query optimization, grouped TV detection, frontend tests, compact logs and synchronized documentation.

`CHECKLIST.md` is the source of truth for acceptance criteria. It is intentionally gitignored, so read it from the workspace at the beginning of every session.

## Baseline

- Repository: `D:\movielist2\movielist-app`
- Baseline commit observed during handoff: `9c9e0a6 Reworked code`
- Round 24 production verification passed: Go tests, 10 repeated runs, vet, Go build and Wails build.
- Current public APIs and Wails event names are protected by `AGENTS.md`.
- Current recognition state:
  - authoritative manual title lookup;
  - per-row `Авто / Фільм / Серіал` selector;
  - automatic exact movie+TV disambiguation before Gemini fuzzy fallback;
  - `scan_completed` contains separate `disk_total` and `processed_total`.
- Real-log regression already established: `The Bureau` must resolve to TV TMDB `62476`, not movie `802663`.

## Working tree at handoff

The only tracked modification observed before creating this handoff was `README.md`, containing the newly added SQLite table documentation. Treat it as user-owned work and preserve it. This handoff and the `AGENTS.md` update are additional intentional documentation changes.

Always run `git status --short` before editing. Do not reset, checkout or overwrite unrelated changes.

## Recommended implementation slices

### Slice 1 — data contracts and cache safety

Implement FIX-47 and FIX-48 foundations together because both extend storage records. Define recognition source/review semantics and a single `recognitionPipelineVersion`. Add idempotent lazy columns only; the user explicitly declined a migration framework. Preserve old databases and the merge-upsert downgrade guard.

Before continuing, add storage tests for old-schema upgrade, round-trip fields, stale cache miss and manual-confirmation invalidation.

### Slice 2 — candidate backend

Implement the pure TMDB candidate search/ranking layer before Wails methods. Candidate list requirements:

- typed `/search/movie` and `/search/tv` only;
- no poster payload;
- no details, credits, aliases or poster download during preview;
- deterministic deduplication and sorting;
- maximum five results;
- cancellation checks before requests and loop boundaries.

Then add stable new public methods for search and confirmation without renaming existing APIs.

### Slice 3 — candidate UI and review workflow

Build a small inline panel or lightweight modal using DOM APIs, not external-data HTML concatenation. Reuse existing hint/media-type caches. Candidate confirmation must fetch verified TMDB details in the backend, clear suspicious state and invalidate stale AI cache.

### Slice 4 — network and poster efficiency

Move orphan cleanup after successful persistence/finalization conditions. Separate shared TMDB metadata caching from filename-specific local poster handling. Add request counters before optimizing early exits so tests can prove the reduction.

### Slice 5 — grouped TV detection

Build grouping deterministically before worker goroutines. Do not mutate shared maps from workers. Group-derived TV type remains a preference. Protect four-digit years, resolutions and codec tokens from episode-number detection.

### Slice 6 — frontend tests, logs and docs

Choose the smallest viable JS test setup. Cover payloads, cache preservation, candidate states, review counters and scan events. Keep candidate details at DEBUG; INFO should contain winner, at most two alternatives and session counters. Finish by updating README, `.env.example`, AGENTS and CHANGELOG.

## Key risks

- Do not conflate `TmdbID > 0` with semantic certainty: recognized and `NeedsReview` are independent.
- Do not let new zero/default recognition fields downgrade a manually confirmed record during merge upsert.
- Do not attach `LocalPosterPath` to shared cached `MovieInfo`; it is filename-specific.
- Do not delete posters before a scan has completed successfully.
- Do not use `/search/multi`.
- Do not make Gemini parallel; its sequential batching is intentional.
- Do not expose API keys in candidate/request logs.

## Verification cadence

After every slice:

```text
gofmt changed Go files
go test affected packages
go test ./...
git diff --check
```

Before handoff/completion:

```text
go test ./... -cover
go vet ./...
go build ./...
go test ./... -count=10
npm test/build commands introduced for frontend
wails build
UTF-8 without BOM and LF validation
```

Runtime items remain unchecked until the user tests a production build and supplies `build/bin/logs/app.jsonl`.

## Starter prompt for a new session

```text
Працюй у D:\movielist2\movielist-app і виконай весь погоджений Round 25 з CHECKLIST.md.

Спочатку повністю прочитай AGENTS.md, CHECKLIST.md та ROUND25_HANDOFF.md, потім перевір git status і актуальний код. README.md уже має незакомічену документацію SQLite — збережи її та всі інші наявні зміни користувача. Не починай планування з нуля: CHECKLIST.md є затвердженою специфікацією, а ROUND25_HANDOFF.md визначає залежності та рекомендовані slices.

Почни одразу з Slice 1: контракти recognition source/NeedsReview і версіонування ai_resolutions. Далі послідовно виконай candidate backend, легкий candidate UI без постерів, оптимізації poster cleanup/TMDB details/exact lookup, групове визначення серіалів, frontend-тести, компактні логи та документацію.

Працюй автономно до завершення всіх пунктів, які можна перевірити автоматично. Після кожного slice запускай релевантні тести й позначай у CHECKLIST.md лише реально реалізовані та перевірені пункти. Не позначай runtime-перевірки без мого запуску production build і нового лога. Не створюй migration framework, backup/restore, file fingerprint або FTS — це явно поза scope.

Обов'язково збережи інваріанти AGENTS.md: TmdbID > 0 для recognized, тільки typed TMDB endpoints, жодного /search/multi, batch persistence, cancellation checks, sequential Gemini, lifecycle shutdown order, filename primary key, стабільні Wails APIs/events і UTF-8 LF.

Не зупиняйся після аналізу чи складання плану. Реалізуй, тестуй, оновлюй чеклист і документацію. Перед фінальною відповіддю виконай повну verification-секцію з CHECKLIST.md та створи production wails build.
```
