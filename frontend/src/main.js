import './style.css';

import { GetAppVersion, GetMovies, GetStats, RunScan, StopScan, GetAIModelCatalog, SetAIModels, OpenLogs, FixSelected, SearchTMDBCandidates, ConfirmTMDBCandidate, GetTMDBCandidateDetails, SyncToCloud, SyncToGitHub, OpenShowcase, OpenSheet, OpenGoogleSheet, OpenGitHubRepo, OpenGitHubPage, OpenURL, DeleteMovie, SelectMediaFolder } from '../wailsjs/go/main/App.js';
import { Quit, WindowMinimise, WindowToggleMaximise, EventsOn } from '../wailsjs/runtime/runtime.js';
import logoUrl from './assets/images/appicon.png';
import noPosterUrl from './assets/images/no-poster.jpg';
import { candidateConfirmPayload, candidateSearchPayload, fixPayload, reviewReasonLabel, mediaTypeLabel, candidateTMDBURL, formatRuntime, formatTMDBRating, candidateStatus, scanLifecycleTransition, cacheEditorValue, filterAndSortEditorMovies, updateEditorSelection as changeEditorSelection } from './editor-state.js';

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
            <div class="nav-btn" id="btn-editor"><span class="nav-icon">✎</span> Редактор</div>
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

            <div class="nav-btn" id="btn-models"><span class="nav-icon">✦</span> Моделі ШІ</div>
            <div class="nav-btn" id="btn-select-folder"><span class="nav-icon">▱</span> Вибрати папку</div>
            <div class="nav-btn" id="btn-logs"><span class="nav-icon">☷</span> Папка з логами</div>
        </div>

        <div class="sidebar-footer">
            © 2026 <a href="#" onclick="window.go.main.App.OpenGitHubRepo(); return false;">shylosa</a>
        </div>
    </div>
    <div class="main-area library-active">
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
                    <section class="movie-section movie-file-section"><h2>Файл</h2><p id="movie-file-path"></p></section>
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
                <div class="editor-heading"><h1>Редактор</h1><span id="editor-count">—</span></div>
                <div class="editor-search">
                    <span aria-hidden="true">⌕</span>
                    <input type="text" id="search-input" placeholder="Назва або файл…" aria-label="Пошук у редакторі" />
                    <button id="search-clear" type="button" aria-label="Очистити пошук" title="Очистити пошук" hidden>✕</button>
                </div>
                <label class="editor-sort-label">Сортувати <select id="editor-sort"><option value="title">За назвою</option><option value="year">За роком</option><option value="rating">За рейтингом</option><option value="filename">За файлом</option></select></label>
            </div>
            <div class="editor-filters" role="group" aria-label="Фільтр редактора">
                <button class="editor-filter active" data-filter="all" type="button">Усі</button>
                <button class="editor-filter" data-filter="review" type="button">Потребують перевірки</button>
                <button class="editor-filter" data-filter="unresolved" type="button">Нерозпізнані</button>
                <label class="editor-select-all-label" title="Вибрати всі записи поточного списку"><input id="editor-select-all" class="row-cb" type="checkbox" aria-label="Вибрати всі записи поточного списку"> Вибрати всі</label>
            </div>
            <div class="editor-workspace">
                <div class="editor-list-area">
                    <div id="movie-list" aria-label="Записи колекції">Завантаження...</div>
                </div>
                <aside id="editor-inspector" class="editor-inspector" aria-label="Вибраний запис">
                    <div id="editor-inspector-content" class="editor-inspector-content"></div>
                </aside>
            </div>
            <div id="editor-bulk" class="editor-bulk" hidden>
                <span id="editor-selected-count"></span>
                <button id="editor-clear-selection" type="button">Скасувати вибір</button>
                <button id="editor-batch-open" type="button">✦ Уточнити й виправити</button>
                <button id="btn-delete-selected" class="btn-danger" type="button">Видалити вибрані</button>
            </div>
            <div id="editor-batch-panel" class="editor-batch-panel" hidden>
                <section class="editor-batch-dialog" role="dialog" aria-modal="true" aria-labelledby="editor-batch-title">
                    <div class="editor-batch-heading"><div><h2 id="editor-batch-title">Пакетне уточнення</h2><p>Для кожного файлу вкажіть власну назву або IMDb ID.</p></div><button id="editor-batch-close" type="button" aria-label="Закрити пакетне уточнення" title="Закрити">✕</button></div>
                    <div class="editor-batch-tools"><label>Тип для всіх <select id="editor-batch-type"><option value="">Не змінювати</option><option value="auto">Авто</option><option value="movie">Фільм</option><option value="tv">Серіал</option></select></label><button id="editor-batch-apply-type" type="button">Застосувати тип</button></div>
                    <div id="editor-batch-list" class="editor-batch-list"></div>
                    <div class="editor-batch-footer"><span id="editor-batch-count"></span><button id="editor-batch-fix" class="primary-button" type="button">✦ Запустити виправлення</button></div>
                </section>
            </div>
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
    document.querySelector('.main-area').classList.toggle('library-active', tab === 'library');

    document.getElementById('current-page-title').innerText = title;

    const flag = document.getElementById('current-page-title');
    flag.innerText = title;
};

