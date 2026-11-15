import assert from 'assert';
import './model-tables.test.mjs';
import './metadata-popover.test.mjs';
import './settings-menu.test.mjs';
import './folder-tables.test.mjs';
import './config-editor.test.mjs';
import './about-dialog.test.mjs';
import { metadataValues, matchesCollectionSearch } from './metadata.js';
import { shouldKeepEditorInspector } from './editor-state.js';

assert.deepStrictEqual(metadataValues('Драма'), ['Драма']);
assert.deepStrictEqual(metadataValues('Драма, Фантастика · Трилер'), ['Драма', 'Фантастика', 'Трилер']);
assert.deepStrictEqual(metadataValues('Gary Oldman, Tom Hardy'), ['Gary Oldman', 'Tom Hardy']);
assert.deepStrictEqual(metadataValues(null), []);
for (const media_type of ['movie', 'tv']) {
    const movie = {media_type, title_ua: 'Український фільм', filename: 'Folder/file.mkv', genres: 'Драма, Фантастика', cast: 'Gary Oldman, Tom Hardy', year: '2014'};
    for (const query of ['драма', 'ФАНТАСТИКА', 'Gary Oldman', 'Tom Hardy', '2014', 'український', 'file.mkv', '']) {
        assert.strictEqual(matchesCollectionSearch(movie, query), true);
        assert.strictEqual(matchesCollectionSearch(movie, query), true);
    }
    assert.strictEqual(matchesCollectionSearch(movie, 'невідомий актор'), false);
    assert.strictEqual(matchesCollectionSearch(movie, ''), true);
}
assert.strictEqual(matchesCollectionSearch({}, 'Tom Hardy'), false);
import { candidateConfirmPayload, candidateSearchPayload, fixPayload, reviewCounts, reviewReasonLabel, mediaTypeLabel, candidateTMDBURL, formatRuntime, formatTMDBRating, candidateStatus, scanLifecycleTransition, cacheEditorValue, filterAndSortEditorMovies, updateEditorSelection } from './editor-state.js';

