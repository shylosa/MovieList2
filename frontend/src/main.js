import './style.css';

import { GetAppVersion, GetMovies, GetStats, RunScan, StopScan, GetAIModelCatalog, SetAIModels, OpenLogs, FixSelected, SearchTMDBCandidates, ConfirmTMDBCandidate, GetTMDBCandidateDetails, SyncToCloud, SyncToGitHub, OpenShowcase, OpenSheet, OpenGoogleSheet, OpenGitHubRepo, OpenGitHubPage, OpenURL, DeleteMovie, SelectMediaFolder } from '../wailsjs/go/main/App.js';
import { Quit, WindowMinimise, WindowToggleMaximise, EventsOn } from '../wailsjs/runtime/runtime.js';
import logoUrl from './assets/images/appicon.png';
import noPosterUrl from './assets/images/no-poster.jpg';
import { candidateConfirmPayload, candidateSearchPayload, fixPayload, reviewReasonLabel, mediaTypeLabel, candidateTMDBURL, formatRuntime, formatTMDBRating, createRequestGate, createAsyncVisibilityGate, candidateStatus, scanLifecycleTransition, cacheEditorValue, floatingPopoverPosition } from './editor-state.js';

document.querySelector('#app').innerHTML = `
  <div class="titlebar">
      <div class="titlebar-title" style="display: flex; align-items: center;">
          <img src="${logoUrl}" alt="logo" style="width: 16px; height: 16px; margin-right: 8px; border-radius: 4px; pointer-events: none;">
          MovieList <span id="app-version" style="margin-left: 8px; color: var(--text-dim); font-size: 11px;">?</span>
      </div>
      <div class="titlebar-controls">
          <div class="control-btn" id="btn-min">─</div>
          <div class="control-btn" id="btn-max">□</div>
          <div class="control-btn close" id="btn-close">✕</div>
      </div>
  </div>

  <div class="layout">
    <div class="sidebar">
        <div class="sidebar-header">
            <div class="header-flag" id="current-page-title">MovieList</div>
        </div>

        <div style="padding-top: 10px; flex-grow: 1;">
            <div class="nav-btn active" id="btn-library"><span class="nav-icon">▦</span> Бібліотека</div>
            <div class="nav-btn" id="btn-overview"><span class="nav-icon">◫</span> Огляд і журнал</div>
            <div class="nav-btn" id="btn-review"><span class="nav-icon">△</span> Потребують перевірки <span id="nav-review-count" class="nav-count" hidden></span></div>
            <div class="nav-btn" id="btn-scan"><span class="nav-icon">⟳</span> Сканувати</div>
            <div class="sidebar-section">Інструменти</div>
            <div class="toolbar-row">
                <div class="nav-btn" id="btn-sync"><span class="nav-icon">☁</span> Google Sheets</div>
                <div class="nav-btn icon-btn" id="btn-open-sheet" title="Відкрити таблицю">
                    <span class="nav-icon">📊</span>
                </div>
            </div>
            <div class="toolbar-row github-toolbar">
                <div class="nav-btn" id="btn-sync-github">
                    <span class="nav-icon">↗</span><span class="nav-label">GitHub Pages</span>
                    <span class="nav-spinner" aria-hidden="true"></span>
                </div>
                <div class="nav-btn icon-btn" id="btn-open-project" title="Відкрити репозиторій">
                    <span class="nav-icon">💻</span>
                </div>
                <div class="nav-btn icon-btn" id="btn-open-page" title="Відкрити GitHub Pages">
                    <span class="nav-icon">🌐</span>
                </div>
            </div>
            <div class="nav-btn" id="btn-showcase"><span class="nav-icon">▣</span> Вітрина</div>

            <div class="nav-btn" id="btn-editor"><span class="nav-icon">✎</span> Редактор</div>
            <div class="nav-btn" id="btn-models"><span class="nav-icon">✦</span> Моделі ШІ</div>
            <div class="nav-btn" id="btn-select-folder"><span class="nav-icon">▱</span> Вибрати папку</div>
            <div class="nav-btn" id="btn-logs"><span class="nav-icon">☷</span> Папка з логами</div>
        </div>

        <div class="sidebar-footer">
            © 2026 <a href="#" onclick="window.go.main.App.OpenGitHubRepo(); return false;">shylosa</a>
        </div>
    </div>
    <div class="main-area">
        <div id="panel-library" class="panel active">
            <div class="library-heading">
                <div><h1>Моя бібліотека</h1></div>
                <div class="library-heading-actions">
                    <input id="library-search" type="search" placeholder="Пошук фільмів і серіалів…" aria-label="Пошук у бібліотеці">
                    <button id="library-scan" class="primary-button" type="button">Сканувати</button>
                </div>
            </div>
            <div class="library-stats">
                <div class="library-stat"><span>Усього</span><strong id="library-total">—</strong></div>
                <div class="library-stat"><span>Фільми</span><strong id="library-movies">—</strong></div>
                <div class="library-stat"><span>Серіали</span><strong id="library-series">—</strong></div>
                <div class="library-stat review-stat"><span>Потребують перевірки</span><strong id="library-review">—</strong></div>
            </div>
            <div id="library-scan-status" class="library-scan-status" hidden>
                <div class="library-scan-copy"><strong id="library-scan-label">Сканування…</strong><span id="library-scan-file">Пошук файлів і метаданих…</span></div>
                <div class="library-progress-track"><div id="library-progress-fill"></div></div>
                <button id="library-stop" type="button">Зупинити</button>
            </div>
            <div class="library-controls">
                <div class="library-filters" role="group" aria-label="Фільтр бібліотеки">
                    <button class="filter-chip active" data-filter="all" type="button">Усі</button>
                    <button class="filter-chip" data-filter="movie" type="button">Фільми</button>
                    <button class="filter-chip" data-filter="tv" type="button">Серіали</button>
                    <button class="filter-chip" data-filter="no-poster" type="button">Без постера</button>
                </div>
                <label class="library-sort-label">Сортувати: <select id="library-sort"><option value="added">За датою додавання</option><option value="title">За назвою</option><option value="year">За роком</option><option value="rating">За рейтингом</option></select></label>
            </div>
            <div id="library-grid" class="library-grid" aria-live="polite"></div>
            <div class="library-footer"><span id="library-last-scan">Останнє сканування: —</span></div>
        </div>
        <div id="panel-movie" class="panel movie-view">
            <div class="movie-view-top">
                <button id="movie-back" class="movie-back" type="button" aria-label="Повернутися до бібліотеки" title="Повернутися до бібліотеки">
                    <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M19 12H5m7 7-7-7 7-7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
                </button>
                <div class="movie-view-actions">
                    <button id="movie-tmdb" type="button" hidden>Відкрити в TMDB ↗</button>
                    <button id="movie-edit" class="primary-button" type="button">Редагувати запис</button>
                </div>
            </div>
            <div class="movie-view-body">
                <div class="movie-view-art"><img id="movie-poster" alt="" /></div>
                <div class="movie-view-info">
                    <div id="movie-status" class="movie-status"></div>
                    <h1 id="movie-title"></h1>
                    <p id="movie-original-title" class="movie-original-title" hidden></p>
                    <div id="movie-facts" class="movie-facts"></div>
                    <section class="movie-section"><h2>Про фільм</h2><p id="movie-plot"></p></section>
                    <section id="movie-cast-section" class="movie-section" hidden><h2>У ролях</h2><p id="movie-cast"></p></section>
                    <section class="movie-section movie-file-section"><h2>Файл у колекції</h2><strong id="movie-file-label"></strong><p id="movie-file-path"></p></section>
                </div>
            </div>
        </div>
        <div id="panel-overview" class="panel">
            <div class="cards">
                <div class="card">
                    <div class="card-title">ФАЙЛІВ У БАЗІ</div>
                    <div class="card-val" id="val-total">—</div>
                    <div class="card-sub">у базі даних</div>
                </div>
                <div class="card">
                    <div class="card-title">СТАТУС</div>
                    <div class="card-val" id="val-unrec">—</div>
                    <div class="card-sub" id="sub-unrec">Завантаження...</div>
                </div>
                <div class="card">
                    <div class="card-title">ОСТАННІЙ СКАН</div>
                    <div class="card-val" id="val-last">—</div>
                </div>
            </div>

            <div id="scan-progress-area" class="scan-progress-area">
                <div class="progress-wrap" id="progress-wrap">
                    <div id="progress-bar" class="progress-bar"></div>
                </div>

                <button type="button" id="btn-stop-scan" class="stop-btn disabled" title="Зупинити сканування">
                    <svg viewBox="0 0 24 24" width="24" height="24">
                        <circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="2" fill="none" />
                        <rect x="8" y="8" width="8" height="8" fill="currentColor" />
                    </svg>
                </button>
            </div>

            <div class="console-outer">
                <div class="console-header">
                    <span>Консоль виконання</span>
                    <div style="display: flex; gap: 10px; align-items: center;">
                        <span id="scan-timer" style="font-family: monospace; font-size: 14px; color: #4ade80;">00:00</span>
                        <span id="console-status">Очікування...</span>
                    </div>
                </div>
                <div class="console-body" id="console-body"><div id="welcome-message">Вітаю у MovieList! Система готова до роботи.</div></div>
            </div>
        </div>

        <div id="panel-editor" class="panel">
            <div class="editor-toolbar">
                <div class="editor-search">
                    <input type="text" id="search-input" placeholder="Пошук..." />
                    <button id="search-clear" type="button" aria-label="Очистити пошук" title="Очистити пошук" hidden>✕</button>
                </div>
                <label><input type="checkbox" id="review-only" /> Потребують перевірки</label>
                <div style="display: flex; gap: 10px;">
                    <button id="btn-fix" style="cursor: pointer;">✨ Виправити вибрані</button>
                    <button id="btn-delete-selected" class="btn-danger">🗑 Видалити вибрані</button>
                </div>
            </div>
            <div class="editor-header">
                <div class="col-cb"></div>
                <div class="col-file">Файл</div>
                <div class="col-arr"></div>
                <div class="col-year">Рік</div>
                <div class="col-title">Розпізнано як</div>
                <div class="col-media-type">Тип</div>
                <div class="col-hint">Підказка</div>
            </div>
            <div id="movie-list" style="overflow-y: scroll; flex-grow: 1;">Завантаження...</div>
        </div>
    </div>
  </div>
`;

