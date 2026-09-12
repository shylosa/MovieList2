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
