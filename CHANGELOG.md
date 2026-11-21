# CHANGELOG — MovieList App

All notable changes to this project. Dates are approximate (session-based).

## 7 жовтня 2026 — Діагностика, локалізація та проєктні скіли

* Додано `LOG_LEVEL=info|debug|warn|error`, який застосовується після завантаження конфігурації при запуску. DEBUG показує модель перекладу, результат локалізації, збережену назву та явне видалення запису; сирі AI-відповіді й конфігурація не додаються до цих подій.
* Генерація пошукових кандидатів прибирає `AMZN` між роком релізу й маркером якості, зберігаючи це слово у справжній назві. Події збереження скана містять фактичний TMDB ID, тип, джерело та стан review після commit.
* Локалізація приймає українську назву або точний оригінал, зберігає валідну українську назву й відхиляє сторонні неукраїнські відповіді. Повідомлення розрізняють результати для назви та опису; лічильник оновлень змінюється лише після успішної транзакції. Правило збереження оригіналу за невідомої офіційної української назви збережено.
* README описує проєктні скіли й цикл роботи з журналами. `movielist-log-analysis` обов’язково оцінює необхідність змін коду; додано `movielist-docs` для узгодження документації з реалізацією.
* Go-тести, `go vet` та production-збірки змін логування й локалізації пройшли. Журнал підтвердив фонову локалізацію одного TMDB-кандидата; повний production-скан і решта ручних UI-сценаріїв залишаються відкритими.

## 3 жовтня 2026 — MovieList 2.9.0

* Сайдбар містить Бібліотеку, Активність і Редактор; унизу закріплено Папки сканування та меню налаштувань. Сканування запускається лише кнопкою в Бібліотеці. Вітрину перенесено до «Експорту і синхронізації» поруч із Google Sheets та GitHub Pages.
* Папки сканування та виключення керуються двома таблицями з додаванням і видаленням. Кілька джерел мають стабільні ідентифікатори без зміни старих ключів; порожній список або недоступне джерело зупиняє скан до очищення каталогу.
* Додано текстовий редактор `.env` з автозбереженням, перевіркою синтаксису, UTF-8/LF, атомарним записом і захистом від перезапису зовнішніх змін. Конфігурація застосовується після перезапуску. «Про MovieList» відкривається компактним модальним вікном із закриттям кнопкою, кліком поза ним або Escape.
* Єдине джерело версії — `internal/version/VERSION`. `build.ps1` синхронізує метадані Wails/npm та HTML перед збіркою; Go вбудовує версію для заголовків, UI, логів і вітрин. `APP_VERSION` більше не впливає на застосунок.
* «Моделі ШІ» має дві таблиці Gemini/Groq: відкриття читає збережений вибір, каталоги запитуються окремими кнопками, прапорці й порядок зберігаються автоматично. Активні операції використовують зафіксований на початку каскад.
* Groq отримує каталог через `/openai/v1/models` і виконує розпізнавання/переклад через `/openai/v1/chat/completions` із власним ключем та параметрами reasoning для відповідної моделі. Сумісність із попереднім xAI Grok збережено; ключі провайдерів не змішуються.
* Виправлено контракт Groq-розпізнавання: `confidence` обов’язковий і перевіряється в діапазоні 0–1. Реальні синтетичні перевірки підтвердили GPT-OSS 120B/20B та український переклад через 120B. Qwen для цього пакетного запиту повернув HTTP 429: 1426 токенів за ліміту 1000; збільшення бюджету відповіді цього обмеження не усуває.
* Оновлено документацію й інструкції агентів. Повний production-скан реальної медіатеки та ручні UI-сценарії залишаються відкритими в `CHECKLIST.md` і `DESIGN_CHECKLIST.md`.

## 2 жовтня 2026 — MovieList 2.8.1