// Прив'язка кнопок
document.getElementById('btn-library').onclick = () => { switchTab('library', 'Бібліотека'); loadMovies(); };
document.getElementById('btn-overview').onclick = () => switchTab('overview', 'Огляд');
document.getElementById('btn-review').onclick = () => {
    document.getElementById('search-input').value = '';
    editorFilter = 'review';
    closeEditorInspector();
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
    editorFilter = 'all';
    closeEditorInspector();
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
    const reviewRequired = needsReview(movie);
    status.textContent = reviewRequired ? 'Потребує перевірки' : '';
    status.hidden = !reviewRequired;
    status.classList.toggle('needs-review', reviewRequired);
    const facts = document.getElementById('movie-facts');
    facts.replaceChildren();
    const items = [
        {text: movie.media_type === 'tv' ? 'TV' : movie.media_type === 'movie' ? 'Фільм' : 'Тип невідомий', kind: 'type'},
        {text: movie.year || 'Рік невідомий', kind: 'year'},
    ];
    const tmdbURL = candidateTMDBURL(movie);
    if (movie.vote_count > 0 || tmdbURL) {
        items.push({text: movie.vote_count > 0 ? `★ ${Number(movie.vote_average).toFixed(1)} TMDB ↗` : 'TMDB ↗', kind: 'rating', url: tmdbURL});
    }
    if (movie.genres) items.push({text: movie.genres, kind: 'genres'});
    for (const item of items) {
        const chip = document.createElement(item.url ? 'a' : 'span');
        chip.className = `movie-fact-${item.kind}`;
        chip.textContent = item.text;
        if (item.url) {
            chip.href = item.url;
            chip.title = 'Відкрити в TMDB';
            chip.setAttribute('aria-label', `${item.text.replace(' ↗', '')}. Відкрити в TMDB`);
            chip.onclick = event => { event.preventDefault(); OpenURL(item.url); };
        }
        facts.appendChild(chip);
    }
    document.querySelector('.movie-section h2').textContent = movie.media_type === 'tv' ? 'Про серіал' : 'Про фільм';
    document.getElementById('movie-plot').textContent = movie.plot || 'Опис поки що відсутній.';
    const cast = document.getElementById('movie-cast-section');
    document.getElementById('movie-cast').textContent = movie.cast || '';
    cast.hidden = !movie.cast;
    document.getElementById('movie-file-path').textContent = movie.filename;
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
    editorFilter = 'all';
    editorActiveFilename = activeMovieFilename;
    document.getElementById('search-input').value = activeMovieFilename;
    switchTab('editor', 'Редактор');
    renderFilteredMovies();
    document.getElementById('editor-inspector').classList.add('open');
    document.querySelector('.editor-workspace').classList.add('has-selection');
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
        updateLibraryScrolledState();
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
        meta.textContent = movie.year || 'Рік невідомий';
        const metaRow = document.createElement('div');
        metaRow.className = 'library-movie-meta-row';
        metaRow.appendChild(meta);
        if (movie.media_type === 'tv') {
            const tvBadge = document.createElement('span');
            tvBadge.className = 'library-tv-badge';
            tvBadge.textContent = 'TV';
            metaRow.appendChild(tvBadge);
        }
        if (movie.vote_count > 0) {
            const rating = document.createElement('span');
            rating.className = 'library-rating';
            rating.textContent = `★ ${Number(movie.vote_average).toFixed(1)}`;
            metaRow.appendChild(rating);
        }
        const genres = document.createElement('span');
        genres.className = 'library-movie-genres';
        genres.textContent = movie.genres || 'Жанр не вказано';
        if (movie.genres) genres.title = movie.genres;
        card.append(art, title, metaRow, genres);
        card.onclick = () => showMovieView(movie);
        grid.appendChild(card);
    }
    updateLibraryScrolledState();
}

function updateLibraryScrolledState() {
    document.getElementById('panel-library').classList.toggle('library-scrolled', document.getElementById('library-grid').scrollTop > 8);
}

