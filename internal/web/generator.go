package web

import (
	"encoding/base64"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
)

// SVG-іконки з вашого build_html.py
const (
	gridIconSVG = `<svg viewBox="0 0 24 24" class="toggle-icon"><path d="M4 4h4v4H4V4m6 0h4v4h-4V4m6 0h4v4h-4V4M4 10h4v4H4v-4m6 0h4v4h-4v-4m6 0h4v4h-4v-4M4 16h4v4H4v-4m6 0h4v4h-4v-4m6 0h4v4h-4v-4Z"/></svg>`
	listIconSVG = `<svg viewBox="0 0 24 24" class="toggle-icon"><path d="M4 6h16v2H4V6m0 5h16v2H4v-2m0 5h16v2H4v-2m-3 0h2v2H1v-2m0-5h2v2H1v-2m0-5h2v2H1V6"/></svg>`
)

// Generate створює статичний HTML-файл каталогу
// movielist-app\internal\web\generator.go

func Generate(cfg *config.Config, movies []storage.Movie, isMobile bool) error {
	fmt.Printf("🎨 Генерація веб-каталогу %s (mobile=%v)...\n", cfg.AppVersion, isMobile)

	displayMovies := make([]storage.Movie, len(movies))
	for i, m := range movies {
		var posterSrc string
		if isMobile && m.PosterURL != "" {
			posterSrc = m.PosterURL
		} else if m.LocalPosterPath != "" {
			posterSrc = m.LocalPosterPath
			if !isMobile {
				posterSrc = filepath.ToSlash(posterSrc)
			}
		}
		m.LocalPosterPath = posterSrc
		displayMovies[i] = m
	}

	f, err := os.Create(cfg.HTMLPath)
	if err != nil {
		return fmt.Errorf("не вдалося створити файл: %v", err)
	}
	defer f.Close()

	tmpl, err := template.New("catalog").Parse(htmlLayout)
	if err != nil {
		return fmt.Errorf("помилка парсингу шаблону: %v", err)
	}

	data := struct {
		AppVersion     string
		TotalMovies    int
		GenerationTime string
		Movies         []storage.Movie
		GridIcon       template.HTML
		ListIcon       template.HTML
		FaviconBase64  string
	}{
		AppVersion:     cfg.AppVersion,
		TotalMovies:    len(displayMovies),
		GenerationTime: time.Now().Format("02.01.2006 о 15:04"),
		Movies:         displayMovies, // Відправляємо виправлені дані
		GridIcon:       template.HTML(gridIconSVG),
		ListIcon:       template.HTML(listIconSVG),
		FaviconBase64:  appIconBase64(),
	}

	return tmpl.Execute(f, data)
}

func appIconBase64() string {
	icon, err := os.ReadFile(filepath.Join("build", "appicon.png"))
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(icon)
}