* Виправлено rescue після порожньої відповіді Gemini: точна локалізована назва з відповіді TMDB зберігається для перевірки; поріг 0,85 залишається. Неоднозначний кандидат або розбіжність ID/типу між search і details відхиляються. Реальні TMDB-перевірки підтвердили TV 284725 і TV 241882.
* Release-хвости `andre90`/`TRIPLE` і порожні дужки не потрапляють у пошукові назви; Lovelace більше не стає TV через помилкове трактування `andre90` як епізоду. Справжні S01/S01E01 та слова назви збережено. Повторні папкові спроби усунено в межах одного послідовного каскаду, зі збереженням року, типу й мовного порядку.
* `app_started` містить версію; `metadata_repaired` — ID, тип і стан перевірки до/після, змінені поля, джерела назви/опису та тривалість. Idle HTTP-відповіді Go transport логуються як `http_idle_unsolicited_response` без сирого вмісту; провайдер за відсутності контексту позначається `unknown`.
* Ізольовані та opt-in інтеграційні тести перевіряють випадки production-логу. Реальний TMDB підтвердив Ted 72105 і Road Trip 9285 без AI; перевірено відсутність `gemini-2.0-flash` у локально збереженому виборі моделей. Новий повний production-скан та візуальні сценарії залишаються відкритими.
* Сканування в сайдбарі запускається значком у рядку «Бібліотека»; окремий пункт меню прибрано.
* У сайдбарі прибрано підзаголовок «Інструменти», «Огляд і журнал» перейменовано на «Журнал», «Моделі ШІ» розміщено під «Редактор». Версію узгоджено до 2.8.1.

## 2 жовтня 2026 — MovieList 2.8.0

* Жанри й рік на картках та в перегляді, актори у перегляді відкривають добірку через існуючий пошук бібліотеки. Пошук бібліотеки й редактора знаходить жанри та акторів.
* Значок блискавки праворуч від TMDB у Редакторі оновлює свіжі деталі збереженого TMDB entity без повторного розпізнавання. Зберігає ID, тип і стан перевірки; надає пріоритет українським даним TMDB та перекладає відсутню локалізацію через наявний AI-клієнт. У повному перегляді цієї дії немає.
* Збереження використовує транзакційний batch upsert із перевіркою конкурентних змін; помилки перекладу й постера повідомляються як часткове оновлення. Завершення застосунку скасовує активний Repair.
* «Опис» і «Актори» в панелі Редактора показують збережені дані при наведенні або фокусі; клік закріплює поповер, Escape, хрестик або клік поза ним закриває його.
* Блок «Потребують перевірки» у бібліотеці відкриває відповідний фільтр Редактора й очищає попередній пошук. Після вибору TMDB-варіанта картка залишається відкритою з оновленими даними, навіть коли запис зникає з проблемного списку.
* Додано сортування Редактора «За додаванням»: старіші записи каталогу зверху, новіші внизу. Клікабельні метадані підсвічуються без підкреслення.
* Узгоджено версію 2.8.0 у конфігурації, локальному `.env`, HTML/Wails-заголовках, npm-метаданих, шаблоні середовища та документації. Візуальна перевірка на реальній медіатеці залишається відкритою в `DESIGN_CHECKLIST.md`.

## 27 вересня 2026 — MovieList 2.7.1

* Backfill локалізації читає лише потрібні поля послідовно й не повторює AI-запити для коректної української назви з порожнім описом.
* SQL upsert використовує єдину умову зміни TMDB identity; нерозпізнаний запис не змінює media type уже розпізнаного.
* Заголовок Wails використовує версію з конфігурації; sidebar відкриває репозиторій без inline-обробника.
* Узгоджено версію 2.7.1 у конфігурації, заголовках, npm-метаданих, шаблоні середовища та документації.

## 26 вересня 2026 — MovieList 2.7.0

* Оновлено Редактор: компактний список, панель вибраного запису з постером, пакетне уточнення, швидке очищення пошуку та варіанти TMDB.
* Картки бібліотеки показують жанри й компактний рейтинг; оцінка в перегляді та редакторі веде до TMDB. Позначення серіалів у картках скорочено до «TV».
* Підтвердження видалення запису розміщено під кнопкою «Видалити»; панель містить лише кнопки скасування та видалення.
* Узгоджено версію 2.7.0 у конфігурації, локальному `.env`, заголовках, npm-метаданих, шаблоні середовища та документації.

