# Контракти Wails та UI

`main.go` прив’язує `App`. Ручні джерела — `app*.go`, `frontend/index.html`, `frontend/src/*` та шаблон HTML-вітрини в Go. Wails генерує `frontend/wailsjs` (`App.js`, `App.d.ts`, `models.ts` і runtime), Vite генерує `frontend/dist`. Змінюйте Go-сигнатури, JSON tags або ручний JS у джерелі, а bindings оновлюйте штатним Wails workflow. `index.html` у корені репозиторію — окрема мобільна вітрина. [main.go](../../main.go), [App.js](../../frontend/wailsjs/go/main/App.js), [App.d.ts](../../frontend/wailsjs/go/main/App.d.ts), [models.ts](../../frontend/wailsjs/go/models.ts), [wails.json](../../wails.json)

## Bridge

Для зміни API звіряйте exported method у Go, wrapper/тип у bindings та імпорт і call sites у `main.js`. Зберігайте сумісність, зокрема для експортованих методів, які поточний екран не викликає. Повний список є в [App.d.ts](../../frontend/wailsjs/go/main/App.d.ts), фактичне використання — у [main.js](../../frontend/src/main.js).

| Область | Методи та payload |
| --- | --- |
| Каталог і скан | `RunScan`, `StopScan`, `GetMovies`, `GetStats`, `DeleteMovie`. `GetStats` повертає `total`, `unrec`, `suspicious`, `last` (помилка читання може опустити `suspicious`). Поля `Movie` включають `filename`, `tmdb_id`, `media_type`, `title_ua`, `title_en`, `year`, `plot`, `genres`, `cast`, `poster_url`, `local_poster_path`, `file_label`, `file_path`, `vote_average`, `vote_count`, `recognition_source`, `recognition_confidence`, `verification_score`, `needs_review`, `review_reason`. Ключ для Bridge — `filename`. [app.go](../../app.go#L243), [Movie](../../internal/storage/storage.go#L19) |
| Редактор | `FixSelected([{filename,hint,media_type}])`, `UpdateMovie(filename,hint)`, `SearchTMDBCandidates({filename,title,media_type})`, `ConfirmTMDBCandidate` / `GetTMDBCandidateDetails({filename,tmdb_id,media_type})`, `RepairMetadata(filename)` → `{movie,warning?}`. `media_type` допускає `auto` для пошуку/уточнення, підтвердження використовує `movie` чи `tv`. Легкий результат кандидата має `tmdb_id`, `title`, `original_title`, `year`, `media_type`, `popularity`. `Exact` використовується всередині Go для ранжування. [app.go](../../app.go#L1844), [app_repair.go](../../app_repair.go), [models.go](../../internal/tmdb/models.go), [editor-state.js](../../frontend/src/editor-state.js) |
| Папки й конфігурація | `GetScanFolders` → `{root,folders,excluded}`. `SetScanFolders`, `SetExcludedFolders`, `SelectScanFolder`, `SelectExcludedFolder`, legacy `SelectMediaFolder`. `GetEnvConfig` → `{path,content,revision}`, `SaveEnvConfig(content,revision)` → нова ревізія. [app_folders.go](../../app_folders.go), [app_env.go](../../app_env.go), [folder-tables.js](../../frontend/src/folder-tables.js) |
| Моделі | `GetModelSelections` повертає map provider → `{current,configured}`. `SetProviderModels` зберігає вибір. `GetAIModelCatalog` і `GetGroqModelCatalog` повертають `current` / `available`. `GetGrokModelCatalog`, `GetAIModels`, `SetAIModels` також експортовані. Згенерована декларація посилається на `main.ProviderModels`, якого немає в `models.ts`. Поточний споживач — JavaScript. [app_models.go](../../app_models.go), [app_groq_models.go](../../app_groq_models.go), [App.d.ts](../../frontend/wailsjs/go/main/App.d.ts#L25), [models.ts](../../frontend/wailsjs/go/models.ts), [model-tables.js](../../frontend/src/model-tables.js) |
| Версія, експорт і відкриття | `GetAppVersion`, `OpenLogs`, `OpenURL`, `OpenShowcase`, `SyncToCloud`, `SyncToGitHub`, `OpenGoogleSheet`, `OpenGitHubRepo`, `OpenGitHubPage`, compatibility alias `OpenSheet`. UI формує TMDB-посилання за відомим HTTPS-host. [app.go](../../app.go#L283), [editor-state.js](../../frontend/src/editor-state.js) |

## Події

| Event | Payload і споживач |
| --- | --- |
| `scan-started` | Без payload. Скан або `FixSelected` запускає busy стан і STOP. |
| `scan-progress` | `{current,total,filename}`. UI показує прогрес поточного етапу. |
| `scan-finished` | Рядок повідомлення. UI знімає busy, припиняє таймер, перечитує каталог і статистику. Повернення promise від `RunScan` не замінює цю подію. |
| `log-message` | Рядок до «Активності». |
| `github-sync-started` / `github-sync-finished` | Початок без payload, завершення `{success,message}`. Керує станом експорту. |
| `movie-updated` | `{filename,tmdb_id}`. Фонове доповнення кандидата або Repair повідомляє UI про оновлення. |
| `wails:ready` | Runtime event, який слухає `main.js` для версії, статистики й каталогу. Є окрема ініціалізація через таймер. |

[App emitters](../../app.go#L2684), [Repair emitter](../../app_repair.go#L123), [EventsOn](../../frontend/src/main.js#L431), [Wails ready](../../frontend/src/main.js#L1376)

## Навігація та DOM

- `switchTab` керує Бібліотекою, Активністю, Редактором, переглядом та іншими панелями. «Експорт і синхронізація» (`btn-exports`) є основним пунктом. Папки й Налаштування закріплені внизу. Скан запускається з Бібліотеки, STOP доступний у стані скану. Навігація до експорту або моделей не викликає синхронізацію чи каталоги. About викликає `showModal`, обробляє кнопку, backdrop й Escape і повертає фокус. [main.js](../../frontend/src/main.js#L292), [about-dialog.js](../../frontend/src/about-dialog.js), [settings-menu.js](../../frontend/src/settings-menu.js)
- Редактор зберігає підказку, тип і вибір у стані, підтримує очищення пошуку через кнопку/Escape і зберігає інспектор при виході виправленого запису з review-фільтра. «За додаванням» використовує порядок каталогу. `metadata.js` фільтрує назви, жанри й акторів, `metadata-popover.js` керує локальними поповерами. Зовнішні дані в картках/редакторі вставляються через DOM API, наприклад `textContent`. Lightning Repair належить інспектору. [editor-state.js](../../frontend/src/editor-state.js), [main.js](../../frontend/src/main.js#L1044), [metadata.js](../../frontend/src/metadata.js), [metadata-popover.js](../../frontend/src/metadata-popover.js)
- `config-editor.js` керує draft, таймером автозбереження і flush на переході/blur/кнопці закриття програми. `folder-tables.js` та `model-tables.js` працюють з окремими таблицями і збереженим станом. Відповіді про помилку не повинні затирати незбережений draft. [config-editor.js](../../frontend/src/config-editor.js), [main.js](../../frontend/src/main.js#L286), [folder-tables.js](../../frontend/src/folder-tables.js), [model-tables.js](../../frontend/src/model-tables.js)
- Стабільні `noResults`, `filteredCount`, `filteredNum` належать шаблону **HTML-вітрини**. Вбудований JS звертається до них за ID. Для Wails перевіряйте ID на відповідній панелі в `main.js`, зокрема `btn-stop-scan` та `library-scan-status` для стану сканування. [generator.go](../../internal/web/generator.go#L455), [main.js](../../frontend/src/main.js#L431)

Node suite перевіряє формування payload, стани та фільтрацію редактора, налаштування, поповери, таблиці й About. Go-тести ін’єктують emitters і перевіряють порядок подій, cancellation й оновлення кандидата. Ручні Wails-сценарії відстежуються окремо. [test-runner.mjs](../../frontend/src/test-runner.mjs), [app_lifecycle_github_test.go](../../app_lifecycle_github_test.go), [app_candidates_test.go](../../app_candidates_test.go), [DESIGN_CHECKLIST.md](../../DESIGN_CHECKLIST.md)
