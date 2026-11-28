# Локальна розробка й перевірка

Виконуйте команди з кореня Git-репозиторію. Вимоги поточного checkout беріть із [go.mod](../../go.mod) та [frontend/package.json](../../frontend/package.json). Зафіксовано Go `1.27.1`, Wails module `v2.16.0`, Node `^20.19.0 || >=22.12.0`, npm і Vite `^8.3.3`. Для Wails CLI використовуйте версію модуля. Ці значення описують manifests. Для діагностики фактично встановлених інструментів див. [movielist-build](../../.agents/skills/movielist-build/SKILL.md).

```powershell
go version
node --version
npm --version
Get-Command wails
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
.\build.ps1 -Dev
.\build.ps1
```

Виконуйте install лише для підготовки інструментів. `-Dev` запускає Wails та dev server. Production wrapper викликає `wails build`, який використовує npm-команди з `wails.json`. Вихідний EXE — `build/bin/movielist-app.exe`. Clean/rebuild і діагностика сумісності описані в скілі. [build.ps1](../../build.ps1), [wails.json](../../wails.json), [movielist-build](../../.agents/skills/movielist-build/SKILL.md)

## Версія та генеровані файли

- Єдине редаговане джерело версії — [VERSION](../../internal/version/VERSION). `internal/version` вбудовує його в EXE. Wails title, `GetAppVersion`, `app_started`, About і нові вітрини читають цю версію. Runtime `.env` не має version override. [version.go](../../internal/version/version.go), [main.go](../../main.go), [app.go](../../app.go#L252), [generator.go](../../internal/web/generator.go)
- Wrapper синхронізує `wails.json` → `info.productVersion`, version-поля у `frontend/package.json` та `frontend/package-lock.json`, і `<title>` у `frontend/index.html` перед Wails. npm `prebuild` та `predev` викликають цей самий скрипт. Він приймає `major.minor.patch` з компонентами 0–65535 і без зайвих початкових нулів. `npm --prefix frontend test` тестує sync у тимчасовій директорії. [sync-version.mjs](../../scripts/sync-version.mjs), [sync-version.test.mjs](../../scripts/sync-version.test.mjs)
- `frontend/wailsjs` — згенеровані tracked bindings, `frontend/dist` і `build/bin` — ігноровані результати збірки. Кореневий `index.html` генерує окремий workflow GitHub Pages. [main.go](../../main.go), [.gitignore](../../.gitignore), [WAILS_UI_CONTRACTS.md](../architecture/WAILS_UI_CONTRACTS.md)

## Налаштування, логи й експорт

- [Шаблон .env](../../.env.example) і `config.Load` задають ключі та defaults. `Load` пробує `.env` поруч з EXE, далі `.env` у робочій директорії. За відсутності файла редактор може створити його в обраному `EnvPath`. `GetEnvConfig` і `SaveEnvConfig` звертаються лише до цього шляху. SHA-256 revision відхиляє зовнішню зміну, валідатор повертає загальне повідомлення, запис зберігає коментарі, нормалізує CRLF та BOM і замінює файл через temp/sync/rename. Клієнти й env не оновлюються на місці. Після перезапуску збережені папки та моделі мають пріоритет. [config.go](../../internal/config/config.go#L39), [app_env.go](../../app_env.go), [app_env_test.go](../../app_env_test.go)
- `LOG_LEVEL` застосовується після `Load`: `info`, `debug`, `warn`, `error`; невідоме значення дає INFO. Logger пише `logs/app.jsonl` поруч з EXE, з fallback на робочу директорію, якщо `os.Executable` не спрацював. Wails `OpenLogs` обчислює `logs` від робочої директорії, тому звіряйте фактичний лог потрібного запуску, trace і події збереження. Джерело налаштування документуйте за шаблоном та кодом, без читання секретів. [main.go](../../main.go), [logger.go](../../internal/utils/logger.go#L40), [OpenLogs](../../app.go#L316), [movielist-log-analysis](../../.agents/skills/movielist-log-analysis/SKILL.md)
- Локальна вітрина використовує `HTML_PATH` (типово `local_index.html`) й локальні постери, мобільна використовує CDN. `SyncToCloud` потребує налаштованого `GOOGLE_SHEET_URL` і локального `credentials.json`. Sheets спочатку очищає аркуш і потім записує дані. GitHub Pages використовує системний Git та його налаштування доступу. Кнопки експорту запускають ці дії явно. [generator.go](../../internal/web/generator.go), [sheets.go](../../internal/sheets/sheets.go#L24), [app.go](../../app.go#L403)

## Вибір перевірки

| Зміна | Пропорційна перевірка |
| --- | --- |
| Лише документація | `git status --short`, diff/stat, `git diff --check`, відносні посилання, символи, UTF-8 без BOM, LF, відсутність секретів. Для зміненого скіла додатково перевірити його формат за скілом. |
| Go | `gofmt` для змінених Go-файлів, адресні `go test`/`go vet`; для shared contracts — `go test ./...` і `go vet ./...`. Якщо змінено Bridge, перевірити frontend. |
| Frontend | `npm --prefix frontend test` для JS, `npm --prefix frontend run build` для Vite, ручний Wails-сценарій за потреби. npm build запускає sync metadata. |
| Версія або інструменти | Для версії `go test ./internal/version`, `npm --prefix frontend test` і `.\build.ps1`. Для залежностей, CLI і розширеного набору див. скіли. |

[movielist-verify](../../.agents/skills/movielist-verify/SKILL.md), [movielist-build](../../.agents/skills/movielist-build/SKILL.md), [movielist-docs](../../.agents/skills/movielist-docs/SKILL.md)

Go suite має локальні fake transports/servers, тимчасові БД, stub диска, подій і Git. JS suite перевіряє модулі та sync-version через Node. Для живих запитів і локальної БД є окремі опт-іни, які слід залишати невстановленими в звичайних прогонах. [app_lifecycle_github_test.go](../../app_lifecycle_github_test.go), [gemini_test.go](../../internal/ai/gemini_test.go), [frontend/package.json](../../frontend/package.json)

Перевірки, які компілюють root package, потребують згенерованих assets у `frontend/dist`, бо `main.go` вбудовує цей каталог. У чистому clone, коли завдання потребує цих перевірок, підготуйте frontend через `npm --prefix frontend ci` та `npm --prefix frontend run build`. Враховуйте, що build запускає sync версії. [main.go](../../main.go#L18), [.gitignore](../../.gitignore), [frontend/package.json](../../frontend/package.json)

| Opt-in | Ефект |
| --- | --- |
| `MOVIELIST_LIVE_TMDB=1` | Реальні запити на окремих TMDB fixtures. [app_checklist_live_test.go](../../app_checklist_live_test.go) |
| `MOVIELIST_LIVE_GROQ=1` | Реальний каталог моделей. [app_groq_models_test.go](../../app_groq_models_test.go) |
| `MOVIELIST_LIVE_GROQ_INFERENCE=1` | Реальне синтетичне розпізнавання й переклад, витрата квоти. `MOVIELIST_LIVE_GROQ_MODELS` задає список для recognition. [groq_live_test.go](../../internal/ai/groq_live_test.go) |
| `MOVIELIST_CLEANUP_MODELS_DB` | Окремий опт-ін із шляхом до БД, який може змінити збережений вибір Gemini. Застосовуйте лише до явно погодженої БД. [app_checklist_live_test.go](../../app_checklist_live_test.go#L70) |

Живі запити, запуски на реальній медіатеці, експорт і публікація потребують окремого погодження. Для production-приймання потрібні конкретний Wails-сценарій або новий лог відповідного запуску. [DESIGN_CHECKLIST.md](../../DESIGN_CHECKLIST.md), [movielist-verify](../../.agents/skills/movielist-verify/SKILL.md)