## 25 вересня 2026 — MovieList 2.6.0

* Узгоджено версію в конфігурації, локальному `.env`, шаблоні, заголовках Wails/HTML, npm-метаданих і документації.
* Додано бібліотеку з постерами, пошуком, фільтрами, статистикою та компактним прогресом сканування.
* Картка відкриває окремий перегляд; редактор зберігає ручні виправлення та має швидке очищення пошуку.
* Оновлено темну палітру, навігацію та подання метаданих фільму. Перевірку фінального вигляду на реальній бібліотеці залишено відкритою.

## 13 вересня 2026 — Round 28 automatic exact disambiguation

* Popularity is bounded to a small tie-breaker and can no longer dominate exact movie/TV identity selection.
* Clean filename and release-folder signatures disambiguate already-exact homonyms without admitting fuzzy candidates.
* `Bjuro legend.../Bjuro legend.08...avi` now selects The Bureau TV 62476 even when Gemini prefers movie, supplies no year and movie popularity is much larger.
* Exact selection emits one compact structured winner summary with preferred/selected type and identity score.
* Candidate search reuses the verified AI-resolution alias (for example `The Bureau`) when stored localized/original titles only rediscover the current record.
* A current TMDB identity is shown first in `Варіантах` when `NeedsReview` is set, allowing explicit confirmation to clear the warning; selecting the same media type and running the fix now confirms locally without an AI request.

## 13 вересня 2026 — Round 27 candidate/manual-fix safety

- Candidate search rejects filename-shaped placeholders and uses clean deterministic manual, stored-title, filename and parent queries.
- No-hint manual re-recognition preserves the verified year/media type and refuses silent TMDB identity replacement.
- Candidate and Gemini merge logs now expose compact query-source and requested/preferred/selected-type diagnostics.
- Unavailable configured Gemini models are skipped in configured order; production defaults remain unchanged.

## 13 вересня 2026 — Round 26 grouped TV safety

* Версію документації та `.env.example` синхронізовано з default `APP_VERSION=2.5.0`; застарілі handoff/implementation plan позначено архівними.
* Сильні episode-маркери відокремлено від слабких голих номерів; одиночне число більше не робить каталог серіальним.
* Слабкий TV preference потребує двох сусідніх номерів з однаковою серіальною основою й застосовується лише до відповідних файлів, а не до всього mixed-каталогу.
* Службові каталоги (`Фильмы`, `Фільми`, `Movies`, `Video`, `Media`) не замінюють parsed filename title.
* Конфлікт grouped-TV з movie parsing отримує `NeedsReview`; контекстно прийняті результати з конфліктом року/type або низькою verification score також не вважаються надійними.
* TMDB details waterfall захищено `singleflight`; stale AI cache не може перезаписати новішу pipeline version.
* INFO-лог містить компактний winner/alternatives summary та повні Gemini/Grok session counters без candidate DEBUG spam.
* Розпізнана назва в редакторі отримала кешований TMDB details preview при hover/focus. Floating overlay автоматично відкривається над елементом біля нижньої межі й не обрізається scroll-контейнером; поточний TMDB-запис більше не дублюється у `Варіантах`.
* Підтвердження кандидата стало двофазним: ID і TMDB metadata зберігаються у foreground, а poster download та Gemini localization працюють у tracked background task. `movie-updated` освіжає UI, а TMDB-ID guard відкидає запізнілий результат для вже заміненого кандидата.

## 12 вересня 2026 — runtime corrections Round 25