const btnScan = document.getElementById('btn-scan');
const btnStop = document.getElementById('btn-stop-scan');
const libraryScan = document.getElementById('library-scan');

// ЗАМОК: Змінна, що стежить, чи йде зараз сканування
let isScanning = false;

function setStopButtonState(state) {
    if (btnStop) btnStop.className = `stop-btn ${state}`;
}

setStopButtonState('disabled'); // Початковий стан

// 🔴 Клік по кнопці СТОП
btnStop.addEventListener('click', async (e) => {
    e.stopPropagation(); // КРИТИЧНО: Блокує натискання елементів під кнопкою!

    if (!isScanning) return; // Якщо не скануємо, кнопка не працює

    setStopButtonState('stopping'); // Стає Жовтою

    try {
        await StopScan(); // Відправляємо сигнал в Go
    } catch (err) {
        console.error("Помилка при спробі зупинки:", err);
    }
});
document.getElementById('library-stop').addEventListener('click', () => btnStop.click());
libraryScan.addEventListener('click', () => btnScan.click());

// 🟢 Клік по кнопці Сканувати
btnScan.addEventListener('click', async (e) => {
    e.preventDefault();
    e.stopPropagation();

    if (isScanning) return; // КРИТИЧНО: Захист від подвійного запуску
    isScanning = true;
    btnScan.classList.add('disabled');

    switchTab('library', 'Бібліотека');
    setStopButtonState('active'); // Стає червоною

    document.getElementById('progress-bar').style.width = '0%';

    try {
        await RunScan();
    } catch (err) {
        console.error("Помилка сканування:", err);
        isScanning = false;
        btnScan.classList.remove('disabled');
        setStopButtonState('disabled');
    }
});

// --- КЕРУВАННЯ ВІКНОМ ---
document.getElementById('btn-min').onclick = WindowMinimise;
document.getElementById('btn-max').onclick = WindowToggleMaximise;
document.getElementById('btn-close').onclick = Quit;

// --- НАВІГАЦІЯ ТА КНОПКИ ---
const switchTab = (tab, title) => {
    document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
    document.querySelectorAll('.nav-btn').forEach(b => b.classList.remove('active'));

    document.getElementById(`panel-${tab}`).classList.add('active');
    document.getElementById(`btn-${tab}`)?.classList.add('active');

    document.getElementById('current-page-title').innerText = title;

    const flag = document.getElementById('current-page-title');
    flag.innerText = title;
};

