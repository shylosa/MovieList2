export function modelRows(current, available, fetched) {
    const selected = new Set(current);
    const catalog = new Set(available);
    return [...new Set([...current, ...available])].map(name => ({
        name, selected: selected.has(name), unavailable: fetched && !catalog.has(name)
    }));
}

export function moveModel(current, name, offset) {
    const next = [...current];
    const index = next.indexOf(name), target = index + offset;
    if (index >= 0 && target >= 0 && target < next.length) [next[index], next[target]] = [next[target], next[index]];
    return next;
}

export function createModelTables({api, document: doc = document}) {
    const host = doc.getElementById('models-content');
    let loadPromise;
    let loaded = false;
    function element(tag, text, className) {
        const node = doc.createElement(tag);
        if (text) node.textContent = text;
        if (className) node.className = className;
        return node;
    }
    async function load() {
        if (loaded) return;
        if (loadPromise) return loadPromise;
        loadPromise = (async () => {
            const selections = await api.GetModelSelections();
            host.replaceChildren(); host.className = 'folder-columns model-columns';
            for (const [provider, title] of [['gemini', 'Google Gemini'], ['groq', 'Groq']]) {
                let current = [...(selections[provider].current || [])], available = [], fetched = false, busy = false;
                const column = element('section', '', 'folder-column');
                const heading = element('div', '', 'folder-column-heading');
                const request = element('button', 'Запитати доступні моделі'); request.type = 'button';
                const status = element('p', selections[provider].configured ? 'Поточний вибір' : `Ключ ${provider === 'gemini' ? 'GEMINI' : 'GROQ'}_API_KEY не задано`, 'model-provider-status');
                status.setAttribute('role', 'status');
                heading.append(element('h3', title), request);
                const scroll = element('div', '', 'folder-table-scroll');
                const table = element('table', '', 'folder-table model-table');
                const caption = element('caption', provider === 'gemini' ? 'Основний каскад · порядок зверху вниз' : 'Резерв після Gemini · порядок зверху вниз');
                const body = element('tbody'); table.append(caption, body); scroll.append(table);
                column.append(heading, status, scroll); host.append(column);
                function render() {
                    body.replaceChildren();
                    request.disabled = busy || !selections[provider].configured;
                    for (const row of modelRows(current, available, fetched)) {
                        const tr = element('tr'); const cell = element('td'); const actions = element('td');
                        const label = element('label', '', 'model-choice');
                        const check = element('input'); check.type = 'checkbox'; check.checked = row.selected;
                        check.disabled = busy || (!row.selected && row.unavailable) || (row.selected && current.length === 1);
                        check.addEventListener('change', () => save(check.checked ? [...current, row.name] : current.filter(name => name !== row.name)));
                        label.append(check, element('span', row.name)); cell.append(label);
                        if (row.unavailable) cell.append(element('small', 'Немає в отриманому каталозі', 'model-unavailable'));
                        if (row.selected) {
                            const index = current.indexOf(row.name);
                            for (const [offset, symbol, description] of [[-1, '↑', 'Вище'], [1, '↓', 'Нижче']]) {
                                const button = element('button', symbol); button.type = 'button';
                                button.setAttribute('aria-label', `${description}: ${row.name}`);
                                button.disabled = busy || index + offset < 0 || index + offset >= current.length;
                                button.addEventListener('click', () => save(moveModel(current, row.name, offset)));
                                actions.append(button);
                            }
                        }
                        tr.append(cell, actions); body.append(tr);
                    }
                    if (!body.children.length) { const row = element('tr'), cell = element('td', 'Моделі не вибрано'); cell.colSpan = 2; row.append(cell); body.append(row); }
                }
                async function save(next) {
                    if (busy) return;
                    busy = true; status.textContent = 'Збереження…'; render();
                    try { await api.SetProviderModels(provider, next); current = next; status.textContent = 'Збережено. Застосується до наступної операції.'; }
                    catch (error) { status.textContent = `Не вдалося зберегти: ${error}`; }
                    finally { busy = false; render(); }
                }
                request.addEventListener('click', async () => {
                    if (busy) return;
                    busy = true; status.textContent = 'Запит до API…'; render();
                    try {
                        const catalog = await (provider === 'gemini' ? api.GetAIModelCatalog() : api.GetGroqModelCatalog());
                        available = catalog.available || []; fetched = true;
                        status.textContent = available.length ? `Доступних моделей: ${available.length}` : 'API не повернув сумісних текстових моделей';
                    } catch (error) { status.textContent = `Не вдалося отримати каталог: ${error}`; }
                    finally { busy = false; render(); }
                });
                render();
            }
            loaded = true;
        })();
        try { await loadPromise; } finally { loadPromise = null; }
    }
    return {load};
}