* Candidate search більше не повторює помилкову назву з БД: без явної підказки використовується filename, а для невдалого або несумісного за роком транслітерату є typed fallback. На той момент поточний TMDB-запис показувався першим; у Round 26 його preview перенесено на розпізнану назву.
* Старі дублікати одного movie TMDB ID для різних назв позначаються як сумнівні; automatic ambiguous exact більше не втрачає `NeedsReview`.
* Назви й описи проходять alphabet-based language validation; джерела перекладу мають окремі EN→RU та RU→EN пріоритети. Виправлено російські рядки на кшталт `Суперкопы 80`.
* «Моделі ШІ» показує поточний каскад та повний сумісний список Gemini API, дозволяє зберегти checkbox-вибір у SQLite.
* Кнопку candidate picker перейменовано на стримане `Варіанти`; production log за замовчуванням має INFO-рівень.

## Вересень 2026 — Audit Round 25

* Додано versioned `ai_resolutions`, recognition provenance та окремий `NeedsReview` стан.
* Редактор має lightweight TMDB candidate picker без poster/details запитів до підтвердження.
* TMDB details кешуються на сесію окремо від filename-specific постерів; orphan cleanup виконується лише після успішного scan.
* Додано grouped-TV preference, TMDB request metrics, frontend unit tests і окремі unresolved/suspicious counters.
* Candidate picker отримав on-demand `Уточнити`: кешований popover із runtime, short-film marker, CDN-постером, жанрами, описом і рейтингом без credits або локального poster download.
* Рейтинг і кількість голосів TMDB зберігаються в SQLite, показуються в редакторі/Вітрині/Sheets і lazily дозаповнюються для старих записів.

## Вересень 2026 — Audit Round 24 Fix Patch

* Automatic Gemini→TMDB merge тепер спочатку виконує exact dual-type lookup; Gemini media type є tie-breaker, а не жорстким endpoint lock.
* Exact localized title передається як validated alias для strong post-verification. Regression підтверджує `The Bureau` → TV `62476` замість movie `802663`.
* `scan_completed` розділяє `disk_total` і `processed_total`; accepted/unresolved counters описують лише реально оброблені файли.

---

## Вересень 2026 — Manual media type selector

* У редакторі додано selector `Авто / Фільм / Серіал` для кожної ручної підказки; вибір зберігається при фільтрації списку й передається у `FixSelected` як `media_type`.
* Strict `Фільм` використовує лише `/search/movie`, strict `Серіал` — лише `/search/tv`; `Авто` перевіряє обидва typed endpoints.
* Exact primary, original, localized та validated-alias matches отримують однакову базову вагу. Неоднозначність розв'язують популярність, рік і слабкий filename type hint.
* Додано regression для `The Bureau` зі strict TV та перевіркою, що movie endpoint не викликається.

---

## Вересень 2026 — Audit Round 23 Fix Patch

* Ручна текстова назва стала авторитетною підказкою: вона проходить окремий exact TMDB movie/TV lookup і більше не переписується filename parser, scoring або Gemini.
* Exact original/localized/validated-alias match підтверджує назву; рік і media type використовуються лише як tie-breakers. `TmdbID` як і раніше походить виключно з TMDB.
* Додано регресії для `The Bureau`, `Enemy` проти `Shatru`, розбіжності року та заборони Gemini fallback для ручної назви.
* Gemini discovery тепер враховує `supportedGenerationMethods`, configured order і фільтрує preview/TTS/image/embedding та відому недоступну `gemini-2.5-pro`.
* Дефолтний каскад оновлено до `gemini-2.5-flash,gemini-flash-lite-latest`; постійно недоступна модель вимикається до наступного refresh.
* `scan_completed.needs_review` більше не є константою; `FixSelected` пише підсумок requested/resolved/unresolved.

---

## Вересень 2026 — Audit Round 22 Fix Patch

* `finalizeScan` більше не може отримати скасований scan-контекст: усі фінальні читання БД та генерація showcase виконуються через lifecycle-контекст додатку.
* Додано регресійні тести для cancellation, disk scan error, no-change scan, `last_scan_at` та єдиного `scan-finished`.
* GitHub Pages workflow отримав ін'єктований git runner та синхронне тестове ядро без зміни публічного Wails API чи назв events.
* Покрито порожню БД, відсутній repository, порядок add/commit/push, commit/push failures, configured branch, duplicate guard і `App.wg` без реальних push або мережі.
* Додано unit-тести `internal/web`, `internal/config` та pure mapping для `internal/sheets`.
* Пройдено `go test ./...`, coverage, vet, build, 10 повторів тестів і production `wails build`.

