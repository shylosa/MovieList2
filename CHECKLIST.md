# CHECKLIST

## Bugfixes

- [x] **[app.go / FixSelected]** Додати захист `isScanning` на початку `FixSelected`: встановити `a.isScanning = true` під `a.scanMutex` і скинути у `defer`. Перед встановленням перевіряти: якщо `isScanning == true` — виходити з лог-повідомленням.

- [x] **[app.go / FixSelected → setScanCancel]** Перед викликом `a.setScanCancel(cancel)` — перевірити, чи не висить попередній `a.scanCancel`, і якщо так — викликати його, щоб не залишити сиротливу goroutine.

- [x] **[app.go / filterUnprocessed]** Замінити `a.db.GetAllMovies(a.ctx)` на `a.db.GetAllMovies(ctx)` — прийняти `ctx context.Context` як параметр методу. Оновити всі виклики `filterUnprocessed` у `RunScan`.

- [x] **[app.go / finalizeScan]** Додати параметр `ctx context.Context` до `finalizeScan`. Замінити `a.db.GetAllMovies(a.ctx)` на `a.db.GetAllMovies(ctx)`. Оновити всі виклики.

- [x] **[internal/utils/logger.go]** Зберегти `*os.File` у package-level змінній `var logFile *os.File`. У `CloseLogger` додати: `if logFile != nil { logFile.Sync(); logFile.Close() }`.

- [x] **[internal/tmdb/client.go / doRequest]** Прибрати ручний `_, _ = io.Copy(io.Discard, resp.Body)` після `Decode` — `defer resp.Body.Close()` вже є і дренує тіло при закритті. Якщо є бажання явно дренувати для keep-alive — перемістити `io.Copy` до `defer`, але після `Close`.

- [x] **[internal/storage/storage.go / CleanMissingMovies]** Після `for rows.Next() {...}` додати `if err := rows.Err(); err != nil { return 0, err }` для детектування помилок ітерації.

- [x] **[internal/storage/storage.go / InitSchema]** Розбити `PRAGMA journal_mode`, `PRAGMA synchronous`, `PRAGMA busy_timeout` на окремі `db.db.ExecContext(ctx, "PRAGMA ...")` виклики. Перевіряти `err` кожного.

- [x] **[internal/config/config.go / getEnvRequired]** Замінити `log.Panicf(...)` на `slog.Error("missing_required_env", slog.String("key", key)); panic(...)` для запису в `logs/app.jsonl`.

## Оптимізації

- [x] **[internal/ai/gemini.go / buildPrompt]** Замінити `json.MarshalIndent(contexts, "", "  ")` на `json.Marshal(contexts)` — прибрати відступи для зменшення кількості вхідних токенів.

- [x] **[internal/ai/gemini.go / buildPrompt]** Видалити з тексту промпту секції `FIELD RULES` пункти 2 (`title_ua`), 4 (`plot`), 5 (`genres`), 6 (`cast`) — ці поля відсутні у `buildGenAISchema`, і їх опис лише збиває модель та витрачає токени.

- [ ] **[internal/ai/gemini.go / TranslateBulk]** Перед `fmt.Errorf("всі моделі ... недоступні: %w", lastErr)` додати перевірку: `if lastErr == nil { lastErr = errors.New("невідома помилка") }`.

- [ ] **[app.go / GetAIModels]** Винести `http.Client` з `&http.Client{Timeout: 10 * time.Second}` у поле структури `App` (або singleton) замість створення нового клієнта при кожному виклику.

- [ ] **[app.go / needsTranslation]** Пом'якшити "сіру зону": замість `if !hasUkrainianLetter { return true }` — додати мінімальну умову: повертати `true` тільки якщо рядок довший за 5 символів і не містить специфічних українських літер. Для коротких назв (≤5 символів) — `return false`.

- [ ] **[internal/storage/storage.go / SaveMoviesBatch]** Додати коментар `// Partial save: errors per-row are logged but batch commit succeeds` для явного документування архітектурного рішення.