document.getElementById('library-search').addEventListener('input', renderLibrary);
document.getElementById('library-grid').addEventListener('scroll', updateLibraryScrolledState, {passive: true});
document.getElementById('library-sort').addEventListener('change', renderLibrary);
document.querySelector('.library-filters').addEventListener('click', event => {
    const chip = event.target.closest('.filter-chip');
    if (!chip) return;
    libraryFilter = chip.dataset.filter;
    document.querySelectorAll('.filter-chip').forEach(button => button.classList.toggle('active', button === chip));
    renderLibrary();
});
let hintsCache = {};
let mediaTypesCache = {};
let checkedCache = new Set();
let editorFilter = 'all';
let editorActiveFilename = '';
let visibleEditorMovies = [];
let editorCandidateRequest = 0;
let editorPreview = null;

async function loadMovies(focusFilename = '') {
    const list = document.getElementById('movie-list');
    list.textContent = 'Завантаження…';
    try {
        allMovies = await GetMovies();
		checkedCache = new Set([...checkedCache].filter(filename => allMovies.some(movie => movie.filename === filename)));
		if (focusFilename && document.getElementById('editor-inspector').classList.contains('open')) editorActiveFilename = focusFilename;
		if (editorActiveFilename && !allMovies.some(movie => movie.filename === editorActiveFilename)) editorActiveFilename = '';
		if (document.getElementById('panel-movie').classList.contains('active')) {
			const current = allMovies.find(movie => movie.filename === activeMovieFilename);
			if (current) renderMovieView(current);
		}
		document.getElementById('library-movies').textContent = allMovies.filter(movie => movie.media_type === 'movie').length.toLocaleString('uk-UA');
		document.getElementById('library-series').textContent = allMovies.filter(movie => movie.media_type === 'tv').length.toLocaleString('uk-UA');
		renderLibrary();
		renderFilteredMovies();
		if (editorActiveFilename) renderEditorInspector();
		if (focusFilename) {
			requestAnimationFrame(() => {
				const row = Array.from(document.querySelectorAll('.movie-row')).find(item => item.dataset.filename === focusFilename);
				if (row) { row.scrollIntoView({block: 'center'}); row.classList.add('movie-row-focused'); setTimeout(() => row.classList.remove('movie-row-focused'), 1600); }
			});
		}
    } catch (e) { console.error(e); }
}

function editorStatus(movie) {
    if (!movie.tmdb_id) return {icon: '!', label: 'Не розпізнано', kind: 'unresolved'};
    if (movie.needs_review) return {icon: '!', label: `Потребує перевірки: ${reviewReasonLabel(movie.review_reason)}`, kind: 'review'};
    return {icon: '✓', label: 'Розпізнано', kind: 'recognized'};
}

function editorElement(tag, className, textValue) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (textValue !== undefined) element.textContent = textValue;
    return element;
}

function hideEditorPreview() {
    editorPreview?.remove();
    editorPreview = null;
}

function showEditorPreview(anchor, movie) {
    hideEditorPreview();
    if (!movie.tmdb_id) return;
    const preview = editorElement('div', 'editor-hover-preview');
    preview.setAttribute('role', 'tooltip');
    const poster = editorElement('img', 'editor-hover-poster');
    poster.src = safePosterURL(movie.poster_url);
    poster.alt = '';
    poster.onerror = () => { poster.onerror = null; poster.src = noPosterUrl; };
    const body = editorElement('div', 'editor-hover-body');
    body.appendChild(editorElement('strong', '', movie.title_ua || movie.title_en || 'Без назви'));
    body.appendChild(editorElement('span', '', `${movie.year || 'Рік невідомий'} · ${mediaTypeLabel(movie.media_type)}${movie.vote_count > 0 ? ` · ★ ${Number(movie.vote_average).toFixed(1)}` : ''}`));
    if (movie.plot) body.appendChild(editorElement('p', '', movie.plot));
    preview.append(poster, body);
    document.body.appendChild(preview);
    editorPreview = preview;
    const rect = anchor.getBoundingClientRect();
    const left = rect.left >= preview.offsetWidth + 18 ? rect.left - preview.offsetWidth - 10 : rect.right + 10;
    preview.style.left = `${Math.max(8, Math.min(left, window.innerWidth - preview.offsetWidth - 8))}px`;
    preview.style.top = `${Math.max(8, Math.min(rect.top - 12, window.innerHeight - preview.offsetHeight - 8))}px`;
}