---

## Вересень 2026 — Audit Round 19 Fix Patch

* TMDB HTTP errors now redact `api_key` throughout the error chain while preserving `errors.Is` cancellation semantics.
* TMDB search attempts, language/endpoint cascades, metadata waterfalls and poster downloads now return immediately on context cancellation instead of producing additional fallbacks and WARN noise.
* `RunScan` emits one structured `scan_cancelled` INFO record with trace ID and duration.
* Added cancellation and secret-redaction regression tests, including a one-request assertion for cancelled cascades.

---

## Вересень 2026 — Audit Round 18 Fix Patch

* `frontend/src/main.js`: стан сканування тепер завершується лише за подією `scan-finished`; STOP не вимикається після миттєвого повернення RPC `RunScan`. Рендеринг записів переписано на безпечні DOM API без вставки зовнішніх значень через `innerHTML`; TMDB URL обмежено офіційним HTTPS-host.
* `internal/scanner/scanner.go`: дисковий обхід приймає `context.Context`, реагує на STOP і повертає помилки `WalkDir`. Cleanup БД більше не запускається після неповного або скасованого обходу.
* `internal/tmdb/client.go`: постери завантажуються у temporary-файл і атомарно перейменовуються лише після повного непорожнього запису; partial-файли прибираються при помилці.
* Додано регресійні тести scanner та завантаження постерів. `go test ./... -count=10`, `go vet ./...`, `go build ./...` і frontend build пройшли.

---

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
# Audit Round 20 — Вересень 2026 (Recognition hardening)

* TMDB fallback тепер окремо зберігає `queryYear` і `targetYear`: API-запит може бути
  розширений без року, але scoring, cache key і фінальна перевірка не втрачають рік файла.
* Post-Gemini та L2-cache verification більше не приймають збіг лише через локалізований
  `SearchTitle`; потрібен збіг з original title або перевіреним alternate title, сумісні
  рік і тип медіа.
* Language/popularity винесено в tie-breaker відносно identity score.
* `2O16`, `2o16`, `2О16` нормалізуються як рік без заміни літер у звичайних словах.
* Gemini recognition отримав `request_id`, ambiguity metadata, зіставлення незалежно від
  порядку відповіді та один індивідуальний retry для пропущеного batch-елемента.
* Додано `scan_completed` з підсумковими recognition/API-call метриками.
# Audit Round 21 — Вересень 2026 (Ручне уточнення через IMDb)

* Додано підтримку повних IMDb URL та чистих `tt...` ID у ручному уточненні.
* IMDb-підказка викликає TMDB `/find` і details напряму, без title scoring, року файла
  та Gemini fallback.
* Невідомий IMDb ID повертає явну помилку й не запускає неточний текстовий пошук.
* `FixSelected` очищає TMDB cache, рахує фактичні успіхи та не додає невдалі записи
  до черги перекладу.

## Архів нотаток з AGENTS.md за попередні раунди

Перенесено з історичного розділу AGENTS.md у стані на коміті `cdf44e0`. Згадки про тести, логи та чеклисти описують спостереження того часу.

### Audit Round 24 — Вересень 2026 (automatic exact disambiguation)

* Gemini results first use exact typed movie+TV lookup; reported AI media type is only a weak tie-breaker.
* Exact localized search titles are validated aliases for post-verification.
* `scan_completed.processed_total` is the denominator for `tmdb_accepted + ai_accepted + unresolved`; `disk_total` remains the collection size.

### Manual media type selector — Вересень 2026

* `FixRequest.media_type` приймає `auto`, `movie` або `tv`; порожнє значення сумісне з `auto`.
* Strict manual type обмежує пошук відповідним typed TMDB endpoint.
* В Auto exact original/localized/alias titles рівноправні; popularity/year/type використовуються лише для deterministic tie-break.

