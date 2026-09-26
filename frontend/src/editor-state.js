export function candidateSearchPayload(filename, hints, mediaTypes) {
    return { filename, title: hints[filename] || '', media_type: mediaTypes[filename] || 'auto' };
}

export function reviewReasonLabel(reason) {
    return ({ambiguous_exact: 'кілька близьких точних збігів', low_verification_score: 'низька оцінка перевірки', year_conflict: 'конфлікт року', media_type_conflict: 'конфлікт типу', identity_conflict: 'відхилено автоматичну заміну ідентичності', duplicate_tmdb_id: 'один TMDB ID у різних фільмів'})[reason] || 'потребує ручної перевірки';
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

export function filterAndSortEditorMovies(movies, query = '', filter = 'all', sort = 'title') {
    const search = query.trim().toLocaleLowerCase('uk-UA');
    const result = movies.filter(movie => {
        if (filter === 'review' && !(movie.needs_review || !movie.tmdb_id)) return false;
        if (filter === 'unresolved' && movie.tmdb_id) return false;
        const text = [movie.filename, movie.file_label, movie.title_ua, movie.title_en, movie.year].join(' ').toLocaleLowerCase('uk-UA');
        return text.includes(search);
    });
    const title = movie => movie.title_ua || movie.title_en || movie.file_label || movie.filename || '';
    result.sort((a, b) => {
        let difference = 0;
        if (sort === 'year') difference = Number(b.year || 0) - Number(a.year || 0);
        else if (sort === 'rating') difference = Number(b.vote_average || 0) - Number(a.vote_average || 0);
        else if (sort === 'filename') difference = (a.filename || '').localeCompare(b.filename || '', 'uk');
        else difference = title(a).localeCompare(title(b), 'uk');
        return difference || (a.filename || '').localeCompare(b.filename || '', 'uk');
    });
    return result;
}

export function updateEditorSelection(selection, filenames, checked) {
    const next = new Set(selection);
    for (const filename of filenames) {
        if (checked) next.add(filename);
        else next.delete(filename);
    }
    return next;
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