// Прив'язка кнопок
document.getElementById('btn-library').onclick = () => { switchTab('library', 'Бібліотека'); loadMovies(); };
document.getElementById('btn-overview').onclick = () => switchTab('overview', 'Огляд');
document.getElementById('btn-review').onclick = () => {
    document.getElementById('search-input').value = '';
    document.getElementById('review-only').checked = true;
    switchTab('editor', 'Потребують перевірки');
    document.getElementById('btn-editor').classList.remove('active');
    document.getElementById('btn-review').classList.add('active');
    loadMovies();
};
document.getElementById('btn-sync').onclick = () => {
    switchTab('overview', 'Sync Sheets');
    SyncToCloud();
};

const btnSyncGitHub = document.getElementById('btn-sync-github');
let isGitHubSyncing = false;

function setGitHubSyncBusy(busy) {
    isGitHubSyncing = busy;
    if (!btnSyncGitHub) return;
    btnSyncGitHub.classList.toggle('disabled', busy);
    btnSyncGitHub.classList.toggle('syncing', busy);
}

btnSyncGitHub.onclick = () => {
    if (isGitHubSyncing) return;
    switchTab('overview', 'GitHub Pages');
    SyncToGitHub();
};
document.getElementById('btn-open-sheet').onclick = () => {
    OpenGoogleSheet();
};

const btnOpenProject = document.getElementById('btn-open-project');
if (btnOpenProject) {
    btnOpenProject.onclick = () => {
        OpenGitHubRepo();
    };
}

const btnOpenPage = document.getElementById('btn-open-page');
if (btnOpenPage) {
    btnOpenPage.onclick = () => {
        OpenGitHubPage();
    };
}

document.getElementById('btn-showcase').onclick = () => {
    switchTab('overview', 'Вітрина');
    OpenShowcase();
};
document.getElementById('btn-editor').onclick = () => {
    document.getElementById('review-only').checked = false;
    switchTab('editor', 'Редактор');
    loadMovies();
};

document.getElementById('btn-logs').onclick = OpenLogs;

document.getElementById('btn-select-folder').onclick = async () => {
    try {
        const path = await SelectMediaFolder();
        if (path) {
            logToConsole(`📁 Вибрана папка: ${path}`, "log-info");
        }
    } catch (err) {
        logToConsole(`❌ Помилка вибору папки: ${err}`, "log-error");
    }
};

document.getElementById('btn-models').onclick = async () => {
    switchTab('overview', 'Моделі ШІ');
	consoleBody.replaceChildren();
    document.getElementById('console-status').innerText = "Запит до API...";
	const loading = document.createElement('div'); loading.textContent = 'Отримання списку моделей Gemini…'; consoleBody.appendChild(loading);
    try {
		const catalog = await GetAIModelCatalog();
		consoleBody.replaceChildren();
		const currentTitle = document.createElement('h3'); currentTitle.textContent = 'Поточні моделі';
		const currentList = document.createElement('div'); currentList.className = 'model-current-list';
		for (const name of catalog.current || []) { const item = document.createElement('div'); item.textContent = name; currentList.appendChild(item); }
		const availableTitle = document.createElement('h3'); availableTitle.textContent = 'Доступні через Gemini API';
		const form = document.createElement('div'); form.className = 'model-picker';
		const current = new Set((catalog.current || []).filter(name => !name.includes(' (fallback)')));
		for (const name of catalog.available || []) {
			const label = document.createElement('label');
			const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.value = name; checkbox.checked = current.has(name);
			const text = document.createElement('span'); text.textContent = name;
			label.append(checkbox, text); form.appendChild(label);
		}
		const save = document.createElement('button'); save.type = 'button'; save.textContent = 'Зберегти вибір'; save.className = 'model-save';
		save.onclick = async () => {
			const selected = Array.from(form.querySelectorAll('input:checked'), input => input.value);
			try { await SetAIModels(selected); document.getElementById('console-status').innerText = 'Вибір збережено'; }
			catch (error) { document.getElementById('console-status').innerText = `Помилка: ${error}`; }
		};
		consoleBody.append(currentTitle, currentList, availableTitle, form, save);
        document.getElementById('console-status').innerText = "Готово";
    } catch (e) {
        logToConsole("❌ Помилка: " + e, "log-warn");
        document.getElementById('console-status').innerText = "Помилка";
    }
};

// --- ФУНКЦІЇ ДЛЯ КОНСОЛІ ТА ПРОГРЕСУ ---
const consoleBody = document.getElementById('console-body');
// 🟢 ЗМІНА 1: Тепер ми керуємо новим загальним контейнером (де є і смужка, і кнопка)
const pbArea = document.getElementById('scan-progress-area');
const pb = document.getElementById('progress-bar');
const cStatus = document.getElementById('console-status');

function logToConsole(text, className = "") {
    const line = document.createElement('div');
    if (className) line.className = className;
    line.innerText = text;
    consoleBody.appendChild(line);
    consoleBody.scrollTop = consoleBody.scrollHeight;
}

// --- ПРИЙОМ ПОДІЙ ВІД GO (МАГІЯ WAILS) ---
let scanTimerInterval;
let scanStartTime;

EventsOn('scan-started', () => {
	isScanning = scanLifecycleTransition(isScanning, 'scan-started');
    document.getElementById('library-scan-status').hidden = false;
    document.getElementById('library-scan-label').textContent = 'Сканування…';
    document.getElementById('library-scan-file').textContent = 'Пошук файлів і метаданих…';
    document.getElementById('library-progress-fill').style.width = '0%';
    libraryScan.disabled = true;
    consoleBody.innerHTML = ""; // Чистимо консоль
    logToConsole("🚀 Запуск процесу...");

    // 🟢 ЗМІНА 2: Використовуємо "flex", щоб кнопка СТОП рівно стояла праворуч
    pbArea.style.display = "flex";
    pb.style.width = "0%";
    cStatus.innerText = "У процесі...";
    document.getElementById("scan-timer").innerText = "00:00";
    scanStartTime = Date.now();

    scanTimerInterval = setInterval(() => {
        const diffInSeconds = Math.floor((Date.now() - scanStartTime) / 1000);
        const m = String(Math.floor(diffInSeconds / 60)).padStart(2, '0');
        const s = String(diffInSeconds % 60).padStart(2, '0');
        document.getElementById("scan-timer").innerText = `${m}:${s}`;
    }, 1000);

    // 🟢 ЗМІНА 3: Робимо кнопку СТОП активною (червоною) на старті
    const btnStop = document.getElementById('btn-stop-scan');
    if (btnStop) btnStop.className = "stop-btn active";

    document.getElementById('btn-scan').style.pointerEvents = "none";
    document.getElementById('btn-scan').style.opacity = "0.5";
});