assert.deepStrictEqual(candidateSearchPayload('a.mkv', {'a.mkv': 'The Bureau'}, {'a.mkv': 'tv'}), {filename: 'a.mkv', title: 'The Bureau', media_type: 'tv'});
assert.strictEqual(reviewReasonLabel('identity_conflict'), 'відхилено автоматичну заміну ідентичності');
assert.deepStrictEqual(candidateConfirmPayload('a.mkv', {tmdb_id: 62476, media_type: 'tv'}), {filename: 'a.mkv', tmdb_id: 62476, media_type: 'tv'});
assert.deepStrictEqual(candidateSearchPayload('a.mkv', {}, {}, 'The Bureau'), {filename: 'a.mkv', title: '', media_type: 'auto'});
assert.deepStrictEqual(candidateSearchPayload('Ebigejl.2024.mkv', {}, {}, 'Любимые фильмы'), {filename: 'Ebigejl.2024.mkv', title: '', media_type: 'auto'});
assert.strictEqual(reviewReasonLabel('ambiguous_exact'), 'кілька близьких точних збігів');
assert.strictEqual(reviewReasonLabel('duplicate_tmdb_id'), 'один TMDB ID у різних фільмів');
assert.strictEqual(mediaTypeLabel('tv'), 'Серіал');
assert.strictEqual(candidateTMDBURL({media_type: 'tv', tmdb_id: 62476}), 'https://www.themoviedb.org/tv/62476');
assert.strictEqual(candidateTMDBURL({media_type: 'invalid', tmdb_id: 1}), '');
assert.strictEqual(formatRuntime(104), '1 год 44 хв');
assert.strictEqual(formatRuntime(14), '14 хв');
assert.match(formatTMDBRating(7.12, 250), /★ 7\.1/);
assert.deepStrictEqual(fixPayload(new Set(['a']), {a: 'Hint'}, {a: 'movie'}), [{filename: 'a', hint: 'Hint', media_type: 'movie'}]);
assert.deepStrictEqual(reviewCounts([{tmdb_id: 0, needs_review: true}, {tmdb_id: 2, needs_review: true}, {tmdb_id: 3}]), {unresolved: 1, suspicious: 1});
assert.deepStrictEqual(candidateStatus([]), {kind: 'empty', text: 'Нічого не знайдено'});
assert.strictEqual(candidateStatus(null, 'network').kind, 'error');
assert.strictEqual(candidateStatus([{}]).kind, 'ready');
assert.strictEqual(scanLifecycleTransition(false, 'scan-started'), true);
assert.strictEqual(scanLifecycleTransition(true, 'scan-finished'), false);
assert.strictEqual(scanLifecycleTransition(true, 'scan-progress'), true);
const cachedHints = {}, cachedTypes = {};
cacheEditorValue(cachedHints, 'a.mkv', 'Abigail');
cacheEditorValue(cachedTypes, 'a.mkv', 'movie');
assert.deepStrictEqual(candidateSearchPayload('a.mkv', cachedHints, cachedTypes), {filename: 'a.mkv', title: 'Abigail', media_type: 'movie'});
const editorMovies = [
    {filename: 'B.mkv', title_ua: 'Бета', year: 2018, tmdb_id: 1, needs_review: false, vote_average: 6.2},
    {filename: 'A.mkv', title_ua: 'Альфа', year: 2020, tmdb_id: 2, needs_review: true, vote_average: 7.8},
    {filename: 'C.mkv', file_label: 'Невідомий', year: 2019, tmdb_id: 0, needs_review: false, vote_average: 0},
];
const repairedMovies = editorMovies.map(movie => movie.filename === 'C.mkv' ? {...movie, tmdb_id: 42, title_ua: 'Визначений фільм'} : movie);
for (const filter of ['review', 'unresolved']) {
    const before = filterAndSortEditorMovies(editorMovies, '', filter);
    const after = filterAndSortEditorMovies(repairedMovies, '', filter);
    assert.strictEqual(shouldKeepEditorInspector('C.mkv', editorMovies, before), true);
    assert.strictEqual(after.some(movie => movie.filename === 'C.mkv'), false);
    assert.strictEqual(shouldKeepEditorInspector('C.mkv', repairedMovies, after, true), true);
    assert.strictEqual(shouldKeepEditorInspector('C.mkv', repairedMovies, after), false);
}
// The last problem can disappear while its updated inspector remains open.
assert.strictEqual(shouldKeepEditorInspector('C.mkv', repairedMovies, [], true), true);
// Background updates keep the record the user is currently inspecting.
assert.strictEqual(shouldKeepEditorInspector('A.mkv', repairedMovies, [], true), true);
// A deleted record must close its inspector even during a data refresh.
assert.strictEqual(shouldKeepEditorInspector('C.mkv', repairedMovies.filter(movie => movie.filename !== 'C.mkv'), [], true), false);
assert.strictEqual(shouldKeepEditorInspector('C.mkv', repairedMovies, filterAndSortEditorMovies(repairedMovies, 'інший запит')), false);
assert.deepStrictEqual(filterAndSortEditorMovies(editorMovies, '', 'all', 'title').map(movie => movie.filename), ['A.mkv', 'B.mkv', 'C.mkv']);
assert.deepStrictEqual(filterAndSortEditorMovies(editorMovies, '', 'review', 'year').map(movie => movie.filename), ['A.mkv', 'C.mkv']);
assert.deepStrictEqual(filterAndSortEditorMovies(editorMovies, 'невідомий', 'unresolved').map(movie => movie.filename), ['C.mkv']);
assert.deepStrictEqual(filterAndSortEditorMovies(editorMovies, '', 'all', 'rating').map(movie => movie.filename), ['A.mkv', 'B.mkv', 'C.mkv']);
assert.deepStrictEqual(filterAndSortEditorMovies(editorMovies, '', 'all', 'added').map(movie => movie.filename), ['B.mkv', 'A.mkv', 'C.mkv']);
assert.deepStrictEqual(filterAndSortEditorMovies(editorMovies, '', 'review', 'added').map(movie => movie.filename), ['A.mkv', 'C.mkv']);
assert.deepStrictEqual(filterAndSortEditorMovies(editorMovies, 'mkv', 'unresolved', 'added').map(movie => movie.filename), ['C.mkv']);
const withNewMovie = [...editorMovies, {filename: 'AA-new.mkv', title_ua: 'Аа', tmdb_id: 3}];
assert.deepStrictEqual(filterAndSortEditorMovies(withNewMovie, '', 'all', 'added').map(movie => movie.filename), ['B.mkv', 'A.mkv', 'C.mkv', 'AA-new.mkv']);
assert.deepStrictEqual(editorMovies.map(movie => movie.filename), ['B.mkv', 'A.mkv', 'C.mkv']);
let selection = updateEditorSelection(new Set(), ['A.mkv', 'B.mkv'], true);
selection = updateEditorSelection(selection, ['B.mkv'], false);
selection = updateEditorSelection(selection, ['C.mkv'], true);
assert.deepStrictEqual([...selection].sort(), ['A.mkv', 'C.mkv']);
assert.deepStrictEqual([...updateEditorSelection(selection, ['C.mkv'], false)], ['A.mkv']);
const batchHints = {'A.mkv': 'IMDb tt1234567', 'C.mkv': 'Інша назва'};
const batchTypes = {'A.mkv': 'movie', 'C.mkv': 'tv'};
assert.deepStrictEqual(fixPayload(selection, batchHints, batchTypes), [
    {filename: 'A.mkv', hint: 'IMDb tt1234567', media_type: 'movie'},
    {filename: 'C.mkv', hint: 'Інша назва', media_type: 'tv'},
]);
console.log('frontend state tests passed');