const htmlLayout = `<!DOCTYPE html>
<html lang="uk">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>MovieList {{.AppVersion}}</title>
    <link rel="icon" type="image/png" href="data:image/png;base64,{{.FaviconBase64}}">
    <style>
        /* Додаємо стиль для посилання в назві */
        .title-ua a { color: inherit; text-decoration: none; transition: color 0.2s; }
        .title-ua a:hover { color: #e50914; text-decoration: underline; }

        /* (Решта ваших стилів без змін) */
        body { background-color: #121212; color: #e0e0e0; font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; margin: 0; line-height: 1.6; }
        .sticky-header { position: sticky; top: 0; background: rgba(18, 18, 18, 0.95); backdrop-filter: blur(10px); z-index: 1000; padding: 15px 0; border-bottom: 1px solid #333; box-shadow: 0 4px 15px rgba(0,0,0,0.5); }
        .controls { max-width: 95%; margin: 0 auto; display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
        .logo { font-size: 1.5em; font-weight: bold; color: #e50914; text-transform: uppercase; margin-right: auto; }
        .search-input, .sort-select, .btn-action { padding: 10px 15px; border-radius: 8px; border: 1px solid #444; background: #222; color: #fff; font-size: 1em; }
        .search-input { flex-grow: 1; min-width: 200px; }
        .filters-row { display: contents; }
        .btn-action { background: #2b2b2b; cursor: pointer; font-weight: bold; transition: background 0.2s; display: flex; align-items: center; gap: 8px; }
        .btn-action.primary { background: #e50914; border-color: #e50914; }
        .toggle-icon { width: 1.2em; height: 1.2em; fill: currentColor; vertical-align: middle; }
        .stats-bar { max-width: 95%; margin: 15px auto 0; padding: 10px 20px; background: #1a1a1a; border-radius: 8px; border: 1px solid #333; display: flex; gap: 15px; font-size: 0.9em; color: #aaa; align-items: center; box-sizing: border-box; }
        .container { max-width: 95%; margin: 20px auto 30px; }
        .movie-list { display: grid; grid-template-columns: repeat(auto-fit, minmax(650px, 1fr)); gap: 30px; }
        .card { display: flex; background: #1e1e1e; border-radius: 12px; overflow: hidden; border: 1px solid #333; cursor: pointer; }
        .poster-container { flex-shrink: 0; width: 220px; background: #000; }
        .poster { width: 100%; height: 100%; object-fit: cover; cursor: zoom-in; }
        .info { padding: 25px; display: flex; flex-direction: column; width: 100%; overflow: hidden;}
        .title-meta-group { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin-bottom: 5px; }
        .title-ua { width: 100%; font-size: 1.6em; font-weight: bold; margin: 0; color: #ffffff; }
        .year { background: #e50914; color: #fff; padding: 3px 8px; border-radius: 4px; font-size: 0.85em; }
        .genre { color: #e50914; font-size: 0.9em; font-weight: bold; }
        .title-en { font-size: 1em; color: #888; margin: 0 0 15px 0; font-style: italic; }
        .plot { margin-top: 10px; font-size: 0.95em; color: #ccc; flex-grow: 1; }
        .file-path-badge { margin-top: 20px; font-size: 0.85em; color: #aaa; font-family: monospace; text-align: right; border-top: 1px solid #333; padding-top: 5px; }
        .info .file-path-badge { font-size: 0.7rem; color: #888; word-break: break-all; margin-top: auto; padding-top: 6px; border-top: 1px solid #222; }

        /* ВЕРСТКА РЕЖИМУ СПИСКУ (Перенесено з Python) */
        .movie-list.list-mode { grid-template-columns: 1fr; gap: 12px; max-width: 1200px; margin: 0 auto; }
        .movie-list.list-mode .card { flex-direction: row; height: 145px; align-items: stretch; }
        .movie-list.list-mode .poster-container { width: 95px; height: 100%; }
        .movie-list.list-mode .info { padding: 12px 20px; display: grid; grid-template-columns: 48% 1fr; grid-template-rows: auto auto 1fr auto; gap: 2px 25px; width: 100%; }
        .movie-list.list-mode .title-meta-group { grid-column: 1; grid-row: 1; display: grid; grid-template-columns: 240px 50px 1fr; gap: 12px; align-items: start; width: 100%; }
        .movie-list.list-mode .title-ua { width: auto; margin: 0; font-size: 1.25em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; line-height: 1.2; }
        .movie-list.list-mode .year { text-align: center; padding: 2px 5px; font-size: 0.8em; line-height: 1.2; }
        .movie-list.list-mode .genre { margin: 0; font-size: 0.85em; line-height: 1.2; }
        .movie-list.list-mode .title-en { grid-column: 1; grid-row: 2; font-size: 0.85em; margin-bottom: 5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
        .movie-list.list-mode .details { grid-column: 1; grid-row: 3 / 5; font-size: 0.85em; margin: 0; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
        .movie-list.list-mode .plot { grid-column: 2; grid-row: 1 / 4; margin: 0; font-size: 0.85em; color: #bbb; text-align: left; display: -webkit-box; -webkit-line-clamp: 5; -webkit-box-orient: vertical; overflow: hidden; }
        .movie-list.list-mode .file-path-badge { grid-column: 2; grid-row: 4; margin: 0; text-align: right; align-self: end; font-size: 0.75em; border: none; padding: 0; color: #bbb; }

        .no-results { text-align: center; padding: 50px; font-size: 1.2em; color: #888; display: none; }

        .modal { display: none; position: fixed; z-index: 2000; left: 0; top: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.8); backdrop-filter: blur(8px); justify-content: center; align-items: center; }
        .modal-content { max-width: 90vw; max-height: 90vh; }

        /* Адаптив під мобільні екрани */
        @media (max-width: 768px) {
            .controls { flex-direction: column; align-items: stretch; }
            .logo { text-align: center; margin: 0 0 10px 0; }
            .stats-bar { flex-direction: column; gap: 5px; align-items: flex-start; }
            .movie-list:not(.grid-mode) { grid-template-columns: 1fr; }
            .movie-list.list-mode .card { flex-direction: column; height: auto !important; }
            .movie-list.list-mode .poster-container { width: 100% !important; aspect-ratio: 2/3; }
            .movie-list.list-mode .info { display: flex; flex-direction: column; padding: 15px; gap: 10px; }
            .movie-list.list-mode .title-meta-group { display: flex; flex-wrap: wrap; }
            .movie-list.list-mode .title-ua { white-space: normal; }
        }

        /* Мобільний редизайн (CHECKLIST): глобальні відступи, без змін десктопу */
        @media (max-width: 600px) {
            body {
                padding: 12px 16px;
                box-sizing: border-box;
            }
            .container {
                box-sizing: border-box;
                max-width: 100%;
                margin-left: 0;
                margin-right: 0;
            }
            .controls {
                flex-direction: column;
                flex-wrap: nowrap;
                align-items: stretch;
                gap: 0;
                max-width: 100%;
            }
            .logo {
                text-align: center;
                margin: 0 0 8px;
                padding-top: 16px;
            }
            .sticky-header {
                position: static;
                padding: 0;
                border-bottom: 0;
                box-shadow: none;
                background: transparent;
                backdrop-filter: none;
            }
            .filters-row {
                display: flex;
                flex-direction: row;
                flex-wrap: nowrap;
                align-items: center;
                gap: 6px;
                width: 100%;
                position: sticky;
                top: 0;
                z-index: 100;
                padding: 8px 0;
                background-color: rgba(18, 18, 18, 0.9);
                backdrop-filter: blur(10px);
            }
            .filters-row .search-input,
            .filters-row .sort-select,
            .filters-row .btn-view-toggle,
            .filters-row .btn-reset {
                min-width: 0;
                padding: 0 8px;
                font-size: 0.9em;
            }
            .filters-row .search-input,
            .filters-row .sort-select,
            .filters-row .btn-view-toggle,
            .filters-row .btn-reset {
                height: 38px;
                box-sizing: border-box;
                line-height: 1.2;
            }
            .filters-row .search-input {
                flex: 1 1 auto;
                width: auto;
                min-width: 0;
            }
            .filters-row .sort-select {
                flex: 0 0 38px;
                width: 38px;
                background-color: #1a1a1a;
                color: #ffffff;
                border: 1px solid #333;
                appearance: none;
                -webkit-appearance: none;
                -moz-appearance: none;
                background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Ctext x='12' y='16' text-anchor='middle' font-size='17' fill='white'%3E%E2%87%85%3C/text%3E%3C/svg%3E");
                background-repeat: no-repeat;
                background-position: center;
                background-size: 20px 20px;
                /* Hide the selected option text on small screens, keep options text in dropdown */
                color: transparent; /* hide selected text */
                text-indent: -9999px; /* push text out of view for closed select */
            }
            .filters-row .sort-select option {
                background-color: #1a1a1a;
                color: #ffffff;
            }
            .filters-row .btn-view-toggle {
                flex: 0 0 38px;
                width: 38px;
                padding: 6px;
                justify-content: center;
            }
            .filters-row .btn-reset {
                flex: 0 0 38px;
                width: 38px;
                padding: 6px;
                justify-content: center;
            }
            .btn-view-label,
            .btn-reset-text {
                display: none;
            }
            .btn-reset-icon {
                font-size: 1.15em;
                line-height: 1;
            }
            .stats-bar {
                max-width: 100%;
                flex-direction: row;
                flex-wrap: wrap;
                gap: 8px;
                padding: 8px 12px;
                font-size: 0.85em;
            }
            /* Сітка: рівно два постери в ряд */
            .movie-list.grid-mode {
                display: grid;
                grid-template-columns: 1fr 1fr;
                gap: 12px;
            }
            .movie-list.grid-mode .card {
                display: flex;
                flex-direction: column;
                height: auto !important;
                padding: 8px;
                overflow: hidden;
            }
            .movie-list.grid-mode .poster-container {
                width: 100% !important;
                max-width: 100%;
                height: auto !important;
                aspect-ratio: 2/3;
            }
            .movie-list.grid-mode .poster {
                width: 100%;
                max-width: 100%;
                height: auto;
                border-radius: 6px;
                object-fit: cover;
            }
            .movie-list.grid-mode .info {
                padding: 8px 2px 0;
                display: flex;
                flex-direction: column;
                min-width: 0;
            }
            .movie-list.grid-mode .title-meta-group {
                flex-direction: column;
                align-items: flex-start;
                gap: 4px;
                margin-bottom: 0;
            }
            .movie-list.grid-mode .title-ua {
                font-size: 0.92em;
                line-height: 1.25;
                width: 100%;
                display: -webkit-box;
                -webkit-line-clamp: 2;
                -webkit-box-orient: vertical;
                overflow: hidden;
                height: 2.5em;
            }
            .movie-list.grid-mode .year {
                font-size: 0.78em;
            }
            .movie-list.grid-mode .genre,
            .movie-list.grid-mode .title-en,
            .movie-list.grid-mode .details,
            .movie-list.grid-mode .file-path-badge {
                display: none !important;
            }
            /* Keep plot visible in compact grid cards and allow more lines */
            .movie-list.grid-mode .plot {
                display: -webkit-box !important;
                -webkit-line-clamp: 4;
                -webkit-box-orient: vertical;
                overflow: hidden;
                margin-top: 6px;
                font-size: 0.92em;
                color: #ccc;
                line-height: 1.35;
            }

            /* Список: постер зліва, сюжет до 4 рядків під постером */
            .movie-list.list-mode {
                display: flex;
                flex-direction: column;
                gap: 12px;
            }
            .movie-list.list-mode .card {
                display: grid;
                grid-template-columns: 95px 1fr;
                gap: 12px;
                padding: 12px;
                height: auto !important;
                max-height: none;
            }
            .movie-list.list-mode .poster-container {
                grid-column: 1;
                grid-row: 1 / span 3;
                width: 95px !important;
                max-width: 95px;
                height: auto !important;
                aspect-ratio: 2/3;
            }
            .movie-list.list-mode .poster {
                width: 100%;
                max-width: 100%;
                border-radius: 6px;
                object-fit: cover;
            }
            .movie-list.list-mode .info {
                display: contents;
            }
            .movie-list.list-mode .title-meta-group {
                grid-column: 2;
                grid-row: 1;
                display: flex;
                flex-wrap: wrap;
                gap: 6px;
                margin-bottom: 0;
            }
            .movie-list.list-mode .title-ua {
                font-size: 1.05em;
                width: 100%;
                white-space: normal;
            }
            .movie-list.list-mode .title-en {
                grid-column: 2;
                grid-row: 2;
                font-size: 0.85em;
                margin: 0;
            }
            .movie-list.list-mode .details {
                grid-column: 2;
                grid-row: 3;
                font-size: 0.78em; /* slightly smaller to save vertical space */
                margin: 0;
                display: -webkit-box;
                -webkit-line-clamp: 2;
                -webkit-box-orient: vertical;
                overflow: hidden;
                color: #bdbdbd;
                line-height: 1.25;
            }
            .movie-list.list-mode .file-path-badge {
                grid-column: 1 / span 2;
                grid-row: 5;
                display: block;
                font-size: 0.72em;
                color: #888;
                word-break: break-all;
                margin: 2px 0 0;
                padding-top: 6px;
                border-top: 1px solid #222;
                text-align: left;
                font-family: monospace;
            }
            .movie-list.list-mode .plot {
                grid-column: 1 / span 2;
                grid-row: 4;
                margin-top: 6px;
                font-size: 0.95em;
                line-height: 1.45;
                display: -webkit-box;
                -webkit-line-clamp: 7; /* allow more visible lines on mobile */
                -webkit-box-orient: vertical;
                overflow: hidden;
            }
        }
    </style>
</head>
<body>
    <header class="sticky-header">
        <div class="controls">
            <div class="logo">🍿 MovieList</div>
            <div class="filters-row">
                <input type="text" id="searchInput" class="search-input" placeholder="Пошук за назвою…" value="">
                <select id="sortSelect" class="sort-select" title="Сортування">
                    <option value="year-desc">Новіші</option>
                    <option value="year-asc">Старіші</option>
                    <option value="title-asc">А-Я</option>
                </select>
                <button id="viewToggle" type="button" class="btn-action btn-view-toggle" aria-label="Режим відображення" title="Список / Сітка">{{.ListIcon}}<span class="btn-view-label"> Список</span></button>
                <button id="resetBtn" type="button" class="btn-action primary btn-reset" aria-label="Скинути фільтри" title="Скинути">
                    <span class="btn-reset-icon" aria-hidden="true">↺</span>
                    <span class="btn-reset-text">Скинути</span>
                </button>
            </div>
        </div>
    </header>

    <div class="stats-bar">
        <span>🎬 Всього: <strong>{{.TotalMovies}}</strong></span>
        <span>📅 Оновлено: <strong>{{.GenerationTime}}</strong></span>
        <span id="filteredCount" style="display:none">🔎 Знайдено: <strong id="filteredNum">0</strong></span>
    </div>

    <div class="container">
        <div class="movie-list grid-mode" id="movieList">
            {{range .Movies}}
            <div class="card" data-year="{{.Year}}" data-title="{{.TitleUA}}">
                <div class="poster-container">
                    <img class="poster" src="{{if .LocalPosterPath}}{{.LocalPosterPath}}{{else}}data:image/svg+xml;charset=UTF-8,%3Csvg xmlns='http://www.w3.org/2000/svg' width='500' height='750' style='background:%23222'%3E%3Ctext x='50%25' y='50%25' fill='%23666' font-size='24' font-family='sans-serif' text-anchor='middle' dy='.3em'%3EПостер відсутній%3C/text%3E%3C/svg%3E{{end}}" alt="{{.TitleUA}}" loading="lazy">
                </div>
                <div class="info">
                    <div class="title-meta-group">
                        <h2 class="title-ua">
                            <a href="{{if .TmdbID}}https://www.themoviedb.org/{{if eq .MediaType "tv"}}tv{{else}}movie{{end}}/{{.TmdbID}}{{else}}https://www.themoviedb.org/search/{{if eq .MediaType "tv"}}tv{{else}}movie{{end}}?query={{.TitleUA}}{{end}}"
                               target="_blank"
                               onclick="event.stopPropagation();">
                               {{.TitleUA}}
                            </a>
                        </h2>
                        <span class="year">{{if .Year}}{{.Year}}{{else}}—{{end}}</span>
                        <span class="genre">{{.Genres}}</span>
                    </div>
                    <h3 class="title-en">{{.TitleEN}}</h3>
                    <div class="details"><strong>Актори:</strong> {{.Cast}}</div>
                    <div class="plot"><strong>Сюжет:</strong><br>{{.Plot}}</div>
                    <div class="file-path-badge">📁 {{.Filename}}</div>
                </div>
            </div>
            {{end}}
        </div>
    </div>
    <div id="noResults" class="no-results" style="display:none">Нічого не знайдено 🎬</div>
    <div id="imageModal" style="display: none; position: fixed; z-index: 9999; left: 0; top: 0; width: 100%; height: 100%; background-color: rgba(0,0,0,0.85); align-items: center; justify-content: center; cursor: pointer;">
        <img id="modalImage" style="max-height: 90vh; max-width: 90vw; border-radius: 8px; box-shadow: 0 10px 30px rgba(0,0,0,0.8); cursor: default;">
    </div>
    <script>
        const searchInput = document.getElementById('searchInput');
        const sortSelect = document.getElementById('sortSelect');
        const movieList = document.getElementById('movieList');
        const viewToggle = document.getElementById('viewToggle');
        const cards = Array.from(document.querySelectorAll('.card'));

        const gridIcon = '<svg viewBox="0 0 24 24" class="toggle-icon"><path d="M4 4h4v4H4V4m6 0h4v4h-4V4m6 0h4v4h-4V4M4 10h4v4H4v-4m6 0h4v4h-4v-4m6 0h4v4h-4v-4M4 16h4v4H4v-4m6 0h4v4h-4v-4m6 0h4v4h-4v-4Z"/></svg>';
        const listIcon = '<svg viewBox="0 0 24 24" class="toggle-icon"><path d="M4 6h16v2H4V6m0 5h16v2H4v-2m0 5h16v2H4v-2m-3 0h2v2H1v-2m0-5h2v2H1v-2m0-5h2v2H1V6"/></svg>';

        function normalizeSearchValue(value) {
            if (value == null || value === 'null' || value === 'undefined') {
                return '';
            }
            return String(value);
        }

        function initSearchInput() {
            let value = normalizeSearchValue(searchInput.value);
            try {
                const saved = localStorage.getItem('searchQuery');
                if (saved !== null) {
                    value = normalizeSearchValue(saved);
                }
            } catch (e) {}
            searchInput.value = value;
        }

        // Універсальна функція перемикання вигляду
        let isListView = localStorage.getItem('viewMode') === 'list';

        function setViewMode(toList) {
            isListView = toList;
            if(isListView) {
                movieList.classList.add('list-mode');
                movieList.classList.remove('grid-mode');
                viewToggle.innerHTML = gridIcon + '<span class="btn-view-label"> Сітка</span>';
                viewToggle.title = 'Сітка';
            } else {
                movieList.classList.add('grid-mode');
                movieList.classList.remove('list-mode');
                viewToggle.innerHTML = listIcon + '<span class="btn-view-label"> Список</span>';
                viewToggle.title = 'Список';
            }
            try { localStorage.setItem('viewMode', isListView ? 'list' : 'grid'); } catch (e) {}
        }

        // Ініціалізація при завантаженні
        if(isListView) {
            setViewMode(true);
        }

        // Клік по верхній кнопці
        viewToggle.addEventListener('click', () => {
            setViewMode(!isListView);
        });

        // Пошук та сортування
        function filterAndSort() {
            const query = normalizeSearchValue(searchInput.value).toLowerCase().trim();
            const sort = sortSelect.value;

            let visible = cards.filter(card => {
                const text = card.innerText.toLowerCase();
                const isMatch = text.includes(query);
                card.style.display = isMatch ? '' : 'none';
                return isMatch;
            });

            try {
                if (query) {
                    localStorage.setItem('searchQuery', query);
                } else {
                    localStorage.removeItem('searchQuery');
                }
            } catch (e) {}

            document.getElementById('noResults').style.display = visible.length ? 'none' : 'block';
            document.getElementById('filteredCount').style.display = query ? 'inline' : 'none';
            document.getElementById('filteredNum').innerText = visible.length;

            visible.sort((a, b) => {
                if(sort.startsWith('year')) {
                    const yA = parseInt(a.dataset.year) || 0;
                    const yB = parseInt(b.dataset.year) || 0;
                    return sort === 'year-desc' ? yB - yA : yA - yB;
                }
                const tA = a.dataset.title.toLowerCase();
                const tB = b.dataset.title.toLowerCase();
                return sort === 'title-asc' ? tA.localeCompare(tB, 'uk') : tB.localeCompare(tA, 'uk');
            });

            visible.forEach(c => movieList.appendChild(c));
        }

        initSearchInput();

        searchInput.addEventListener('input', filterAndSort);
        sortSelect.addEventListener('change', filterAndSort);
        document.getElementById('resetBtn').onclick = () => {
            searchInput.value = '';
            try { localStorage.removeItem('searchQuery'); } catch (e) {}
            filterAndSort();
        };

        filterAndSort();

        // Взаємодія з картками та постерами
        const modalWrapper = document.getElementById('imageModal');
        const modalImage   = document.getElementById('modalImage');

        movieList.addEventListener('click', e => {
            // 1. Клік по постеру — тільки модалка
            if (e.target.classList.contains('poster')) {
                modalImage.src = e.target.src;
                modalWrapper.style.display = 'flex';
                return;
            }

            // 2. Клік по картці — toggle режиму та центрування
            const card = e.target.closest('.card');
            if (card) {
                // Перемикаємо режим на протилежний
                setViewMode(!isListView);

                // Чекаємо 320мс (CSS transition = 300ms), щоб DOM остаточно
                // прийняв нові розміри, і робимо плавний скрол
                setTimeout(() => {
                    card.scrollIntoView({ behavior: 'smooth', block: 'center' });

                    // Додаємо легке візуальне підсвічування, щоб око сфокусувалось
                    card.style.borderColor = '#e50914';
                    setTimeout(() => card.style.borderColor = '#333', 1000);
                }, 320);
            }
        });

        // Закриття модалки
        modalWrapper.addEventListener('click', () => {
            modalWrapper.style.display = 'none';
            modalImage.src = '';
        });
    </script>
</body>
</html>`
