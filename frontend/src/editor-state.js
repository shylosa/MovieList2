export function candidateSearchPayload(filename, hints, mediaTypes) {
    return { filename, title: hints[filename] || '', media_type: mediaTypes[filename] || 'auto' };
}

export function reviewReasonLabel(reason) {
    return ({ambiguous_exact: 'кілька близьких точних збігів', low_verification_score: 'низька оцінка перевірки', year_conflict: 'конфлікт року', media_type_conflict: 'конфлікт типу', duplicate_tmdb_id: 'один TMDB ID у різних фільмів'})[reason] || 'потребує ручної перевірки';
}

export function mediaTypeLabel(mediaType) {
    return mediaType === 'tv' ? 'Серіал' : 'Фільм';
}

export function candidateTMDBURL(candidate) {
    const mediaType = candidate.media_type === 'tv' ? 'tv' : candidate.media_type === 'movie' ? 'movie' : '';
    const id = Number(candidate.tmdb_id);
    return mediaType && Number.isInteger(id) && id > 0 ? `https://www.themoviedb.org/${mediaType}/${id}` : '';
}

export function formatRuntime(minutes) {
    const value = Number(minutes) || 0;
    if (!value) return 'тривалість невідома';
    const hours = Math.floor(value / 60), rest = value % 60;
    return hours ? `${hours} год ${rest} хв` : `${rest} хв`;
}

export function formatTMDBRating(average, count) {
    return Number(count) > 0 ? `★ ${Number(average).toFixed(1)} · ${Number(count).toLocaleString('uk-UA')} голосів` : 'Ще немає оцінок';
}

export function candidateConfirmPayload(filename, candidate) {
    return { filename, tmdb_id: candidate.tmdb_id, media_type: candidate.media_type };
}

export function fixPayload(filenames, hints, mediaTypes) {
    return Array.from(filenames, filename => ({ filename, hint: hints[filename] || '', media_type: mediaTypes[filename] || 'auto' }));
}

export function reviewCounts(movies) {
    return {
        unresolved: movies.filter(movie => !movie.tmdb_id).length,
        suspicious: movies.filter(movie => movie.tmdb_id > 0 && movie.needs_review).length,
    };
}

export function createRequestGate() {
    const active = new Set();
    return {
        begin(key) { if (active.has(key)) return false; active.add(key); return true; },
        end(key) { active.delete(key); },
        has(key) { return active.has(key); },
    };
}

export function candidateStatus(candidates, error = null) {
    if (error) return {kind: 'error', text: `Помилка пошуку: ${error}`};
    if (!candidates || candidates.length === 0) return {kind: 'empty', text: 'Нічого не знайдено'};
    return {kind: 'ready', text: ''};
}

export function scanLifecycleTransition(scanning, eventName) {
    if (eventName === 'scan-started') return true;
    if (eventName === 'scan-finished') return false;
    return scanning;
}

export function cacheEditorValue(cache, filename, value) {
    cache[filename] = value;
    return cache;
}

export function floatingPopoverPosition(anchor, popover, viewport, margin = 8) {
    let left = Math.min(anchor.left, viewport.width - popover.width - margin);
    left = Math.max(margin, left);
    let top = anchor.bottom + 6;
    if (top + popover.height > viewport.height - margin) top = Math.max(margin, anchor.top - popover.height - 6);
    return {left, top};
}
