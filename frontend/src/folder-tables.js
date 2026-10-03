export function createFolderTables({doc = document, api}) {
    const status = doc.getElementById('folders-status');
    let state = {folders: [], excluded: []};
    let working = false;
    let scanning = false;
    function updateDisabled() {
        for (const button of doc.getElementById('panel-folders').querySelectorAll('button')) button.disabled = working || scanning;
    }
    function renderList(kind, values) {
        const body = doc.getElementById(`${kind}-rows`);
        body.replaceChildren();
        doc.getElementById(`${kind}-count`).textContent = String(values.length);
        if (!values.length) {
            const row = doc.createElement('tr'); const cell = doc.createElement('td');
            cell.colSpan = 2; cell.className = 'folders-empty';
            cell.textContent = kind === 'scan' ? 'Додайте папку для сканування' : 'Виключених папок немає';
            row.appendChild(cell); body.appendChild(row);
        }
        for (const path of values) {
            const row = doc.createElement('tr');
            const cell = doc.createElement('td'); cell.className = 'folder-table-path'; cell.textContent = path; cell.title = path;
            const action = doc.createElement('td');
            const remove = doc.createElement('button'); remove.type = 'button'; remove.className = 'folder-remove'; remove.textContent = '×';
            remove.title = 'Прибрати зі списку'; remove.setAttribute('aria-label', `Прибрати зі списку: ${path}`);
            remove.onclick = () => change(async () => {
                if (kind === 'scan') await api.SetScanFolders(state.folders.filter(value => value !== path));
                else await api.SetExcludedFolders(state.excluded.filter(value => value !== path));
            });
            action.appendChild(remove); row.append(cell, action); body.appendChild(row);
        }
    }
    async function load() {
        state = await api.GetScanFolders();
        renderList('scan', state.folders || []);
        renderList('excluded', state.excluded || []);
        updateDisabled();
    }
    async function change(action) {
        if (working || scanning) return;
        working = true; updateDisabled(); status.textContent = '';
        try { await action(); await load(); }
        catch (error) { status.textContent = String(error); }
        finally { working = false; updateDisabled(); }
    }
    doc.getElementById('add-scan-folder').onclick = () => change(api.SelectScanFolder);
    doc.getElementById('add-excluded-folder').onclick = () => change(api.SelectExcludedFolder);
    return {load, setScanning(value) { scanning = value; updateDisabled(); }};
}