function updateEditorSelection() {
    const count = checkedCache.size;
    const bar = document.getElementById('editor-bulk');
    bar.hidden = count === 0;
    document.getElementById('editor-selected-count').textContent = `Вибрано: ${count}`;
    const selectAll = document.getElementById('editor-select-all');
    const visibleCount = visibleEditorMovies.filter(movie => checkedCache.has(movie.filename)).length;
    selectAll.checked = visibleEditorMovies.length > 0 && visibleCount === visibleEditorMovies.length;
    selectAll.indeterminate = visibleCount > 0 && visibleCount < visibleEditorMovies.length;
    selectAll.disabled = visibleEditorMovies.length === 0;
    if (count === 0) closeEditorBatchPanel();
    else if (!document.getElementById('editor-batch-panel').hidden) renderEditorBatchPanel();
}

function closeEditorBatchPanel() {
    const panel = document.getElementById('editor-batch-panel');
    const wasOpen = !panel.hidden;
    panel.hidden = true;
    if (wasOpen && editorActiveFilename && document.getElementById('editor-inspector').classList.contains('open')) renderEditorInspector();
}

function renderEditorBatchPanel() {
    const list = document.getElementById('editor-batch-list');
    const selected = [...checkedCache].map(filename => allMovies.find(movie => movie.filename === filename)).filter(Boolean);
    list.replaceChildren();
    document.getElementById('editor-batch-count').textContent = `Вибрано: ${selected.length}`;
    for (const movie of selected) {
        const row = editorElement('div', 'editor-batch-row');
        const identity = editorElement('div', 'editor-batch-identity');
        identity.appendChild(editorElement('strong', '', movie.title_ua || movie.title_en || 'Не розпізнано'));
        const filename = editorElement('span', '', movie.file_label || movie.filename);
        filename.title = movie.filename;
        identity.appendChild(filename);
        const typeLabel = editorElement('label', '', 'Тип');
        const type = editorElement('select', 'editor-batch-type');
        for (const [value, label] of [['auto', 'Авто'], ['movie', 'Фільм'], ['tv', 'Серіал']]) {
            const option = editorElement('option', '', label); option.value = value; type.appendChild(option);
        }
        type.value = mediaTypesCache[movie.filename] || 'auto';
        type.onchange = () => cacheEditorValue(mediaTypesCache, movie.filename, type.value);
        typeLabel.appendChild(type);
        const hintLabel = editorElement('label', '', 'Підказка');
        const hint = editorElement('input', 'editor-batch-hint');
        hint.type = 'text'; hint.placeholder = 'Назва або IMDb ID'; hint.value = hintsCache[movie.filename] || '';
        hint.oninput = () => cacheEditorValue(hintsCache, movie.filename, hint.value);
        hintLabel.appendChild(hint);
        row.append(identity, typeLabel, hintLabel);
        list.appendChild(row);
    }
}

document.getElementById('editor-batch-open').onclick = () => {
    if (!checkedCache.size) return;
    renderEditorBatchPanel();
    document.getElementById('editor-batch-panel').hidden = false;
    document.getElementById('editor-batch-close').focus();
};
document.getElementById('editor-batch-close').onclick = closeEditorBatchPanel;
document.getElementById('editor-batch-panel').addEventListener('click', event => {
    if (event.target.id === 'editor-batch-panel') closeEditorBatchPanel();
});
document.getElementById('editor-batch-apply-type').onclick = () => {
    const value = document.getElementById('editor-batch-type').value;
    if (!value) return;
    for (const filename of checkedCache) cacheEditorValue(mediaTypesCache, filename, value);
    renderEditorBatchPanel();
};
document.getElementById('editor-batch-fix').onclick = () => runEditorFix(checkedCache);
document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !document.getElementById('editor-batch-panel').hidden) {
        event.preventDefault(); closeEditorBatchPanel(); document.getElementById('editor-batch-open').focus();
    }
});

