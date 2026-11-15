export function metadataValues(value) {
    return String(value || '').split(/[,·]/u).map(part => part.trim()).filter(Boolean);
}

export function matchesCollectionSearch(movie, query = '') {
    const text = [movie.title_ua, movie.title_en, movie.filename, movie.file_label,
        movie.year, movie.genres, movie.cast].join(' ').toLocaleLowerCase('uk-UA');
    return text.includes(query.trim().toLocaleLowerCase('uk-UA'));
}