EventsOn('scan-progress', (data) => {
    // data містить { current, total, filename } з нашого app.go
    const percent = data.total > 0 ? Math.min(100, data.current / data.total * 100) : 0;
    pb.style.width = percent + "%";
    document.getElementById('library-progress-fill').style.width = percent + '%';
    document.getElementById('library-scan-label').textContent = `Сканування: ${Math.round(percent)}% · ${data.current} із ${data.total}`;
    document.getElementById('library-scan-file').textContent = data.filename || 'Обробка файлів…';
    logToConsole(`[${data.current}/${data.total}] Обробка: ${data.filename}`);
});

EventsOn('log-message', (msg) => {
    logToConsole(msg);
});

EventsOn('github-sync-started', () => {
    switchTab('overview', 'GitHub Pages');
    setGitHubSyncBusy(true);
    cStatus.innerText = 'GitHub Pages...';
    logToConsole('📱 Синхронізація мобільної вітрини...');
});

EventsOn('github-sync-finished', (data) => {
    setGitHubSyncBusy(false);
    const success = data && data.success;
    const message = (data && data.message) ? data.message : (success ? '✅ Готово' : '❌ Помилка синхронізації');
    logToConsole(message, success ? 'log-success' : 'log-warn');
    cStatus.innerText = success ? 'Готово' : 'Помилка';
});

EventsOn('scan-finished', (msg) => {
	isScanning = scanLifecycleTransition(isScanning, 'scan-finished');
    document.getElementById('library-scan-status').hidden = true;
    libraryScan.disabled = false;
    clearInterval(scanTimerInterval);
    logToConsole(`\n✅ ${msg}`, "log-success");
    cStatus.innerText = "Готово";

    // 🟢 ЗМІНА 4: Робимо кнопку СТОП знову сірою після завершення (або скасування)
    const btnStop = document.getElementById('btn-stop-scan');
    if (btnStop) btnStop.className = "stop-btn disabled";
	document.getElementById('btn-scan').classList.remove('disabled');

    document.getElementById('btn-scan').style.pointerEvents = "auto";
    document.getElementById('btn-scan').style.opacity = "1";
    loadStats(); // Оновлюємо картки
    loadMovies();
});

EventsOn('movie-updated', (data) => {
	const filename = data && data.filename;
	if (filename) loadMovies(filename);
	loadStats();
});


// --- ЗАВАНТАЖЕННЯ ДАНИХ ---
async function loadStats() {
    try {
        console.log("📊 Завантаження статистики...");
        const stats = await GetStats();
        console.log("📊 Статистика отримана:", stats);

        document.getElementById('val-total').innerText = stats.total;

        // ⬅️ ДОДАНО: Спочатку перевіряємо, чи є взагалі файли
        if (stats.total === 0 || stats.total === "0") {
            document.getElementById('val-unrec').innerText = "📭 База порожня";
            document.getElementById('val-unrec').style.color = "var(--text-dim)"; // Робимо текст сірим
            document.getElementById('sub-unrec').innerText = "Оновіть базу";
        }
        // Якщо файли є, але є нерозпізнані
        else if (stats.unrec > 0) {
            document.getElementById('val-unrec').innerText = `⚠ ${stats.unrec}`;
            document.getElementById('val-unrec').style.color = "var(--warn-yellow)";
            document.getElementById('sub-unrec').innerText = `Нерозпізнані: ${stats.unrec}; сумнівні: ${stats.suspicious || 0}`;
        }
        // Якщо файли є і всі розпізнані успішно
        else {
            document.getElementById('val-unrec').innerText = "✓ Всі розпізнані";
            document.getElementById('val-unrec').style.color = "var(--ok-green)";
            document.getElementById('sub-unrec').innerText = "База актуальна";
        }

        document.getElementById('val-last').innerText = stats.last;
        document.getElementById('library-total').textContent = Number(stats.total || 0).toLocaleString('uk-UA');
        const reviewCount = Number(stats.unrec || 0) + Number(stats.suspicious || 0);
        document.getElementById('library-review').textContent = reviewCount.toLocaleString('uk-UA');
        const badge = document.getElementById('nav-review-count');
        badge.textContent = reviewCount;
        badge.hidden = reviewCount === 0;
        document.getElementById('library-last-scan').textContent = `Останнє сканування: ${stats.last || '—'}`;
    } catch (e) {
        console.error("❌ Помилка при завантаженні статистики:", e);
    }
}

// --- ЛОГІКА РЕДАКТОРА ---
let allMovies = []; // Зберігаємо список глобально для швидкого пошуку
let libraryFilter = 'all';

function needsReview(movie) { return !movie.tmdb_id || movie.needs_review; }

function safePosterURL(value) {
    try {
        const url = new URL(value);
        return url.protocol === 'https:' && url.hostname === 'image.tmdb.org' ? url.href : noPosterUrl;
    } catch { return noPosterUrl; }
}

let activeMovieFilename = '';
let movieReturnTab = 'library';