function renderMovies(movies) {
    const list = document.getElementById('movie-list');
    hideEditorPreview();
    list.replaceChildren();
    if (!movies.length) {
        const isReviewEmpty = editorFilter !== 'all' && !document.getElementById('search-input').value.trim();
        const message = !allMovies.length ? 'Колекція порожня. Запустіть сканування.' : isReviewEmpty ? 'Всі файли розпізнані' : 'Нічого не знайдено.';
        list.appendChild(editorElement('div', 'editor-empty', message));
    }
    for (const movie of movies) {
        const row = editorElement('div', 'movie-row');
        row.dataset.filename = movie.filename;
        row.tabIndex = 0;
        row.setAttribute('role', 'button');
        row.setAttribute('aria-label', `Редагувати ${movie.title_ua || movie.title_en || movie.file_label || movie.filename}`);
        row.classList.toggle('active', movie.filename === editorActiveFilename);

        const checkCell = editorElement('div', 'col-cb');
        const checkbox = editorElement('input', 'row-cb');
        checkbox.type = 'checkbox';
        checkbox.checked = checkedCache.has(movie.filename);
        checkbox.setAttribute('aria-label', `Вибрати ${movie.file_label || movie.filename}`);
        checkbox.addEventListener('click', event => event.stopPropagation());
        checkbox.addEventListener('change', () => {
            checkedCache = changeEditorSelection(checkedCache, [movie.filename], checkbox.checked);
            updateEditorSelection();
        });
        checkCell.appendChild(checkbox);

        const titleCell = editorElement('div', 'col-file');
        titleCell.appendChild(editorElement('strong', 'editor-row-title', movie.title_ua || movie.title_en || 'Не розпізнано'));
        const file = editorElement('span', 'editor-row-file', movie.file_label || movie.filename);
        file.title = movie.filename;
        titleCell.appendChild(file);
        const yearCell = editorElement('span', 'col-year', movie.year || '—');
        const typeCell = editorElement('span', 'col-type', movie.media_type === 'tv' ? 'Серіал' : movie.media_type === 'movie' ? 'Фільм' : '—');
        const ratingCell = editorElement('span', 'col-rating', movie.vote_count > 0 ? `★ ${Number(movie.vote_average).toFixed(1)}` : '—');
        const status = editorStatus(movie);
        const statusCell = editorElement('span', `col-status status-${status.kind}`, status.icon);
        statusCell.title = status.label;
        statusCell.setAttribute('aria-label', status.label);
        const actionCell = editorElement('div', 'col-action');
        const view = editorElement('button', 'editor-icon-button');
        const eye = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        eye.setAttribute('viewBox', '0 0 24 24');
        eye.setAttribute('width', '17'); eye.setAttribute('height', '17'); eye.setAttribute('aria-hidden', 'true');
        const eyeShape = document.createElementNS('http://www.w3.org/2000/svg', 'path');
        eyeShape.setAttribute('d', 'M2 12s3.6-6 10-6 10 6 10 6-3.6 6-10 6S2 12 2 12Z M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z');
        eyeShape.setAttribute('fill', 'none'); eyeShape.setAttribute('stroke', 'currentColor'); eyeShape.setAttribute('stroke-width', '1.8');
        eye.appendChild(eyeShape); view.appendChild(eye);
        view.type = 'button';
        view.title = movie.tmdb_id ? '' : 'Переглянути картку';
        view.setAttribute('aria-label', `Переглянути картку: ${movie.title_ua || movie.title_en || movie.filename}`);
        view.onmouseenter = () => showEditorPreview(view, movie);
        view.onmouseleave = hideEditorPreview;
        view.onfocus = () => showEditorPreview(view, movie);
        view.onblur = hideEditorPreview;
        view.onclick = event => { event.stopPropagation(); hideEditorPreview(); showMovieView(movie, 'editor'); };
        actionCell.appendChild(view);
        row.append(checkCell, titleCell, yearCell, typeCell, ratingCell, statusCell, actionCell);
        row.onclick = () => selectEditorMovie(movie.filename);
        row.onkeydown = event => {
            if ((event.key === 'Enter' || event.key === ' ') && event.target === row) { event.preventDefault(); selectEditorMovie(movie.filename); }
        };
        list.appendChild(row);
    }
    updateEditorSelection();
    if (document.getElementById('editor-inspector-content').dataset.filename !== editorActiveFilename) renderEditorInspector();
}

function selectEditorMovie(filename) {
    if (editorActiveFilename === filename && document.getElementById('editor-inspector').classList.contains('open')) return;
    editorActiveFilename = filename;
    document.querySelectorAll('#movie-list .movie-row').forEach(row => row.classList.toggle('active', row.dataset.filename === filename));
    renderEditorInspector();
    document.getElementById('editor-inspector').classList.add('open');
    document.querySelector('.editor-workspace').classList.add('has-selection');
}

function closeEditorInspector() {
    editorActiveFilename = '';
    document.getElementById('editor-inspector').classList.remove('open');
    document.querySelector('.editor-workspace').classList.remove('has-selection');
    document.querySelectorAll('#movie-list .movie-row').forEach(row => row.classList.remove('active'));
    renderEditorInspector();
}