### Audit Round 23 — Вересень 2026 (authoritative manual titles)

* Точна ручна назва не передається Gemini: typed TMDB exact search перевіряє primary/original/localized/alias titles, а filename year/type є лише tie-breakers.
* Gemini discovery допускає лише `generateContent` production-моделі у configured order; недоступні 404-моделі вимикаються до refresh.
* Default Gemini cascade: `gemini-2.5-flash,gemini-flash-lite-latest`.
* Scan/fix summaries повідомляють фактичні unresolved counters.

### Audit Round 22 — Вересень 2026 (lifecycle і deployment tests)

* `finalizeScan` завжди використовує lifecycle-контекст; cancellation і disk errors не очищають showcase та не оновлюють `last_scan_at`.
* GitHub Pages git execution і Wails event emission мають вузькі test adapters; публічні API та events незмінні.
* Додано ізольовані тести scan lifecycle, GitHub deployment, web generator, config та Sheets row mapping.
* Production `wails build` і повний набір Go-перевірок проходять.

### Audit Round 21 — Вересень 2026 (IMDb manual hints)

* `FixSelected` / `UpdateMovie` accept full IMDb title URLs and bare `tt...` IDs.
* IMDb hints use TMDB `/find` directly and never fall through to title scoring or Gemini.
* Unknown authoritative IDs return an explicit error instead of silently retrying as text.
* FixSelected clears TMDB caches and reports actual resolved/unresolved counts.

### Audit Round 20 — Вересень 2026

* TMDB no-year fallback preserves the parsed target year for ranking and cache identity.
* Gemini verification requires a strong original/validated-alias match; localized titles
  are diagnostic-only signals.
* Gemini batch outputs correlate by `request_id`; a missing item gets at most one
  sequential retry and cancellation prevents retry.
* Year-like homoglyph normalization is limited to four-digit year tokens.
* `scan_completed` reports deterministic and AI recognition counts plus AI call metrics.

### Audit Round 15 — Липень 2026

Лог 12:58–12:59: FIX-15, FIX-16, FIX-20 підтверджені. Нуль багів.

* `app_updatemovie_test.go`: `TestMaxTitleSimilarity_CyrillicCandidate` +
  `TestMaxTitleSimilarity_MatchedAlias` — тести для FIX-20, пропущені в R14.
* `CHECKLIST.md`: Очищено (раунди 1–15 закриті).

### Audit Round 14 Fix Patch — Липень 2026 (Аналіз логів)

Підтверджено логами: FIX-15 (warmup a.ctx) — нуль warmup_failed. FIX-16 (app_closed у
shutdown) — рівно один app_closed в кінці сесії. FIX-17 (folder_en_fallback) — активний.

* `app.go` / `maxTitleSimilarity()`: Додано перевірку `info.TitleUA` між `TitleEN` і
  `SearchTitle`. Раніше кириличний кандидат ("Скарпетта") отримував similarity=0 проти
  TitleEN="Scarpetta" (JaroWinkler між різними алфавітами = 0), хоча TitleUA="Скарпетта" —
  точний збіг. Rescue витрачав 2 зайвих TMDB-запити і знаходив відповідь лише через
  borrowedLatinVariants. Після фіксу: accepted на першому кандидаті (similarity=1).

### Round 13b — Аналіз логів після FIX-17 (Липень 2026)

Підтверджено: FIX-15 (warmup a.ctx), FIX-17 (folder_en_fallback) — в логах присутні.
`ai_models_warmup_failed` не спостерігається.

Спостереження:
* `"Скарпетта"→"Skarpetta"` через folder_en_fallback: технічно правильна транслітерація,
  але TMDB назва `"Scarpetta"` (італійська) — не знайдено. Файл коректно перенаправлено
  в Gemini, який повернув правильний EN title. Post-recognition lookup знайшов серіал через
  TV endpoint. Каскад відпрацював.