function renderMovieView(movie) {
    const title = movie.title_ua || movie.title_en || movie.file_label || 'Невідомий запис';
    const poster = document.getElementById('movie-poster');
    poster.src = safePosterURL(movie.poster_url);
    poster.alt = `Постер: ${title}`;
    poster.onerror = () => { poster.onerror = null; poster.src = noPosterUrl; };
    document.getElementById('movie-title').textContent = title;
    const original = document.getElementById('movie-original-title');
    original.textContent = movie.title_en || '';
    original.hidden = !movie.title_en || movie.title_en === title;
    const status = document.getElementById('movie-status');
    status.textContent = needsReview(movie) ? 'Потребує перевірки' : 'У бібліотеці';
    status.classList.toggle('needs-review', needsReview(movie));
    const facts = document.getElementById('movie-facts');
    facts.replaceChildren();
    const items = [
        {text: movie.media_type === 'tv' ? 'Серіал' : movie.media_type === 'movie' ? 'Фільм' : 'Тип невідомий', kind: 'type'},
        {text: movie.year || 'Рік невідомий', kind: 'year'},
    ];
    if (movie.vote_count > 0) items.push({text: `★ ${Number(movie.vote_average).toFixed(1)} TMDB`, kind: 'rating'});
    if (movie.genres) items.push({text: movie.genres, kind: 'genres'});
    for (const item of items) {
        const chip = document.createElement('span');
        chip.className = `movie-fact-${item.kind}`;
        chip.textContent = item.text;
        facts.appendChild(chip);
    }
    document.querySelector('.movie-section h2').textContent = movie.media_type === 'tv' ? 'Про серіал' : 'Про фільм';
    document.getElementById('movie-plot').textContent = movie.plot || 'Опис поки що відсутній.';
    const cast = document.getElementById('movie-cast-section');
    document.getElementById('movie-cast').textContent = movie.cast || '';
    cast.hidden = !movie.cast;
    document.getElementById('movie-file-label').textContent = movie.file_label || movie.filename;
    document.getElementById('movie-file-path').textContent = movie.filename;
    const tmdbButton = document.getElementById('movie-tmdb');
    const validType = movie.media_type === 'tv' || movie.media_type === 'movie';
    tmdbButton.hidden = !(movie.tmdb_id > 0 && validType);
    tmdbButton.onclick = () => { if (!tmdbButton.hidden) OpenURL(`https://www.themoviedb.org/${movie.media_type}/${movie.tmdb_id}`); };
}

function showMovieView(movie, returnTab = 'library') {
    activeMovieFilename = movie.filename;
    movieReturnTab = returnTab;
    renderMovieView(movie);
    const backLabel = returnTab === 'editor' ? 'Повернутися до редактора' : 'Повернутися до бібліотеки';
    document.getElementById('movie-back').setAttribute('aria-label', backLabel);
    document.getElementById('movie-back').title = backLabel;
    switchTab('movie', 'Перегляд');
    document.getElementById(returnTab === 'editor' ? 'btn-editor' : 'btn-library').classList.add('active');
}

document.getElementById('movie-back').onclick = () => switchTab(movieReturnTab, movieReturnTab === 'editor' ? 'Редактор' : 'Бібліотека');
document.getElementById('movie-edit').onclick = () => {
    document.getElementById('review-only').checked = false;
    document.getElementById('search-input').value = activeMovieFilename;
    switchTab('editor', 'Редактор');
    renderFilteredMovies();
    requestAnimationFrame(() => document.querySelector('.movie-row')?.scrollIntoView({block: 'center'}));
};

function renderLibrary() {
    const grid = document.getElementById('library-grid');
    const query = document.getElementById('library-search').value.trim().toLocaleLowerCase('uk-UA');
    const movies = allMovies.filter(movie => {
        const matchesType = libraryFilter === 'all' || (libraryFilter === 'no-poster' ? !movie.poster_url : movie.media_type === libraryFilter);
        const haystack = [movie.title_ua, movie.title_en, movie.filename, movie.file_label, movie.year].join(' ').toLocaleLowerCase('uk-UA');
        return matchesType && haystack.includes(query);
    });
    const sort = document.getElementById('library-sort').value;
    if (sort === 'title') movies.sort((a, b) => (a.title_ua || a.title_en || a.file_label || '').localeCompare(b.title_ua || b.title_en || b.file_label || '', 'uk'));
    if (sort === 'year') movies.sort((a, b) => Number(b.year || 0) - Number(a.year || 0));
    if (sort === 'rating') movies.sort((a, b) => Number(b.vote_average || 0) - Number(a.vote_average || 0));
    grid.replaceChildren();
    if (movies.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'library-empty';
        empty.textContent = allMovies.length ? 'За цим запитом нічого не знайдено.' : 'Бібліотека порожня. Натисніть «Сканувати», щоб додати файли.';
        grid.appendChild(empty);
        return;
    }
    for (const movie of movies) {
        const card = document.createElement('button');
        card.type = 'button';
        card.className = 'library-movie';
        card.title = `${movie.file_label || movie.filename} — переглянути`;
        const poster = document.createElement('img');
        poster.src = safePosterURL(movie.poster_url);
        poster.alt = '';
        poster.loading = 'lazy';
        poster.onerror = () => { poster.onerror = null; poster.src = noPosterUrl; };
        const art = document.createElement('div');
        art.className = 'library-poster';
        art.appendChild(poster);
        if (needsReview(movie)) {
            const badge = document.createElement('span');
            badge.className = 'library-review-badge';
            badge.textContent = 'Перевірити';
            art.appendChild(badge);
        }
        const title = document.createElement('strong');
        title.textContent = movie.title_ua || movie.title_en || movie.file_label || 'Невідомий запис';
        const meta = document.createElement('span');
        meta.className = 'library-movie-meta';
        meta.textContent = `${movie.year || 'Рік невідомий'} · ${movie.media_type === 'tv' ? 'Серіал' : movie.media_type === 'movie' ? 'Фільм' : 'Тип невідомий'}`;
        card.append(art, title, meta);
        if (movie.vote_count > 0) {
            const rating = document.createElement('span');
            rating.className = 'library-rating';
            rating.textContent = `★ ${Number(movie.vote_average).toFixed(1)}`;
            card.appendChild(rating);
        }
        card.onclick = () => showMovieView(movie);
        grid.appendChild(card);
    }
}

document.getElementById('library-search').addEventListener('input', renderLibrary);
document.getElementById('library-sort').addEventListener('change', renderLibrary);
document.querySelector('.library-filters').addEventListener('click', event => {
    const chip = event.target.closest('.filter-chip');
    if (!chip) return;
    libraryFilter = chip.dataset.filter;
    document.querySelectorAll('.filter-chip').forEach(button => button.classList.toggle('active', button === chip));
    renderLibrary();
});
let hintsCache = {}; // 👈 ФІКС: Кеш для текстових підказок
let mediaTypesCache = {}; // auto | movie | tv для ручного уточнення
let checkedCache = new Set(); // 👈 ФІКС: Кеш для вибраних чекбоксів
const candidateSearches = createRequestGate();