function renderEditorInspector() {
    editorCandidateRequest++;
    const content = document.getElementById('editor-inspector-content');
    content.replaceChildren();
    content.dataset.filename = editorActiveFilename;
    const movie = allMovies.find(item => item.filename === editorActiveFilename);
    if (!movie) {
        content.appendChild(editorElement('div', 'editor-inspector-empty', 'Виберіть запис у списку, щоб переглянути й змінити його.'));
        return;
    }
    const top = editorElement('div', 'inspector-top');
    const close = editorElement('button', 'editor-inspector-close', '✕');
    close.type = 'button'; close.title = 'Закрити панель'; close.setAttribute('aria-label', 'Закрити панель');
    close.onclick = closeEditorInspector;
    top.appendChild(close);
    content.appendChild(top);
    const hero = editorElement('div', 'inspector-hero');
    const poster = editorElement('img', 'inspector-hero-poster');
    poster.src = safePosterURL(movie.poster_url);
    poster.alt = `Постер: ${movie.title_ua || movie.title_en || 'Без назви'}`;
    poster.onerror = () => { poster.onerror = null; poster.src = noPosterUrl; };
    const heroDetails = editorElement('div', 'inspector-hero-details');
    const title = editorElement('h2', 'inspector-title');
    const titleLink = editorElement('button', 'inspector-title-link', movie.title_ua || movie.title_en || 'Не розпізнано');
    titleLink.type = 'button';
    titleLink.title = 'Переглянути картку';
    titleLink.setAttribute('aria-label', `Переглянути картку: ${titleLink.textContent}`);
    titleLink.onclick = () => showMovieView(movie, 'editor');
    title.appendChild(titleLink);
    heroDetails.appendChild(title);
    if (movie.title_en && movie.title_en !== movie.title_ua) heroDetails.appendChild(editorElement('p', 'inspector-original', movie.title_en));
    const status = editorStatus(movie);
    const meta = editorElement('div', 'inspector-meta');
    meta.appendChild(editorElement('span', `inspector-status status-${status.kind}`, `${status.icon} ${status.label}`));
    meta.appendChild(editorElement('span', '', movie.year || 'Рік невідомий'));
    if (movie.media_type === 'tv') meta.appendChild(editorElement('span', '', 'TV'));
    const tmdbURL = candidateTMDBURL(movie);
    if (movie.vote_count > 0 || tmdbURL) {
        const ratingText = movie.vote_count > 0 ? `★ ${Number(movie.vote_average).toFixed(1)} TMDB ↗` : 'TMDB ↗';
        const rating = editorElement(tmdbURL ? 'a' : 'span', 'inspector-rating', ratingText);
        if (tmdbURL) {
            rating.href = tmdbURL;
            rating.title = 'Відкрити в TMDB';
            rating.setAttribute('aria-label', `${ratingText.replace(' ↗', '')}. Відкрити в TMDB`);
            rating.onclick = event => { event.preventDefault(); OpenURL(tmdbURL); };
        }
        meta.appendChild(rating);
    }
    heroDetails.appendChild(meta);
    hero.append(poster, heroDetails);
    content.appendChild(hero);
    const path = editorElement('div', 'inspector-file');
    path.appendChild(editorElement('span', 'inspector-label', 'ФАЙЛ'));
    path.appendChild(editorElement('strong', '', movie.filename));
    content.appendChild(path);
    const fields = editorElement('div', 'inspector-fields');
    const typeLabel = editorElement('label', '', 'Тип для пошуку');
    const type = editorElement('select', 'media-type-select');
    for (const [value, label] of [['auto', 'Авто'], ['movie', 'Фільм'], ['tv', 'Серіал']]) {
        const option = editorElement('option', '', label); option.value = value; type.appendChild(option);
    }
    type.value = mediaTypesCache[movie.filename] || 'auto';
    type.onchange = () => cacheEditorValue(mediaTypesCache, movie.filename, type.value);
    typeLabel.appendChild(type);
    const hintLabel = editorElement('label', '', 'Підказка для пошуку');
    const hint = editorElement('input', 'hint-input');
    hint.type = 'text'; hint.placeholder = 'Назва, IMDb ID або посилання'; hint.value = hintsCache[movie.filename] || '';
    hint.oninput = () => cacheEditorValue(hintsCache, movie.filename, hint.value);
    hintLabel.appendChild(hint);
    fields.append(typeLabel, hintLabel);
    content.appendChild(fields);
    const actions = editorElement('div', 'inspector-actions');
    const candidatesButton = editorElement('button', 'primary-button', 'Знайти варіанти');
    candidatesButton.type = 'button';
    const fixButton = editorElement('button', 'inspector-secondary', '✦ Виправити запис');
    fixButton.type = 'button';
    fixButton.onclick = () => runEditorFix([movie.filename]);
    const remove = editorElement('button', 'inspector-delete', 'Видалити');
    remove.type = 'button';
    remove.title = 'Видалити запис із MovieList';
    remove.setAttribute('aria-controls', 'inspector-delete-confirm');
    remove.setAttribute('aria-expanded', 'false');
    actions.append(candidatesButton, fixButton, remove);
    content.appendChild(actions);
    const deletePanel = editorElement('section', 'inspector-delete-confirm');
    deletePanel.id = 'inspector-delete-confirm';
    deletePanel.hidden = true;
    deletePanel.setAttribute('aria-label', 'Підтвердження видалення');
    const deleteError = editorElement('p', 'inspector-delete-error');
    deleteError.setAttribute('role', 'alert');
    deleteError.hidden = true;
    const deleteActions = editorElement('div', 'inspector-delete-actions');
    const cancelDelete = editorElement('button', '', 'Скасувати');
    cancelDelete.type = 'button';
    const confirmDelete = editorElement('button', 'inspector-delete-confirm-button', 'Видалити запис');
    confirmDelete.type = 'button';
    deleteActions.append(cancelDelete, confirmDelete);
    deletePanel.appendChild(deleteActions);
    content.append(deletePanel, deleteError);
    let deleting = false;
    const hideDeletePanel = () => {
        if (deleting) return;
        deletePanel.hidden = true;
        deleteError.hidden = true;
        remove.setAttribute('aria-expanded', 'false');
        remove.focus();
    };
    remove.onclick = () => {
        if (deleting) return;
        if (!deletePanel.hidden) {
            hideDeletePanel();
            return;
        }
        deleteError.hidden = true;
        deletePanel.hidden = false;
        remove.setAttribute('aria-expanded', 'true');
        cancelDelete.focus();
    };
    cancelDelete.onclick = hideDeletePanel;
    deletePanel.addEventListener('keydown', event => {
        if (event.key === 'Escape' && !deleting) {
            event.preventDefault();
            hideDeletePanel();
        }
    });
    confirmDelete.onclick = async () => {
        if (deleting) return;
        deleting = true;
        confirmDelete.disabled = true;
        cancelDelete.disabled = true;
        deleteError.hidden = true;
        try {
            await DeleteMovie(movie.filename);
        } catch (error) {
            deleteError.textContent = `Не вдалося видалити запис: ${error}`;
            deleteError.hidden = false;
            deleting = false;
            confirmDelete.disabled = false;
            cancelDelete.disabled = false;
            return;
        }
        checkedCache.delete(movie.filename);
        if (editorActiveFilename === movie.filename) closeEditorInspector();
        await loadMovies();
        await loadStats();
    };
    const candidatesPanel = editorElement('div', 'inspector-candidates');
    content.appendChild(candidatesPanel);
    candidatesButton.onclick = async () => {
        const filename = movie.filename;
        const request = ++editorCandidateRequest;
        candidatesButton.disabled = true;
        candidatesPanel.textContent = 'Пошук варіантів…';
        try {
            const candidates = await SearchTMDBCandidates(candidateSearchPayload(filename, hintsCache, mediaTypesCache));
            if (request !== editorCandidateRequest || editorActiveFilename !== filename) return;
            candidatesPanel.replaceChildren();
            const status = candidateStatus(candidates);
            if (status.kind === 'empty') candidatesPanel.textContent = status.text;
            else for (const candidate of (candidates || []).slice(0, 5)) candidatesPanel.appendChild(renderEditorCandidate(filename, candidate));
        } catch (error) {
            if (request === editorCandidateRequest) candidatesPanel.textContent = candidateStatus(null, error).text;
        } finally {
            candidatesButton.disabled = false;
        }
    };
}