* `internal/utils/lang.go` / `CyrillicToLatin()`: FIX-19 — подвоєні приголосні (тт→tt тощо)
  обробляються окремим replacer до одинарних (BGN/PCGN). Тест `TestCyrillicToLatinDoubleConsonants`.

---

### Audit Round 13 Fix Patch — Липень 2026 (Код + Аналіз логів)

* `app.go` / `RunScan()` warmup goroutine: `fetchAIModels(scanCtx)` → `fetchAIModels(a.ctx)`.
  Warmup кешує AI-моделі для lifecycle додатку; `scanCtx` скасовується при миттєвому
  завершенні скана (нових файлів нема) і передчасно вбивав HTTP-запит (`context canceled`).
* `internal/utils/logger.go` / `CloseLogger()`: Видалено `slog.Info("app_closed")`.
* `app.go` / `shutdown()`: Додано `slog.Info("app_closed")` першим рядком. Усуває подвійні
  `app_closed` при WebView2 retry (~15с) або повторному запуску exe на Windows.
* `internal/tmdb/search.go`: Варіант "Папка" пробує EN cascade після провалу CYR — усуває
  cache hit блокування EN-пошуку для кириличних назв папок.
* `internal/tmdb/client.go` / `pipelineLatin()`: Guard `!HasCyrillic(cleanTitle)` → пропуск
  lat→cyr транслітерації для чисто ASCII назв (заощаджує 2+ зайвих TMDB-запити).

---

### Audit Round 12 Fix Patch — Липень 2026

* `app.go` / `finalizeScan()`: Додано параметр `success bool`. `SetState("last_scan_at")`
  тепер викликається лише при `success = true` — виключає STOP користувачем і disk error
  як хибні мітки часу "останнього сканування". Оновлено всі 5 викликів.
* `app.go` / `processTranslationQueue()`: Виправлено коментар — `movies` є результатом
  `GetMoviesByFilenames(filenames)`, а не "all movies in the DB".
* `internal/storage/storage_test.go`: Додано `TestSetAndGetState` — базовий set/get,
  upsert і відсутній ключ для `SetState`/`GetState`.
* `CHECKLIST.md`: Очищено (всі раунди 1–12 закриті).

---

### Audit Round 8 Fix Patch — Червень 2026

* `app.go` / `updateMovie()`: Варіант 1.5 (прямий пошук за hint) більше не робить безумовний `return`, коли TMDB знаходить запис без українського перекладу (`TitleUA` порожній або не кириличний). Тепер потік провалюється у Варіант 2 (Gemini) для локалізації — раніше Gemini fallback був недосяжний у цьому сценарії, попри лог-повідомлення що обіцяло протилежне.
* `config.go`: Видалено невикористану функцію `getEnvRequired`.
* `system.go`: Видалено `utils.OpenLogsFolder` — мертвий дублікат `App.OpenLogs` (app.go), ніде не викликався.

---

### Audit Round 9 Fix Patch — Липень 2026

* `internal/ai/gemini.go` / `TranslateBulk()`: Додано `else`-гілку при `empty Candidates` — тепер логується `Warn("bulk_translate_empty_candidates")` і оновлюється `lastErr`. Раніше порожня відповідь від Gemini (safety block / content filter) мовчки пропускалась, що давало оманливий error message після вичерпання всіх моделей.
* `app.go` / `RunScan()`: Warmup goroutine (`fetchAIModels`) тепер tracked у `a.wg` (`wg.Add(1)` + `defer wg.Done()`). Раніше горутина могла пережити shutdown і звернутись до `a.aiClient` після `db.Close()`.
* `app.go`: Видалено мертву змінну `russianMarkers []string` — артефакт рефакторингу, замінений на `russianMarkersRE`.
* `internal/storage/storage.go`: Видалено мертвий метод `GetAllFilenames` і реліктовий коментар над ним.

---

### Audit Round 11 Fix Patch — Липень 2026