async function loadMovies(focusFilename = '') {
    const list = document.getElementById('movie-list');
    list.innerHTML = "Завантаження...";
    try {
        allMovies = await GetMovies();
		if (document.getElementById('panel-movie').classList.contains('active')) {
			const current = allMovies.find(movie => movie.filename === activeMovieFilename);
			if (current) renderMovieView(current);
		}
		document.getElementById('library-movies').textContent = allMovies.filter(movie => movie.media_type === 'movie').length.toLocaleString('uk-UA');
		document.getElementById('library-series').textContent = allMovies.filter(movie => movie.media_type === 'tv').length.toLocaleString('uk-UA');
		renderLibrary();
		renderFilteredMovies();
		if (focusFilename) {
			requestAnimationFrame(() => {
				const row = Array.from(document.querySelectorAll('.movie-row')).find(item => item.dataset.filename === focusFilename);
				if (row) { row.scrollIntoView({block: 'center'}); row.classList.add('movie-row-focused'); setTimeout(() => row.classList.remove('movie-row-focused'), 1600); }
			});
		}
    } catch (e) { console.error(e); }
}

function renderMovies(movies) {
    const list = document.getElementById('movie-list');
	document.querySelectorAll('.floating-movie-popover').forEach(popover => popover.remove());
	list.replaceChildren();

    movies.forEach((m) => {
        const title = m.title_ua || m.title_en || "Невідомо";
        const query = encodeURIComponent(title);
        const urlType = m.media_type === "tv" ? "tv" : "movie";
        let tmdbUrl = (m.tmdb_id && m.tmdb_id !== 0)
            ? `https://www.themoviedb.org/${urlType}/${m.tmdb_id}`
            : `https://www.themoviedb.org/search/${urlType}?query=${query}`;

        // 👈 ФІКС: Дістаємо значення з кешу
        const hintVal = hintsCache[m.filename] || "";
        const isUnrecognized = !m.tmdb_id || m.tmdb_id === 0;
        const titleColor = isUnrecognized ? "var(--warn-yellow)" : "#58a6ff";

		const row = document.createElement('div');
		row.className = 'movie-row';
		row.dataset.filename = m.filename;

		const checkboxCol = document.createElement('div');
		checkboxCol.className = 'col-cb';
		const checkbox = document.createElement('input');
		checkbox.type = 'checkbox';
		checkbox.className = 'row-cb';
		checkbox.dataset.filename = m.filename;
		checkbox.checked = checkedCache.has(m.filename);
		checkboxCol.appendChild(checkbox);

		const fileCol = document.createElement('div');
		fileCol.className = 'col-file';
		fileCol.title = m.filename;
		fileCol.textContent = m.file_label || m.filename;

		const arrowCol = document.createElement('div');
		arrowCol.className = 'col-arr';
		arrowCol.textContent = '➜';

		const yearCol = document.createElement('div');
		yearCol.className = 'col-year';
		yearCol.textContent = m.year || '—';

		const titleCol = document.createElement('div');
		titleCol.className = 'col-title';
		const link = document.createElement('span');
		link.className = 'tmdb-link';
		link.dataset.url = tmdbUrl;
		link.style.color = titleColor;
		link.style.cursor = 'pointer';
		link.style.textDecoration = 'none';
		link.style.fontWeight = '500';
		link.textContent = title;
		link.tabIndex = 0;
		titleCol.appendChild(link);
		const detailButton = document.createElement('button');
		detailButton.type = 'button';
		detailButton.className = 'btn-movie-detail';
		detailButton.textContent = 'Перегляд';
		detailButton.setAttribute('aria-label', `Переглянути: ${title}`);
		detailButton.onclick = () => showMovieView(m, 'editor');
		titleCol.appendChild(detailButton);
		if (m.tmdb_id > 0 && (m.media_type === 'movie' || m.media_type === 'tv')) attachRecognizedMoviePreview(link, m);
		if (m.vote_count > 0) { const rating = document.createElement('span'); rating.className = 'movie-rating'; rating.textContent = `★ ${Number(m.vote_average).toFixed(1)}`; rating.title = `${m.vote_count} оцінок TMDB`; titleCol.appendChild(rating); }
		if (m.needs_review && m.tmdb_id) {
			const badge = document.createElement('span'); badge.className = 'review-badge'; badge.textContent = `Сумнівний: ${reviewReasonLabel(m.review_reason)}`; titleCol.appendChild(badge);
		}

		const mediaTypeCol = document.createElement('div');
		mediaTypeCol.className = 'col-media-type';
		const mediaTypeSelect = document.createElement('select');
		mediaTypeSelect.className = 'media-type-select';
		mediaTypeSelect.dataset.filename = m.filename;
		for (const [value, label] of [['auto', 'Авто'], ['movie', 'Фільм'], ['tv', 'Серіал']]) {
			const option = document.createElement('option');
			option.value = value;
			option.textContent = label;
			mediaTypeSelect.appendChild(option);
		}
		mediaTypeSelect.value = mediaTypesCache[m.filename] || 'auto';
		mediaTypeCol.appendChild(mediaTypeSelect);

		const hintCol = document.createElement('div');
		hintCol.className = 'col-hint';
		const hintInput = document.createElement('input');
		hintInput.type = 'text';
		hintInput.className = 'hint-input';
		hintInput.dataset.filename = m.filename;
		hintInput.value = hintVal;
		hintCol.appendChild(hintInput);
		const candidatesButton = document.createElement('button');
		candidatesButton.type = 'button';
		candidatesButton.className = 'btn-candidates';
		candidatesButton.dataset.filename = m.filename;
		candidatesButton.textContent = 'Варіанти';
		hintCol.appendChild(candidatesButton);

		row.append(checkboxCol, fileCol, arrowCol, yearCol, titleCol, mediaTypeCol, hintCol);
		list.appendChild(row);
		const candidatePanel = document.createElement('div');
		candidatePanel.className = 'candidate-panel';
		candidatePanel.dataset.filename = m.filename;
		candidatePanel.hidden = true;
		list.appendChild(candidatePanel);
    });

    document.querySelectorAll('.tmdb-link').forEach(el => {
        el.onclick = (e) => {
            const url = e.target.getAttribute('data-url');
			try {
				const parsed = new URL(url);
				if (parsed.protocol !== 'https:' || parsed.hostname !== 'www.themoviedb.org') return;
				if (typeof OpenURL === "function") OpenURL(parsed.href);
				else window.open(parsed.href, '_blank');
			} catch (err) {
				console.error('Некоректний TMDB URL:', err);
			}
        };
        el.onmouseover = () => el.style.textDecoration = 'underline';
        el.onmouseout = () => el.style.textDecoration = 'none';
    });
}

// 👈 ФІКС: Обробники подій для збереження стану в кеш під час вводу/кліку
document.getElementById('movie-list').addEventListener('input', (e) => {
    if (e.target.classList.contains('hint-input')) {
		cacheEditorValue(hintsCache, e.target.getAttribute('data-filename'), e.target.value);
    }
});