function renderEditorCandidate(filename, candidate) {
    const item = editorElement('div', 'candidate-item');
    const label = editorElement('div', 'candidate-label');
    label.appendChild(editorElement('strong', '', candidate.title || candidate.original_title || 'Без назви'));
    label.appendChild(editorElement('span', '', `${candidate.original_title || '—'} · ${candidate.year || '—'} · ${mediaTypeLabel(candidate.media_type)}`));
    const url = candidateTMDBURL(candidate);
    if (url) {
        const tmdb = editorElement('button', 'candidate-tmdb-link', `TMDB ${candidate.tmdb_id} ↗`);
        tmdb.type = 'button'; tmdb.onclick = () => OpenURL(url); label.appendChild(tmdb);
    }
    const buttons = editorElement('div', 'candidate-actions');
    const detailsButton = editorElement('button', 'inspector-secondary', 'Деталі');
    detailsButton.type = 'button';
    const choose = editorElement('button', 'primary-button', 'Обрати');
    choose.type = 'button';
    const detailsPanel = editorElement('div', 'candidate-popover candidate-inline-details');
    detailsPanel.hidden = true;
    detailsButton.onclick = async () => {
        if (detailsButton.dataset.loaded === 'true') { detailsPanel.hidden = !detailsPanel.hidden; return; }
        detailsButton.disabled = true;
        try {
            const details = await GetTMDBCandidateDetails(candidateConfirmPayload(filename, candidate));
            if (editorActiveFilename !== filename || !item.isConnected) return;
            renderCandidatePopover(detailsPanel, details);
            detailsButton.dataset.loaded = 'true'; detailsPanel.hidden = false;
        } catch (error) { detailsPanel.textContent = `Не вдалося завантажити деталі: ${error}`; detailsPanel.hidden = false; }
        finally { detailsButton.disabled = false; }
    };
    choose.onclick = async () => {
        choose.disabled = true; choose.textContent = 'Збереження…';
        try { await ConfirmTMDBCandidate(candidateConfirmPayload(filename, candidate)); await loadMovies(filename); await loadStats(); }
        catch (error) { choose.disabled = false; choose.textContent = 'Повторити'; alert(`Не вдалося обрати фільм: ${error}`); }
    };
    buttons.append(detailsButton, choose);
    item.append(label, buttons, detailsPanel);
    return item;
}