* `internal/utils/path_display.go` / `reSeasonCut`: Видалено year-альтернатив (`[.\s]\d{4}[\s._].*`) — регекс більше не обрізає рік якщо він є частиною назви серіалу (The.1984.Show, 1923...). Обрізання відбувається тільки від маркера сезону `S\d{2}`. Додано регресійні тести.
* `app.go` / `GetStats()`: Замінено `os.Stat(DBPath).ModTime()` на `db.GetState("last_scan_at")` — час останнього сканування тепер записується явно після завершення scan і не залежить від WAL checkpoint-ів SQLite.
* `internal/storage/storage.go`: Додано таблицю `app_state (key TEXT PK, value TEXT)` і методи `SetState` / `GetState`.
* `CHECKLIST.md`: Очищено (всі раунди 1–11 закриті).

---

### DisplayFileLabel: фінальна семантика — Липень 2026

* `internal/utils/path_display.go`: Переписано повністю. Видалено `reSeason`, `reEpisode`,
  `reSeasonCut`, `cleanFolderLabel`, імпорт `"regexp"`. Нова логіка: є каталог → сира назва
  першого рівня; плоский файл → ім'я файлу. Badge слугує навігаційною міткою для пошуку
  файлу на диску, тому потрібна точна сира назва, а не очищений UX-заголовок.
* `internal/web/generator.go`: `title="{{.Filename}}"` → `title="{{.FileLabel}}"`.
* Сигнатура `DisplayFileLabel` скорочена до 1 параметра. Оновлено всі 3 виклики
  (app.go, generator.go, sheets.go) і тести.

---

### Audit Round 19 Fix Patch — Вересень 2026

* TMDB network errors redact `api_key` inside error strings without breaking `errors.Is(err, context.Canceled)`.
* Search attempts, language/endpoint loops, metadata waterfalls and poster downloads stop immediately when the scan context is cancelled; cancellation no longer appears as file-level WARN noise.
* `RunScan` emits one structured `scan_cancelled` INFO event with trace ID and duration.
* Regression tests verify secret redaction and that cancellation permits only the already-started transport request.

### Audit Round 18 Fix Patch — Вересень 2026

* `frontend/src/main.js`: scan UI керується подіями `scan-started` / `scan-finished`, тому асинхронний backend scan не втрачає активну кнопку STOP після повернення RPC. Movie editor використовує DOM API (`textContent`, properties, `dataset`) замість HTML-конкатенації зовнішніх даних; TMDB-посилання обмежені офіційним HTTPS-host.
* `internal/scanner/scanner.go`: `GetDiskFiles(ctx)` і вкладений `WalkDir` перевіряють cancellation та повертають помилки. Неповний disk scan завершується до `CleanMissingMovies`, захищаючи валідні записи і постери від помилкового cleanup.
* `internal/tmdb/client.go`: постер записується у temporary-файл і з'являється за фінальним шляхом лише після успішного непорожнього download та atomic rename.
* Додано `internal/scanner/scanner_test.go` і `internal/tmdb/poster_test.go`. Повний набір тестів стабільно проходить у 10 послідовних прогонах.

### Audit Round 17 Fix Patch — Липень 2026 (StopScan race fix)

* `app.go` / `RunScan()`: `ctx, cancel` і `a.setScanCancel(cancel)` перенесено перед
  `a.wg.Add(1)` — поза горутиною. Усуває race: `StopScan()` міг викликатись до
  реєстрації cancel у горутині → `cancelScan()` був silent no-op → скан не зупинявся.
* `app.go` / `StopScan()`: Додано перевірку `scanCancel != nil` під `scanMutex`. Якщо
  активного сканування немає — попередження `"⚠️ [СТОП] Немає активного сканування"`
  замість хибного `"🚨 Сигнал отримано"`.

---

### Стан на 3 жовтня 2026

Знімок AGENTS.md фіксував синтетичне розпізнавання Groq через GPT-OSS 120B/20B, переклад через 120B і HTTP 429 `request_too_large` для повної Qwen-пачки на тодішньому обліковому записі (1426 токенів за ліміту 1000). У ньому також залишалися відкритими повний production-скан, ручний Wails-прогін і перевірки кількох дисків. Це історичні результати, повідомлені в попередній документації.