document.getElementById('movie-list').addEventListener('change', (e) => {
    if (e.target.classList.contains('row-cb')) {
        const fname = e.target.getAttribute('data-filename');
        if (e.target.checked) checkedCache.add(fname);
		else checkedCache.delete(fname);
	}
	if (e.target.classList.contains('media-type-select')) {
		cacheEditorValue(mediaTypesCache, e.target.dataset.filename, e.target.value);
	}
});

// Пошук по списку (без звернення до бази)
function renderFilteredMovies() {
	const q = document.getElementById('search-input').value.toLowerCase();
	document.getElementById('search-clear').hidden = q.length === 0;
	const reviewOnly = document.getElementById('review-only').checked;
    const filtered = allMovies.filter(m => {
        const title = (m.title_ua || m.title_en || "").toLowerCase();
        const label = (m.file_label || m.filename || "").toLowerCase();
        return (!reviewOnly || needsReview(m)) && (m.filename.toLowerCase().includes(q) || title.includes(q) || label.includes(q));
    });
    renderMovies(filtered);
}
document.getElementById('search-input').addEventListener('input', renderFilteredMovies);
document.getElementById('search-clear').addEventListener('click', () => {
	const input = document.getElementById('search-input');
	input.value = '';
	input.dispatchEvent(new Event('input'));
	input.focus();
});
document.getElementById('search-input').addEventListener('keydown', event => {
	if (event.key === 'Escape' && event.currentTarget.value) {
		event.currentTarget.value = '';
		event.currentTarget.dispatchEvent(new Event('input'));
	}
});
document.getElementById('review-only').addEventListener('change', () => document.getElementById('search-input').dispatchEvent(new Event('input')));

document.getElementById('movie-list').addEventListener('click', async (e) => {
	if (e.target.classList.contains('btn-candidates')) {
		const filename = e.target.dataset.filename;
		if (!candidateSearches.begin(filename)) return;
		e.target.disabled = true;
		const panel = document.querySelector(`.candidate-panel[data-filename="${CSS.escape(filename)}"]`);
		panel.hidden = false;
		panel.replaceChildren(document.createTextNode('Пошук…'));
		try {
			const candidates = await SearchTMDBCandidates(candidateSearchPayload(filename, hintsCache, mediaTypesCache));
			panel.replaceChildren();
			const status = candidateStatus(candidates);
			if (status.kind === 'empty') panel.textContent = status.text;
			for (const candidate of candidates || []) {
				const item = document.createElement('div'); item.className = 'candidate-item';
				const label = document.createElement('span'); label.className = 'candidate-label';
				label.appendChild(document.createTextNode(`${candidate.title} · ${candidate.original_title || '—'} · ${candidate.year || '—'} · ${mediaTypeLabel(candidate.media_type)} · `));
				const tmdbLink = document.createElement('a');
				tmdbLink.className = 'candidate-tmdb-link'; tmdbLink.textContent = `TMDB ${candidate.tmdb_id}`; tmdbLink.href = candidateTMDBURL(candidate);
				tmdbLink.onclick = (event) => { event.preventDefault(); const url = candidateTMDBURL(candidate); if (url) OpenURL(url); };
				label.appendChild(tmdbLink);
				const choose = document.createElement('button'); choose.type = 'button'; choose.textContent = 'Обрати'; choose.className = 'btn-choose-candidate';
				choose.onclick = async () => {
					choose.disabled = true;
					choose.textContent = 'Збереження…';
					try {
						await ConfirmTMDBCandidate(candidateConfirmPayload(filename, candidate));
						await loadMovies(filename);
						await loadStats();
					} catch (err) {
						choose.disabled = false;
						choose.textContent = 'Повторити';
						alert(`Не вдалося обрати фільм: ${err}`);
					}
				};
				const refine = document.createElement('button'); refine.type = 'button'; refine.textContent = 'Уточнити'; refine.className = 'btn-refine-candidate';
				const popover = document.createElement('div'); popover.className = 'candidate-popover floating-movie-popover'; popover.hidden = true; document.body.appendChild(popover);
				refine.onclick = async () => {
					if (refine.dataset.loaded === 'true') { popover.hidden = !popover.hidden; return; }
					refine.disabled = true; refine.textContent = 'Завантаження…';
					try { const details = await GetTMDBCandidateDetails(candidateConfirmPayload(filename, candidate)); renderCandidatePopover(popover, details); refine.dataset.loaded = 'true'; refine.textContent = 'Деталі ✓'; showFloatingPopover(refine, popover); }
					catch (err) { refine.textContent = 'Повторити'; popover.textContent = `Не вдалося завантажити деталі: ${err}`; }
					finally { refine.disabled = false; }
				};
				refine.onmouseenter = () => { if (refine.dataset.loaded === 'true') showFloatingPopover(refine, popover); };
				item.onmouseleave = () => { if (refine.dataset.loaded === 'true') popover.hidden = true; };
				item.append(label, refine, choose); panel.appendChild(item);
			}
			const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = 'Скасувати'; cancel.onclick = () => { panel.hidden = true; document.querySelectorAll('.floating-movie-popover').forEach(popover => popover.remove()); };
			panel.appendChild(cancel);
		} catch (err) { panel.textContent = candidateStatus(null, err).text; }
		finally { candidateSearches.end(filename); e.target.disabled = false; }
		return;
	}
    // Якщо клікнули по іконці видалення
    if (e.target.classList.contains('btn-del')) {
        const filename = e.target.getAttribute('data-filename');

        // Запитуємо підтвердження, щоб не видалити випадково
        if (confirm(`Дійсно видалити "${filename}" з бази?\nПостер також буде знищено. Після цього можна запустити сканування знову.`)) {
            try {
                // Викликаємо Go-метод
                await DeleteMovie(filename);

                // Видаляємо з глобального масиву
                allMovies = allMovies.filter(m => m.filename !== filename);

                // Знаходимо рядок в DOM і видаляємо його плавно (без перерендеру всього списку)
                const row = e.target.closest('.movie-row');
                if (row) {
                    row.style.opacity = '0';
                    setTimeout(() => row.remove(), 200);
                }

                // Оновлюємо статистику
                loadStats();
            } catch (err) {
                console.error("Помилка видалення:", err);
                alert("Не вдалося видалити файл. Дивіться консоль.");
            }
        }
    }
});

