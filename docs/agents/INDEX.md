# Індекс контексту для агента

Почніть з [AGENTS.md](../../AGENTS.md). Для конкретної задачі відкрийте один рядок нижче, потім код і тест, на які він посилається. За наскрізної зміни використовуйте [карту підсистем](../architecture/OVERVIEW.md).

| Задача | Що відкрити |
| --- | --- |
| Парсер, IMDb, TMDB, scoring, ручне уточнення, Repair | [Розпізнавання](../architecture/RECOGNITION.md), [координатор автоматичного розпізнавання R9-A](../../recognition_coordinator.go), [тести пошуку](../../internal/tmdb/search_test.go), [тести ручного уточнення](../../app_updatemovie_test.go) |
| Gemini, Groq, legacy Grok, quota, локалізація | [Розпізнавання](../architecture/RECOGNITION.md#ai-гіпотеза-та-перевірка), [ліміти та контексти](../architecture/STORAGE_LIFECYCLE.md#межі-запитів-у-коді), [збережений вибір моделей](../../app_models.go) |
| SQLite, джерела, STOP, shutdown, cleanup | [Сховище й життєвий цикл](../architecture/STORAGE_LIFECYCLE.md), [тести SQLite](../../internal/storage/storage_test.go), [тести lifecycle](../../app_lifecycle_github_test.go) |
| Wails API, навігація, події, Редактор | [Контракти Wails і UI](../architecture/WAILS_UI_CONTRACTS.md), [Node suite](../../frontend/src/test-runner.mjs), [ручні сценарії](../../DESIGN_CHECKLIST.md) |
| `.env`, папки й моделі | [Розробка](../operations/DEVELOPMENT.md#налаштування-логи-й-експорт), [контракти Wails](../architecture/WAILS_UI_CONTRACTS.md), [шаблон .env](../../.env.example) |
| Реліз, версія, build/dev | [Розробка](../operations/DEVELOPMENT.md), [movielist-build](../../.agents/skills/movielist-build/SKILL.md), [VERSION](../../internal/version/VERSION) |
| Вітрина, Sheets, GitHub Pages | [Розробка](../operations/DEVELOPMENT.md#налаштування-логи-й-експорт), [життєвий цикл](../architecture/STORAGE_LIFECYCLE.md#скасування-й-зовнішні-ефекти), [тести GitHub](../../app_lifecycle_github_test.go) |
| Лог конкретного запуску | [movielist-log-analysis](../../.agents/skills/movielist-log-analysis/SKILL.md), [розпізнавання](../architecture/RECOGNITION.md) або [життєвий цикл](../architecture/STORAGE_LIFECYCLE.md) за подією |
| Вибір перевірок або зміна документації | [movielist-verify](../../.agents/skills/movielist-verify/SKILL.md), [movielist-docs](../../.agents/skills/movielist-docs/SKILL.md), [матриця перевірок](../operations/DEVELOPMENT.md#вибір-перевірки) |
| Явно запитане рев’ю | [reviewer](../../.codex/agents/reviewer.toml) або [deep-reviewer](../../.codex/agents/deep-reviewer.toml) з [однойменним workflow](../../.agents/skills/deep-reviewer/SKILL.md) |

[README](../../README.md) — вхід для користувача та розробника. [CHANGELOG](../../CHANGELOG.md) — історія реалізації та попередніх перевірок. `CHECKLIST.md` ігнорується Git і доступний лише там, де його ведуть локально. Для конкретного production-сценарію звіряйте поточне завдання й локальні докази. [Правила Git](../../.gitignore)

Архівні [Round 25 handoff](../../ROUND25_HANDOFF.md), [Deep Review checklist](../../DEEP_REVIEW_CHECKLIST.md), [implementation plan](../../implementation_plan.md) та [датований аудит контексту](../reports/AGENT_CONTEXT_BASELINE.md) відкривайте для історичних питань або перевірки походження висновку.
