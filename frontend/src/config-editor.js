export function highlightEnvText(highlight, text) {
    const doc = highlight.ownerDocument;
    highlight.replaceChildren();
    for (const line of text.split('\n')) {
        const span = doc.createElement('span');
        if (/^\s*#/.test(line)) { span.className = 'env-comment'; span.textContent = line; }
        else {
            const assignment = /^(\s*(?:export\s+)?[\w.]+\s*=\s*)(.*)$/.exec(line);
            if (assignment) {
                const key = doc.createElement('span'); key.className = 'env-key'; key.textContent = assignment[1];
                const value = doc.createElement('span'); value.className = 'env-value'; value.textContent = assignment[2];
                span.append(key, value);
            } else span.textContent = line;
        }
        highlight.append(span, doc.createTextNode('\n'));
    }
}

export function createConfigEditor({input, status, path, highlight, api, delay = 1000}) {
    let loaded = false;
    let saved = '';
    let revision = '';
    let timer;
    let running;
    function refreshHighlight() {
        if (!highlight) return;
        highlightEnvText(highlight, input.value);
        highlight.scrollTop = input.scrollTop;
        highlight.scrollLeft = input.scrollLeft;
    }
    async function load() {
        if (loaded) return;
        input.disabled = true;
        status.textContent = 'Завантаження…';
        try {
            const doc = await api.GetEnvConfig();
            input.value = doc.content;
            refreshHighlight();
            saved = doc.content;
            revision = doc.revision;
            path.textContent = doc.path;
            loaded = true;
            status.textContent = 'Збережено';
            input.disabled = false;
        } catch (error) { status.textContent = String(error); }
    }
    async function flush() {
        clearTimeout(timer);
        if (running) { await running; return flush(); }
        if (!loaded || input.value === saved) return true;
        const snapshot = input.value;
        status.textContent = 'Зберігається…';
        running = (async () => {
            try {
                revision = await api.SaveEnvConfig(snapshot, revision);
                saved = snapshot;
                status.textContent = input.value === saved ? 'Збережено · застосування після перезапуску' : 'Є незбережені зміни';
                return true;
            } catch (error) { status.textContent = String(error); return false; }
        })();
        const result = await running;
        running = null;
        return result;
    }
    input.addEventListener('input', () => {
        refreshHighlight();
        status.textContent = 'Є незбережені зміни';
        clearTimeout(timer);
        timer = setTimeout(flush, delay);
    });
    input.addEventListener('blur', flush);
    if (highlight) input.addEventListener('scroll', () => { highlight.scrollTop = input.scrollTop; highlight.scrollLeft = input.scrollLeft; });
    return {load, flush};
}