function renderCandidatePopover(popover, details) {
	popover.replaceChildren();
	if (details.poster_url) { const img = document.createElement('img'); img.src = details.poster_url; img.alt = ''; img.className = 'candidate-popover-poster'; popover.appendChild(img); }
	const body = document.createElement('div'); body.className = 'candidate-popover-body';
	const heading = document.createElement('strong'); heading.textContent = details.title || details.original_title || 'Без назви';
	const meta = document.createElement('div'); meta.textContent = `${details.release_date || 'дата невідома'} · ${formatRuntime(details.runtime)}${details.short_film ? ' · Короткометражний' : ''}`;
	const rating = document.createElement('div'); rating.className = 'candidate-rating'; rating.textContent = formatTMDBRating(details.vote_average, details.vote_count);
	const genres = document.createElement('div'); genres.textContent = details.genres || 'Жанри не вказані';
	const overview = document.createElement('p'); overview.textContent = details.overview || 'Опис відсутній';
	body.append(heading, meta, rating, genres, overview); popover.appendChild(body);
}

function showFloatingPopover(anchor, popover) {
	popover.hidden = false;
	const rect = anchor.getBoundingClientRect();
	const position = floatingPopoverPosition(rect, {width: popover.offsetWidth, height: popover.offsetHeight}, {width: window.innerWidth, height: window.innerHeight});
	popover.style.left = `${position.left}px`;
	popover.style.top = `${position.top}px`;
}

function attachRecognizedMoviePreview(link, movie) {
	const popover = document.createElement('div');
	popover.className = 'candidate-popover floating-movie-popover recognized-movie-popover';
	popover.hidden = true;
	document.body.appendChild(popover);
	let hideTimer;
	let loadPromise;
	const visibility = createAsyncVisibilityGate();
	const show = async () => {
		clearTimeout(hideTimer);
		visibility.activate();
		if (!loadPromise) {
			popover.textContent = 'Завантаження…';
			showFloatingPopover(link, popover);
			loadPromise = GetTMDBCandidateDetails({filename: movie.filename, tmdb_id: movie.tmdb_id, media_type: movie.media_type})
				.then(details => renderCandidatePopover(popover, details))
				.catch(err => { popover.textContent = `Не вдалося завантажити деталі: ${err}`; loadPromise = null; });
			await loadPromise;
		}
		if (visibility.isActive()) showFloatingPopover(link, popover);
	};
	const hide = () => { hideTimer = setTimeout(() => { visibility.deactivate(); popover.hidden = true; }, 120); };
	link.addEventListener('mouseenter', show);
	link.addEventListener('mouseleave', hide);
	link.addEventListener('focus', show);
	link.addEventListener('blur', hide);
	popover.addEventListener('mouseenter', () => { clearTimeout(hideTimer); visibility.activate(); });
	popover.addEventListener('mouseleave', hide);
}

// Кнопка "Виправити вибрані"
document.getElementById('btn-fix').onclick = () => {
	const selected = fixPayload(checkedCache, hintsCache, mediaTypesCache);
    // 👈 ФІКС: Беремо дані з кешу, а не з DOM, бо елементи можуть бути відфільтровані
    if (selected.length === 0) return;

    // Очищаємо кеш після відправки
	checkedCache.clear();
	hintsCache = {};
	mediaTypesCache = {};

    switchTab('overview', 'Виправлення');
    FixSelected(selected);
};

// Вбудоване підтвердження (без модальних вікон)
let deleteConfirmTimeout;
let isConfirmingDelete = false;

document.getElementById('btn-delete-selected').onclick = async (e) => {
    const selected = Array.from(checkedCache);
    const btn = e.target;

    if (selected.length === 0) {
        // Замість alert можемо просто блимнути кнопкою або змінити текст на секунду
        const origText = btn.innerText;
        btn.innerText = "👀 Нічого не вибрано";
        setTimeout(() => btn.innerText = origText, 1500);
        return;
    }

    // КРОК 1: Запит підтвердження (перший клік)
    if (!isConfirmingDelete) {
        isConfirmingDelete = true;
        const originalText = btn.innerHTML;

        btn.innerHTML = `⚠️ Точно видалити (${selected.length})?`;
        btn.style.backgroundColor = "#8b0000"; // Робимо колір більш темним/тривожним
        btn.style.borderColor = "#8b0000";

        // Скидаємо стан через 3 секунди, якщо користувач передумав
        deleteConfirmTimeout = setTimeout(() => {
            isConfirmingDelete = false;
            btn.innerHTML = originalText;
            btn.style.backgroundColor = "#d11a2a"; // Повертаємо оригінальний червоний
            btn.style.borderColor = "#b2070f";
        }, 3000);
        return;
    }

    // КРОК 2: Виконання дії (другий клік)
    clearTimeout(deleteConfirmTimeout);
    isConfirmingDelete = false;

    btn.innerHTML = "⏳ Видалення...";
    btn.style.pointerEvents = "none";
    btn.style.backgroundColor = "#d11a2a";

    try {
        for (const filename of selected) {
            await DeleteMovie(filename);
        }

        allMovies = allMovies.filter(m => !selected.includes(m.filename));
        checkedCache.clear();
        document.getElementById('search-input').dispatchEvent(new Event('input'));
        loadStats();

    } catch (err) {
        console.error("❌ Помилка масового видалення:", err);
    } finally {
        // Відновлюємо кнопку після завершення
        btn.innerHTML = "🗑 Видалити вибрані";
        btn.style.pointerEvents = "auto";
    }
};

// Чекаємо на wails:ready перш ніж завантажувати дані
async function loadAppVersion() {
    try {
        const version = await GetAppVersion();
        const versionEl = document.getElementById('app-version');
        const welcomeEl = document.getElementById('welcome-message');
        if (versionEl && version) {
            versionEl.innerText = version;
        }
        if (welcomeEl && version) {
            welcomeEl.innerText = `Вітаю у MovieList ${version}! Система готова до роботи.`;
        }
    } catch (e) {
        console.error("Не вдалося завантажити версію додатку:", e);
    }
}

EventsOn('wails:ready', () => {
    console.log("⚡ wails:ready событие получено");
    loadAppVersion();
    loadStats();
    loadMovies();
});

// Також завантажуємо на прямому завантаженні скрипта (не чекаючи на готовність)
console.log("📍 Ініціалізація фронтенду...");
setTimeout(() => {
    console.log("⏱️ Спроба завантажити статистику (через setTimeout)...");
    loadAppVersion();
    loadStats();
    loadMovies();
}, 500);