function renderFilteredMovies() {
    const query = document.getElementById('search-input').value;
    document.getElementById('search-clear').hidden = query.length === 0;
    const sort = document.getElementById('editor-sort').value;
    visibleEditorMovies = filterAndSortEditorMovies(allMovies, query, editorFilter, sort);
    if (editorActiveFilename && !visibleEditorMovies.some(movie => movie.filename === editorActiveFilename)) closeEditorInspector();
    document.querySelectorAll('.editor-filter').forEach(button => button.classList.toggle('active', button.dataset.filter === editorFilter));
    document.getElementById('editor-count').textContent = `${visibleEditorMovies.length} із ${allMovies.length}`;
    renderMovies(visibleEditorMovies);
}

document.getElementById('search-input').addEventListener('input', renderFilteredMovies);
document.getElementById('editor-sort').addEventListener('change', renderFilteredMovies);
document.querySelector('.editor-filters').addEventListener('click', event => {
    const button = event.target.closest('.editor-filter');
    if (!button) return;
    editorFilter = button.dataset.filter;
    renderFilteredMovies();
});
document.getElementById('search-clear').addEventListener('click', () => {
    const input = document.getElementById('search-input');
    input.value = ''; renderFilteredMovies(); input.focus();
});
document.getElementById('search-input').addEventListener('keydown', event => {
    if (event.key === 'Escape' && event.currentTarget.value) { event.currentTarget.value = ''; renderFilteredMovies(); }
});
document.getElementById('editor-select-all').addEventListener('change', event => {
    checkedCache = changeEditorSelection(checkedCache, visibleEditorMovies.map(movie => movie.filename), event.target.checked);
    document.querySelectorAll('#movie-list .movie-row').forEach(row => { row.querySelector('.row-cb').checked = checkedCache.has(row.dataset.filename); });
    updateEditorSelection();
});
document.getElementById('editor-clear-selection').onclick = () => {
    checkedCache = new Set();
    document.querySelectorAll('#movie-list .row-cb').forEach(checkbox => { checkbox.checked = false; });
    updateEditorSelection();
};
document.getElementById('movie-list').addEventListener('scroll', hideEditorPreview);
window.addEventListener('resize', hideEditorPreview);

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

async function runEditorFix(filenames) {
	const selected = fixPayload(filenames, hintsCache, mediaTypesCache);
    if (selected.length === 0) return;
    try {
        await FixSelected(selected);
        for (const {filename} of selected) { checkedCache.delete(filename); delete hintsCache[filename]; delete mediaTypesCache[filename]; }
        updateEditorSelection();
        closeEditorBatchPanel();
        switchTab('overview', 'Виправлення');
    } catch (error) {
        alert(`Не вдалося запустити виправлення: ${error}`);
    }
}
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
    } catch (err) {
        console.error("❌ Помилка масового видалення:", err);
        alert(`Не вдалося видалити всі вибрані записи: ${err}`);
    } finally {
        await loadMovies();
        await loadStats();
        // Відновлюємо кнопку після завершення
        btn.innerHTML = "Видалити вибрані";
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
