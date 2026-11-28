package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
	"movielist-app/internal/utils"
)

// recognition_coordinator.go — єдиний координатор автоматичного розпізнавання.
// RunScan володіє lifecycle скану, дисковим обходом, подіями UI, метриками та
// фінальною локалізацією; тут зібрано лише послідовність
// TMDB-воркери → класифікація → batch commit → AI-черга (NO_MATCH) →
// TMDB-верифікація AI-гіпотез → commit → черга локалізації.

// automaticRecognitionResult — підсумок автоматичного розпізнавання одного скану.
// Лічильники мають семантику scan_completed: позитивний AI-результат
// враховується лише після успішного commit відповідної пачки.
type automaticRecognitionResult struct {
	tmdbAccepted     int
	aiAccepted       int
	unresolved       int
	translationQueue []string
}

// recognizeAutomaticScan — єдина точка входу автоматичного розпізнавання файлів.
// Порядок етапів не змінюється: TMDB → processScanResults → commit TMDB-пачки →
// AI для NO_MATCH → верифікація та commit AI-пачок → черга локалізації.
func (a *App) recognizeAutomaticScan(ctx context.Context, paths []string) automaticRecognitionResult {
	var result automaticRecognitionResult

	resultsChan := a.runTMDBScan(ctx, paths)

	// 🛡️ Єдиний Writer для SQLite: збирає батч і пише транзакцією
	moviesToSave, geminiQueue, translationQueue := a.processScanResults(ctx, resultsChan)
	// Зберігаємо всіх знайдених одним запитом
	if len(moviesToSave) > 0 {
		if err := a.saveRecognitionBatch(ctx, moviesToSave, "tmdb_scan"); err != nil {
			utils.LoggerWithTrace(ctx).Error("batch_save_failed", slog.Any("error", err))
			translationQueue = nil
		} else {
			for _, movie := range moviesToSave {
				if movie.TmdbID > 0 {
					result.tmdbAccepted++
				} else {
					result.unresolved++
				}
			}
		}
	}

	// Спроба 2: Gemini для тих що TMDB не знайшов
	if ctx.Err() == nil && len(geminiQueue) > 0 {
		a.logFront(fmt.Sprintf("🤖 Черга Gemini: %d файлів", len(geminiQueue)))

		recognizedByGemini := a.processGeminiQueueWithRetry(ctx, geminiQueue, a.aiClient, true)
		result.aiAccepted = len(recognizedByGemini)
		result.unresolved += len(geminiQueue) - result.aiAccepted
		translationQueue = append(translationQueue, recognizedByGemini...)
	}

	result.translationQueue = translationQueue
	return result
}

type scanResult struct {
	path         string
	fname        string
	info         *tmdb.MovieInfo
	tmdbFailed   bool
	needsReview  bool
	reviewReason string
}

// aiConfidenceThreshold — єдиний поріг confidence для AI-гіпотези та її merge з TMDB.
const aiConfidenceThreshold = 0.55

// geminiTMDBVerifyMinJW — мінімальна схожість EN-назви Gemini і TMDB після верифікації.
const geminiTMDBVerifyMinJW = 0.85

var (
	groupedStrongEpisodeRE = regexp.MustCompile(`(?i)(?:^|[. _-])(?:s\d{1,2}e\d{1,3}|(?:season|episode|сезон|серія)\s*\d+)(?:$|[. _-])`)
	groupedWeakEpisodeRE   = regexp.MustCompile(`(?:^|[. _-])(\d{1,3})(?:[. _-]|$)`)
	groupedSpaceRE         = regexp.MustCompile(`\s+`)
)

const groupedTVReviewReason = "grouped_tv_type_conflict"

type groupedTVDecision struct {
	useParentTitle bool
	reason         string
}

func (a *App) runTMDBScan(ctx context.Context, paths []string) <-chan scanResult {
	totalFiles := len(paths)
	resultsChan := make(chan scanResult, len(paths))
	sem := make(chan struct{}, 10) // Ліміт у 10 одночасних запитів до TMDB (rate-limit safe)
	var wg sync.WaitGroup
	var processedCount int32
	groupTV := detectGroupedTV(ctx, paths)

	for _, path := range paths {
		if ctx.Err() != nil {
			a.logFront("🛑 Процес сканування перервано.")
			break
		}

		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			continue
		}

		go func() {
			defer wg.Done()
			defer func() { <-sem }() // Звільняємо слот
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic_in_goroutine",
						slog.Any("panic", r),
						slog.String("stack", string(debug.Stack())),
					)
				}
			}()

			if ctx.Err() != nil {
				return
			}

			fname := a.getFileIdentifier(path)

			// 🟢 СТВОРЮЄМО УНІКАЛЬНИЙ TRACE_ID ДЛЯ ЦЬОГО ФАЙЛУ
			fileTraceID := uuid.New().String()[:8]
			fileCtx := utils.ContextWithTrace(ctx, fileTraceID)
			logger := utils.LoggerWithTrace(fileCtx)

			// Атомарно збільшуємо лічильник для UI
			current := atomic.AddInt32(&processedCount, 1)
			a.emitProgress(int(current), totalFiles, "🔍 TMDB: "+fname)

			logger.Info("start_processing",
				slog.String("file", fname),
				slog.String("stage", "init"),
			)

			parsed := tmdb.ParseFilename(path)
			originalMediaType := parsed.MediaType
			groupDecision, grouped := groupTV[path]
			if grouped {
				parsed.MediaType = tmdb.MediaTypeTV
				if parent := filepath.Base(filepath.Dir(path)); groupDecision.useParentTitle && isMeaningfulSeriesParent(parent) {
					parentParsed := tmdb.ParseFilename(parent)
					if parentParsed.CleanTitle != "" {
						parsed.CleanTitle = parentParsed.CleanTitle
					}
				}
				logger.Info("grouped_tv_applied", slog.String("reason", groupDecision.reason), slog.String("title", parsed.CleanTitle))
			}
			info, err := a.tmdbClient.FetchFromParsed(fileCtx, parsed, path)
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			if err != nil {
				logger.Warn("tmdb_search_error",
					slog.String("file", fname),
					slog.Any("error", err),
				)
				a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", fname, err))
			}

			result := scanResult{path: path, fname: fname, info: info, tmdbFailed: err != nil}
			if grouped && originalMediaType == tmdb.MediaTypeMovie {
				result.needsReview = true
				result.reviewReason = groupedTVReviewReason
			}

			select {
			case resultsChan <- result:
			case <-ctx.Done():
			}
		}()
	}

	// Закриваємо канал у фоні, коли всі воркери відпрацюють
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	return resultsChan
}

func detectGroupedTV(ctx context.Context, paths []string) map[string]groupedTVDecision {
	type group struct {
		episodes map[int][]string
	}
	groups := make(map[string]*group)
	result := make(map[string]groupedTVDecision)
	for _, path := range paths {
		if ctx.Err() != nil {
			return map[string]groupedTVDecision{}
		}
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if groupedStrongEpisodeRE.MatchString(base) {
			result[path] = groupedTVDecision{useParentTitle: true, reason: "strong_episode_marker"}
			continue
		}
		matches := groupedWeakEpisodeRE.FindAllStringSubmatchIndex(base, -1)
		for _, match := range matches {
			if len(match) < 4 || match[2] < 0 {
				continue
			}
			n, _ := strconv.Atoi(base[match[2]:match[3]])
			if !isPlausibleWeakEpisode(base, match[2], match[3], n) {
				continue
			}
			signature := weakEpisodeSignature(base, match[2], match[3])
			key := filepath.Clean(filepath.Dir(path)) + "\x00" + signature
			g := groups[key]
			if g == nil {
				g = &group{episodes: make(map[int][]string)}
				groups[key] = g
			}
			g.episodes[n] = append(g.episodes[n], path)
			break
		}
	}
	for _, g := range groups {
		for n, currentPaths := range g.episodes {
			nextPaths, adjacent := g.episodes[n+1]
			if !adjacent {
				continue
			}
			for _, path := range append(currentPaths, nextPaths...) {
				result[path] = groupedTVDecision{useParentTitle: true, reason: "adjacent_episode_numbers"}
			}
		}
	}
	return result
}

func isPlausibleWeakEpisode(base string, start, end, number int) bool {
	if number <= 0 || number >= 1000 || number == 264 || number == 265 || number == 720 {
		return false
	}
	lower := strings.ToLower(base)
	prefix := strings.TrimRight(lower[:start], ". _-")
	lastToken := prefix
	if i := strings.LastIndexAny(prefix, ". _-"); i >= 0 {
		lastToken = prefix[i+1:]
	}
	if lastToken == "part" || lastToken == "pt" || lastToken == "частина" || lastToken == "часть" || lastToken == "x" || lastToken == "h" {
		return false
	}
	// Decimal audio layouts such as 5.1 and 7.1 are technical metadata. Do not
	// reject an episode followed by a multi-digit year or resolution token.
	if number < 10 && start > 0 && end+1 < len(base) && base[start-1] == '.' && base[end] == '.' && base[end+1] >= '0' && base[end+1] <= '9' {
		nextEnd := end + 1
		for nextEnd < len(base) && base[nextEnd] >= '0' && base[nextEnd] <= '9' {
			nextEnd++
		}
		if nextEnd == end+2 {
			return false
		}
	}
	return true
}

func weakEpisodeSignature(base string, start, end int) string {
	signature := strings.ToLower(base[:start] + " " + base[end:])
	signature = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(signature)
	return strings.TrimSpace(groupedSpaceRE.ReplaceAllString(signature, " "))
}

func isMeaningfulSeriesParent(parent string) bool {
	parent = strings.ToLower(strings.TrimSpace(parent))
	switch parent {
	case "", ".", "films", "film", "movies", "movie", "video", "videos", "media", "фільми", "фільм", "фильмы", "фильм", "відео", "видео":
		return false
	default:
		return true
	}
}

func (a *App) processScanResults(ctx context.Context, results <-chan scanResult) (toSave []storage.Movie, geminiQueue []string, translationQueue []string) {
	for res := range results {
		if ctx.Err() != nil {
			continue
		}
		if res.tmdbFailed {
			// The merge upsert protects confirmed rows, including concurrent updates.
			toSave = append(toSave, buildUnresolvedMovie(res.fname, res.path))
			continue
		}
		if res.info != nil && res.info.TMDBID > 0 {
			movie := movieFromTMDB(res.fname, res.info)
			if res.needsReview {
				movie.NeedsReview = true
				movie.ReviewReason = res.reviewReason
			}
			toSave = append(toSave, movie)
			a.logFront(fmt.Sprintf("✅ TMDB: '%s' → '%s'", res.fname, res.info.TitleUA))
			if a.movieInfoNeedsTranslation(res.info) {
				translationQueue = append(translationQueue, res.fname)
			}
		} else {
			// Чистий промах TMDB (NO_MATCH): файл іде до чинної AI-черги.
			// Історичний кеш ai_resolutions не читається — нова AI-гіпотеза
			// перевіряється через TMDB тим самим механізмом, що й будь-яка інша.
			if ctx.Err() == nil {
				geminiQueue = append(geminiQueue, res.path)
			}
		}
	}

	return toSave, geminiQueue, translationQueue
}

// buildUnresolvedMovie creates a placeholder record for files Gemini/TMDB could not verify.
func buildUnresolvedMovie(fname, path string) storage.Movie {
	parsed := tmdb.ParseFilename(path)
	yearStr := ""
	if parsed.Year > 0 {
		yearStr = strconv.Itoa(parsed.Year)
	}
	return storage.Movie{
		Filename:     fname,
		TitleEN:      "Unresolved: " + fname,
		TitleUA:      fname,
		Year:         yearStr,
		TmdbID:       0,
		NeedsReview:  true,
		ReviewReason: "unresolved",
	}
}

// appendUnresolvedFromMap adds a placeholder unless the file is already recognized (TmdbID > 0) using pre-loaded map.
func (a *App) appendUnresolvedFromMap(ctx context.Context, movies *[]storage.Movie, path string, existing map[string]storage.Movie) {
	fname := a.getFileIdentifier(path)
	movie, exists := existing[fname]
	if exists && movie.TmdbID > 0 {
		utils.LoggerWithTrace(ctx).Warn("skip_unresolved_downgrade",
			slog.String("file", fname), slog.Int("existing_tmdb_id", movie.TmdbID))
		return
	}
	*movies = append(*movies, buildUnresolvedMovie(fname, path))
}

// processGeminiQueue — Gemini розпізнає назви → TMDB верифікує → мерж → збереження.
// paths — повні шляхи до файлів (для парсера та збагаченого промпту).
func (a *App) processGeminiQueue(ctx context.Context, paths []string, aiClient *ai.Client) []string {
	return a.processGeminiQueueWithRetry(ctx, paths, aiClient, false)
}

func (a *App) processGeminiQueueWithRetry(ctx context.Context, paths []string, aiClient *ai.Client, semanticRetry bool) []string {
	var recognizedFiles []string

	const batchSize = 10
	total := len(paths)
	totalBatches := (total + batchSize - 1) / batchSize
	processed := 0

	for i := 0; i < total; i += batchSize {
		if ctx.Err() != nil {
			a.logFront("🛑 Gemini черга перервана користувачем.")
			a.emitProgress(total, total, "🛑 Зупинено")
			break
		}

		end := i + batchSize
		if end > total {
			end = total
		}
		batch := paths[i:end]
		currentBatchIdx := i/batchSize + 1

		a.logFront(fmt.Sprintf("📦 Gemini пачка %d/%d (%d файлів)", currentBatchIdx, totalBatches, len(batch)))

		// Pre-fetch all existing movies in this batch from DB to eliminate O(N) queries
		batchFnames := make([]string, len(batch))
		for j, p := range batch {
			batchFnames[j] = a.getFileIdentifier(p)
		}
		existingMovies, lookupErr := a.db.GetMoviesByFilenames(ctx, batchFnames)
		if lookupErr != nil {
			utils.LoggerWithTrace(ctx).Warn("batch_lookup_failed", slog.Any("error", lookupErr))
		}

		contexts := make([]ai.FileRecognitionContext, len(batch))
		for j, path := range batch {
			ctxObj := ai.FileRecognitionContextFromPath(path)
			ctxObj.ID = j
			ctxObj.RequestID = fmt.Sprintf("recognition-%d", j)
			contexts[j] = ctxObj
		}

		if ctx.Err() != nil {
			break
		}
		results, err := aiClient.RecognizeBulk(ctx, contexts)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			break
		}
		if err != nil {
			a.logFront(fmt.Sprintf("⚠️ Gemini помилка пачки %d: %v", currentBatchIdx, err))
			utils.LoggerWithTrace(ctx).Warn("gemini_batch_failed", slog.Int("batch", currentBatchIdx), slog.Any("error", err))

			// Create placeholders for all files in this failed batch so they don't disappear.
			var failedBatchMovies []storage.Movie
			for _, path := range batch {
				a.appendUnresolvedFromMap(ctx, &failedBatchMovies, path, existingMovies)
			}
			if len(failedBatchMovies) > 0 {
				if errSave := a.db.SaveMoviesBatch(ctx, failedBatchMovies); errSave != nil {
					utils.LoggerWithTrace(ctx).Error("batch_save_failed", slog.Any("error", errSave))
				}
			}
			continue
		}

		verified := make([]storage.Movie, len(batch))
		var retryInputs []ai.FileRecognitionContext
		var retrySlots []int
		for j, path := range batch {
			if ctx.Err() != nil {
				return recognizedFiles
			}
			processed++
			fname := batchFnames[j]
			rec := results[j]
			if rec == nil || rec.ENTitle == "" {
				a.logFront(fmt.Sprintf("⚠️ Gemini не розпізнав: '%s'", fname))
				a.emitProgress(processed, total, "❓ Не розпізнано: "+fname)
			} else {
				a.emitProgress(processed, total, "🤖 Gemini: "+rec.ENTitle)
			}
			movie, rejection, err := a.verifyQueueHypothesis(ctx, path, rec, false)
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return recognizedFiles
			}
			verified[j] = movie
			if !semanticRetry || lookupErr != nil || err != nil || movie.TmdbID > 0 || existingMovies[fname].TmdbID > 0 {
				continue
			}

			input := contexts[j]
			input.PreviousRejection = rejection
			// Keep the basename and parsed file evidence, without sending directory names.
			input.ParentDir = ""
			if rec != nil {
				input.PreviousENTitle, input.PreviousModel = rec.ENTitle, rec.Model
			}
			retryInputs, retrySlots = append(retryInputs, input), append(retrySlots, j)
		}

		var retried map[string]bool
		if len(retryInputs) > 0 && ctx.Err() == nil {
			// A manual confirmation can arrive while the first pass is in flight.
			current, err := a.db.GetMoviesByFilenames(ctx, batchFnames)
			if err != nil {
				utils.LoggerWithTrace(ctx).Warn("semantic_retry_lookup_failed")
			} else {
				existingMovies = current
				n := 0
				for i, slot := range retrySlots {
					if current[batchFnames[slot]].TmdbID <= 0 {
						retryInputs[n], retrySlots[n] = retryInputs[i], slot
						n++
					}
				}
				retryInputs, retrySlots = retryInputs[:n], retrySlots[:n]

				retries, err := aiClient.RetryRecognition(ctx, retryInputs)
				if ctx.Err() != nil || errors.Is(err, context.Canceled) {
					return recognizedFiles
				}
				if err != nil {
					utils.LoggerWithTrace(ctx).Warn("ai_semantic_retry_failed")
				}
				for i, rec := range retries {
					if ctx.Err() != nil {
						return recognizedFiles
					}
					if rec == nil || rec.ENTitle == "" {
						continue
					}
					slot := retrySlots[i]
					movie, _, err := a.verifyQueueHypothesis(ctx, batch[slot], rec, true)
					if ctx.Err() != nil || errors.Is(err, context.Canceled) {
						return recognizedFiles
					}
					if err == nil && movie.TmdbID > 0 {
						if retried == nil {
							retried = make(map[string]bool)
						}
						verified[slot] = movie
						retried[movie.Filename] = true
					}
				}
			}
		}

		if ctx.Err() != nil {
			return recognizedFiles
		}
		var moviesToSave []storage.Movie
		for j, path := range batch {
			if verified[j].TmdbID > 0 {
				moviesToSave = append(moviesToSave, verified[j])
			} else {
				a.appendUnresolvedFromMap(ctx, &moviesToSave, path, existingMovies)
			}
		}

		saved, err := a.commitRecognitionBatch(ctx, moviesToSave, "gemini_queue", retried)
		if err != nil {
			utils.LoggerWithTrace(ctx).Error("batch_save_failed", slog.Any("error", err))
			continue
		}
		for _, movie := range saved {
			if movie.TmdbID > 0 {
				recognizedFiles = append(recognizedFiles, movie.Filename)
			}
		}
	}

	if ctx.Err() == nil {
		a.logFront("✅ Gemini черга оброблена!")
	}
	return recognizedFiles
}

func (a *App) verifyQueueHypothesis(ctx context.Context, path string, rec *ai.RecognizedTitle, semanticRetry bool) (storage.Movie, string, error) {
	fname := a.getFileIdentifier(path)
	logger := utils.LoggerWithTrace(ctx).With(slog.String("file", fname))
	if rec == nil {
		logger.Warn("gemini_recognition_missing")
		return storage.Movie{}, "missing_or_conflicting_response", nil
	}

	var year int
	if rec.Year != nil {
		year = *rec.Year
	}
	logger.Info("gemini_recognition_result",
		slog.String("en_title", rec.ENTitle), slog.String("media_type", rec.MediaType),
		slog.Int("year", year), slog.Float64("confidence", rec.Confidence),
		slog.String("provider", rec.Provider), slog.String("model", rec.Model))
	logger.Debug("ai_recognition_context",
		slog.String("original_title", rec.OriginalTitle), slog.String("status", rec.Status))
	if !semanticRetry && rec.ENTitle == "" {
		logger.Warn("gemini_recognition_empty_title",
			slog.String("media_type", rec.MediaType), slog.Int("year", year), slog.Float64("confidence", rec.Confidence))
		movie, err := a.rescueEmptyGeminiWithFolder(ctx, path)
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			logger.Warn("gemini_queue_rescue_tmdb_error", slog.Any("error", err))
			a.logFront(fmt.Sprintf("⚠️ TMDB помилка (rescue) для '%s': %v", fname, err))
		}
		return movie, "missing_english_title", err
	}
	movie, rejection, err := a.verifyAIHypothesis(ctx, path, *rec, false, semanticRetry)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		logger.Warn("gemini_queue_merge_tmdb_error", slog.String("en_title", rec.ENTitle), slog.Any("error", err))
		a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", fname, err))
	}
	return movie, rejection, err
}

// mergeGeminiWithTMDB — шукає фільм в TMDB за EN назвою від Gemini,
// мержить результати: TMDB має пріоритет, Gemini заповнює прогалини.
// filePath — повний шлях або basename (для ParseFilename).
func (a *App) mergeGeminiWithTMDB(ctx context.Context, filePath string, rec ai.RecognizedTitle) (storage.Movie, error) {
	return a.mergeGeminiWithTMDBForType(ctx, filePath, rec, false)
}

// mergeGeminiWithTMDBForType — явний результат AI→TMDB-верифікації (R9-B):
//
//	movie.TmdbID > 0, err == nil     → FOUND: TMDB підтвердив identity;
//	movie.TmdbID == 0, err == nil    → UNVERIFIED: гіпотеза непридатна/відхилена
//	                                   або TMDB не знайшов прийнятного збігу;
//	err != nil (не context.Canceled) → TMDB_ERROR: істотний збій API, мережі,
//	                                   JSON або необхідної перевірки TMDB;
//	errors.Is(err, context.Canceled) → CANCELED: операцію скасовано (ctx.Err()).
//
// Підтверджений збіг має пріоритет над помилками раніше дозволених спроб.
func (a *App) mergeGeminiWithTMDBForType(ctx context.Context, filePath string, rec ai.RecognizedTitle, strictMediaType bool) (storage.Movie, error) {
	movie, _, err := a.verifyAIHypothesis(ctx, filePath, rec, strictMediaType, false)
	return movie, err
}

func (a *App) verifyAIHypothesis(ctx context.Context, filePath string, rec ai.RecognizedTitle, strictMediaType, semanticRetry bool) (storage.Movie, string, error) {
	fname := a.getFileIdentifier(filePath)
	logger := utils.LoggerWithTrace(ctx).With(
		slog.String("file", fname),
		slog.String("en_title", rec.ENTitle),
		slog.String("requested_media_type", rec.MediaType),
		slog.Float64("confidence", rec.Confidence),
	)

	// 🛡️ КРОК 1: ПЕРЕВІРКА ВАЛІДНОСТІ ВІДПОВІДІ ШІ
	// Непридатна гіпотеза — це UNVERIFIED, а не помилка TMDB: жодного запиту yet.
	if rec.ENTitle == "" || (rec.Status != "" && rec.Status != "resolved") {
		logger.Warn("gemini_merge_skipped_empty_title")
		a.logFront(fmt.Sprintf("⚠️ [GEMINI] Відсутня EN назва для '%s'. Пропускаємо пошук.", fname))
		return storage.Movie{Filename: fname}, "unacceptable_title_or_status", nil
	}

	if rec.Confidence < aiConfidenceThreshold {
		logger.Warn("gemini_merge_skipped_low_confidence")
		a.logFront(fmt.Sprintf("🛡️ [ЗАХИСТ] Gemini невпевнений (%.2f) щодо '%s'. Відхиляємо.", rec.Confidence, fname))
		return storage.Movie{Filename: fname}, "low_confidence", nil
	}

	// 🛡️ КРОК 4: КОНТРОЛЬ РОКУ
	parsed := tmdb.ParseFilename(filePath)
	if parsed.Year > 0 {
		if rec.Year != nil {
			diff := *rec.Year - parsed.Year
			// Допускаємо похибку ±1 рік. Якщо більше — логуємо як потенційну галюцинацію,
			// але даємо шанс TMDB верифікувати цей рік.
			if diff < -1 || diff > 1 {
				a.logFront(fmt.Sprintf("⚠️ [ПОПЕРЕДЖЕННЯ] Gemini вказав рік %d для '%s' (у файлі %d). Довіряємо Gemini, але TMDB має це перевірити.", *rec.Year, fname, parsed.Year))
			}
		} else {
			// Якщо ШІ взагалі не дав року, страхуємо його
			rec.Year = &parsed.Year
		}
	}

	// Базова заготовка з даними Gemini: тільки ідентифікатори для пошуку.
	movie := storage.Movie{
		Filename: fname,
		TitleEN:  rec.ENTitle,
	}
	if rec.Year != nil {
		movie.Year = strconv.Itoa(*rec.Year)
	}

	// Визначаємо MediaType для TMDB пошуку
	mt := tmdb.MediaTypeMovie
	if rec.MediaType == "tv" {
		mt = tmdb.MediaTypeTV
	}

	// Пошук в TMDB за EN назвою від Gemini + ПЕРЕВІРЕНИМ РОКОМ
	yearStr := ""
	if rec.Year != nil {
		yearStr = strconv.Itoa(*rec.Year)
	}

	logger = logger.With(
		slog.String("preferred_media_type", string(mt)),
		slog.String("year", yearStr),
	)
	logger.Info("gemini_merge_started")

	if ctx.Err() != nil {
		logger.Debug("gemini_merge_cancelled_before_tmdb", slog.Any("error", ctx.Err()))
		return storage.Movie{Filename: fname}, "", ctx.Err()
	}

	targetYear := 0
	if rec.Year != nil {
		targetYear = *rec.Year
	}
	tmdbInfo, err := a.tmdbClient.SearchExactTitle(ctx, rec.ENTitle, targetYear, mt, strictMediaType, filePath)
	if err == nil && tmdbInfo != nil {
		logger.Info("gemini_merge_exact_candidate_selected",
			slog.Int("tmdb_id", tmdbInfo.TMDBID),
			slog.String("selected_media_type", string(tmdbInfo.MediaType)),
			slog.String("tmdb_title", tmdbInfo.TitleEN),
			slog.String("matched_alias", tmdbInfo.MatchedAlias),
		)
	}
	if tmdbInfo == nil && err == nil && !strictMediaType {
		tmdbInfo, err = a.tmdbClient.FetchByCleanTitle(ctx, rec.ENTitle, yearStr, mt)
		if tmdbInfo == nil && mt == tmdb.MediaTypeMovie && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			logger.Info("gemini_merge_tv_retry")
			tvInfo, tvErr := a.tmdbClient.FetchByCleanTitle(ctx, rec.ENTitle, yearStr, tmdb.MediaTypeTV)
			if tvInfo != nil {
				tmdbInfo, err = tvInfo, nil
			} else {
				err = cmp.Or(tvErr, err)
			}
		}
	}

	// CANCELED має пріоритет над рештою: операцію скасовано, причина — ctx.Err().
	if ctx.Err() != nil {
		logger.Debug("gemini_merge_cancelled_after_tmdb", slog.Any("error", ctx.Err()))
		return storage.Movie{Filename: fname}, "", ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		logger.Debug("gemini_merge_cancelled_after_tmdb", slog.Any("error", err))
		return storage.Movie{Filename: fname}, "", err
	}
	// Помилка TMDB не перетворюється на чистий промах: повертаємо її як TMDB_ERROR.
	if err != nil {
		logger.Warn("gemini_merge_tmdb_error", slog.Any("error", err))
		a.logFront(fmt.Sprintf("⚠️ TMDB помилка для '%s': %v", rec.ENTitle, err))
		return storage.Movie{Filename: fname}, "", err
	}
	// Чистий промах TMDB (успішні запити, прийнятного збігу немає) — UNVERIFIED.
	if tmdbInfo == nil {
		logger.Warn("gemini_merge_tmdb_not_found")
		a.logFront(fmt.Sprintf("❌ TMDB не знайшов '%s' — запис залишається нерозпізнаним", rec.ENTitle))
		return storage.Movie{Filename: fname}, "tmdb_no_match", nil
	}

	strongJW := tmdb.TitleSimilarity(rec.ENTitle, tmdbInfo.TitleEN)
	if tmdbInfo.MatchedAlias != "" {
		jwAlias := tmdb.TitleSimilarity(rec.ENTitle, tmdbInfo.MatchedAlias)
		if jwAlias > strongJW {
			strongJW = jwAlias
		}
	}
	localizedJW := maxLocalizedTitleSimilarity(rec.ENTitle, tmdbInfo)
	yearCompatible := geminiTMDBYearCompatible(parsed.Year, rec.Year, tmdbInfo.Year)
	typeCompatible := tmdbInfo.MediaType == mt || strongJW >= 0.99

	if strongJW < geminiTMDBVerifyMinJW || !yearCompatible || !typeCompatible {
		logger.Warn("gemini_merge_post_verify_rejected",
			slog.String("tmdb_title", tmdbInfo.TitleEN),
			slog.String("search_title", tmdbInfo.SearchTitle),
			slog.String("matched_alias", tmdbInfo.MatchedAlias),
			slog.Float64("strong_similarity", strongJW),
			slog.Float64("localized_similarity", localizedJW),
			slog.Bool("year_compatible", yearCompatible),
			slog.Bool("media_type_compatible", typeCompatible),
		)
		a.logFront(fmt.Sprintf(
			"🛡️ [POST-VERIFY] Відхилено '%s': Gemini '%s' ≠ TMDB '%s' (Search: '%s', Alias: '%s', strong %.2f, localized %.2f)",
			fname, rec.ENTitle, tmdbInfo.TitleEN, tmdbInfo.SearchTitle, tmdbInfo.MatchedAlias, strongJW, localizedJW,
		))
		// Відхилена гіпотеза — UNVERIFIED, без помилки TMDB.
		var reasons []string
		if !yearCompatible {
			reasons = append(reasons, "year_mismatch")
		}
		if !typeCompatible {
			reasons = append(reasons, "media_type_mismatch")
		}
		if strongJW < geminiTMDBVerifyMinJW {
			reasons = append(reasons, "title_similarity_below_threshold")
		}
		return storage.Movie{Filename: fname}, strings.Join(reasons, ", "), nil
	}

	if semanticRetry {
		// An exact lookup of a plausible AI title proves that the work exists.
		// Require TMDB title evidence that also links the work to this file.
		fileSimilarity := max(
			tmdb.TitleSimilarity(parsed.CleanTitle, tmdbInfo.TitleEN),
			tmdb.TitleSimilarity(parsed.CleanTitle, tmdbInfo.TitleUA),
			tmdb.TitleSimilarity(parsed.CleanTitle, tmdbInfo.MatchedAlias),
		)
		if fileSimilarity < geminiTMDBVerifyMinJW {
			logger.Info("ai_semantic_retry_filename_unverified", slog.Float64("similarity", fileSimilarity))
			return storage.Movie{Filename: fname}, "filename_title_unverified", nil
		}
	}

	logger.Info("gemini_merge_tmdb_accepted",
		slog.Int("tmdb_id", tmdbInfo.TMDBID),
		slog.String("tmdb_title", tmdbInfo.TitleEN),
		slog.String("selected_media_type", string(tmdbInfo.MediaType)),
		slog.String("tmdb_year", tmdbInfo.Year),
	)
	a.logFront(fmt.Sprintf("✅ TMDB знайшов: '%s' (%s)", tmdbInfo.TitleEN, tmdbInfo.Year))

	// TMDB є джерелом повних даних після Gemini Resolve.
	movie.TmdbID = tmdbInfo.TMDBID
	movie.TitleEN = tmdbInfo.TitleEN
	movie.Year = tmdbInfo.Year
	movie.PosterURL = tmdbInfo.PosterURL
	movie.LocalPosterPath = tmdbInfo.LocalPosterPath
	movie.TitleUA = tmdbInfo.TitleUA
	movie.Plot = tmdbInfo.Plot
	movie.Genres = tmdbInfo.Genres
	movie.Cast = tmdbInfo.Cast
	movie.MediaType = string(tmdbInfo.MediaType)
	movie.VoteAverage = tmdbInfo.VoteAverage
	movie.VoteCount = tmdbInfo.VoteCount
	movie.RecognitionSource = rec.Provider
	if movie.RecognitionSource == "" {
		movie.RecognitionSource = "gemini"
	}
	movie.RecognitionConfidence = rec.Confidence
	movie.VerificationScore = strongJW
	if tmdbInfo.NeedsReview {
		movie.NeedsReview = true
		movie.ReviewReason = tmdbInfo.ReviewReason
	}
	if tmdbInfo.AmbiguousExact || strongJW < tmdb.ReviewVerificationThreshold {
		movie.NeedsReview = true
		if tmdbInfo.AmbiguousExact {
			movie.ReviewReason = "ambiguous_exact"
		} else {
			movie.ReviewReason = "low_verification_score"
		}
		logger.Info("recognition_needs_review", slog.String("reason", movie.ReviewReason))
	}

	return movie, "", nil
}

// rescueEmptyGeminiWithFolder tries a deterministic TMDB lookup when AI returned
// an empty title but the file sits in a meaningful release folder.
// Контракт результату — як у mergeGeminiWithTMDBForType (R9-B): FOUND /
// UNVERIFIED / TMDB_ERROR / CANCELED. Перша істотна помилка TMDB зберігається
// локально до завершення дозволених спроб, але пізніший підтверджений кандидат
// (FOUND) має над нею пріоритет; без кешу помилок.
func (a *App) rescueEmptyGeminiWithFolder(ctx context.Context, filePath string) (storage.Movie, error) {
	fname := a.getFileIdentifier(filePath)
	logger := utils.LoggerWithTrace(ctx).With(slog.String("file", fname))

	if ctx.Err() != nil {
		logger.Debug("gemini_empty_rescue_cancelled_before_start", slog.Any("error", ctx.Err()))
		return storage.Movie{Filename: fname}, ctx.Err()
	}

	parsed := tmdb.ParseFilename(filePath)
	parentTitle := cleanRescueParentTitle(filePath, a.cfg.RootForPath(filePath), parsed.ParentDir)
	if parentTitle == "" {
		logger.Warn("gemini_empty_rescue_skipped_no_parent_title")
		return storage.Movie{Filename: fname}, nil
	}

	parentParsed := tmdb.ParseFilename(parentTitle)
	year := parsed.Year
	if year == 0 {
		year = parentParsed.Year
	}
	yearStr := ""
	if year > 0 {
		yearStr = strconv.Itoa(year)
	}

	candidates := buildGeminiEmptyRescueCandidates(parentParsed.CleanTitle)
	if len(candidates) == 0 {
		logger.Warn("gemini_empty_rescue_skipped_no_candidates", slog.String("parent", parentTitle))
		return storage.Movie{Filename: fname}, nil
	}

	logger.Info("gemini_empty_rescue_started",
		slog.String("parent", parentTitle),
		slog.Int("candidate_count", len(candidates)),
		slog.String("year", yearStr),
	)

	// firstErr — перша істотна помилка TMDB серед дозволених спроб (без кешу):
	// вона повертається лише якщо жоден кандидат не підтвердився.
	var firstErr error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			logger.Debug("gemini_empty_rescue_cancelled", slog.Any("error", ctx.Err()))
			return storage.Movie{Filename: fname}, ctx.Err()
		}

		tmdbInfo, err := a.tmdbClient.FetchByCleanTitle(ctx, candidate, yearStr, tmdb.MediaTypeMovie)
		if err != nil {
			logger.Warn("gemini_empty_rescue_tmdb_error",
				slog.String("candidate", candidate),
				slog.Any("error", err),
			)
			firstErr = cmp.Or(firstErr, err)
			continue
		}
		if tmdbInfo == nil && ctx.Err() == nil {
			logger.Info("gemini_empty_rescue_tv_retry", slog.String("candidate", candidate))
			tmdbInfo, err = a.tmdbClient.FetchByCleanTitle(ctx, candidate, yearStr, tmdb.MediaTypeTV)
			if err != nil {
				logger.Warn("gemini_empty_rescue_tv_retry_error",
					slog.String("candidate", candidate),
					slog.Any("error", err),
				)
				firstErr = cmp.Or(firstErr, err)
				continue
			}
		}
		if tmdbInfo == nil {
			logger.Info("gemini_empty_rescue_candidate_miss", slog.String("candidate", candidate))
			continue
		}

		jw := maxTitleSimilarity(candidate, tmdbInfo)
		if jw < geminiTMDBVerifyMinJW || tmdbInfo.AmbiguousExact || tmdbInfo.TMDBID <= 0 || (tmdbInfo.MediaType != tmdb.MediaTypeMovie && tmdbInfo.MediaType != tmdb.MediaTypeTV) {
			logger.Warn("gemini_empty_rescue_post_verify_rejected",
				slog.Int("tmdb_id", tmdbInfo.TMDBID),
				slog.String("tmdb_media_type", string(tmdbInfo.MediaType)),
				slog.Bool("ambiguous", tmdbInfo.AmbiguousExact),
				slog.String("candidate", candidate),
				slog.String("tmdb_title", tmdbInfo.TitleEN),
				slog.String("search_title", tmdbInfo.SearchTitle),
				slog.String("matched_alias", tmdbInfo.MatchedAlias),
				slog.Float64("similarity", jw),
			)
			continue
		}

		logger.Info("gemini_empty_rescue_accepted",
			slog.String("candidate", candidate),
			slog.Int("tmdb_id", tmdbInfo.TMDBID),
			slog.String("tmdb_title", tmdbInfo.TitleEN),
			slog.String("tmdb_media_type", string(tmdbInfo.MediaType)),
			slog.String("tmdb_year", tmdbInfo.Year),
			slog.Float64("similarity", jw),
		)
		a.logFront(fmt.Sprintf("✅ [RESCUE] TMDB знайшов: '%s' (%s)", tmdbInfo.TitleEN, tmdbInfo.Year))
		// Підтверджений кандидат — FOUND, пріоритет над раніше збереженою помилкою.
		return movieFromTMDB(fname, tmdbInfo), nil
	}

	logger.Warn("gemini_empty_rescue_failed", slog.String("parent", parentTitle))
	if ctx.Err() != nil {
		return storage.Movie{Filename: fname}, ctx.Err()
	}
	if firstErr != nil {
		// Прийнятного збігу немає, але була істотна помилка TMDB — TMDB_ERROR.
		return storage.Movie{Filename: fname}, firstErr
	}
	// Усі необхідні запити успішні, збігу немає — UNVERIFIED.
	return storage.Movie{Filename: fname}, nil
}

func cleanRescueParentTitle(filePath, mediaRoot, parentDir string) string {
	parent := strings.TrimSpace(parentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return ""
	}

	if mediaRoot != "" {
		fileAbs := filepath.Clean(filePath)
		rootAbs := filepath.Clean(mediaRoot)
		if !filepath.IsAbs(fileAbs) {
			fileAbs = filepath.Join(rootAbs, fileAbs)
		}
		parentPath := filepath.Clean(filepath.Dir(fileAbs))
		if rel, err := filepath.Rel(rootAbs, parentPath); err == nil && rel == "." {
			return ""
		}
	}

	switch strings.ToLower(parent) {
	case "movie", "movies", "film", "films", "serial", "serials", "series", "show", "shows",
		"video", "videos", "download", "downloads", "кино", "фильмы", "фільми", "серіали", "сериалы":
		return ""
	}

	return parent
}

func buildGeminiEmptyRescueCandidates(title string) []string {
	var candidates []string
	seen := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len([]rune(s)) < 3 {
			return
		}
		key := strings.ToLower(s)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, s)
	}

	add(title)
	latin := utils.CyrillicToLatin(title)
	add(latin)
	for _, variant := range borrowedLatinVariants(latin) {
		add(variant)
	}

	return candidates
}

func borrowedLatinVariants(s string) []string {
	if s == "" || utils.HasCyrillic(s) {
		return nil
	}

	var variants []string
	add := func(old, replacement string) {
		if strings.HasPrefix(s, old) {
			variants = append(variants, replacement+strings.TrimPrefix(s, old))
		}
	}

	add("Sk", "Sc")
	add("sk", "sc")
	add("SK", "SC")

	return variants
}

func maxTitleSimilarity(title string, info *tmdb.MovieInfo) float64 {
	jw := tmdb.TitleSimilarity(title, info.TitleEN)
	// TitleUA is the Cyrillic counterpart; JaroWinkler across alphabets is 0,
	// so a Cyrillic candidate needs this check to avoid false rejection.
	if info.TitleUA != "" {
		if jwUA := tmdb.TitleSimilarity(title, info.TitleUA); jwUA > jw {
			jw = jwUA
		}
	}
	if info.SearchTitle != "" {
		if jwSearch := tmdb.TitleSimilarity(title, info.SearchTitle); jwSearch > jw {
			jw = jwSearch
		}
	}
	if info.MatchedAlias != "" {
		if jwAlias := tmdb.TitleSimilarity(title, info.MatchedAlias); jwAlias > jw {
			jw = jwAlias
		}
	}
	return jw
}

func maxLocalizedTitleSimilarity(title string, info *tmdb.MovieInfo) float64 {
	if info == nil {
		return 0
	}
	jw := tmdb.TitleSimilarity(title, info.TitleUA)
	if searchJW := tmdb.TitleSimilarity(title, info.SearchTitle); searchJW > jw {
		jw = searchJW
	}
	return jw
}

func geminiTMDBYearCompatible(parsedYear int, recognizedYear *int, tmdbYear string) bool {
	foundYear, err := strconv.Atoi(tmdbYear)
	if err != nil || foundYear == 0 {
		return parsedYear == 0 && recognizedYear == nil
	}
	if parsedYear > 0 && absInt(parsedYear-foundYear) > 1 {
		return false
	}
	return recognizedYear == nil || *recognizedYear == 0 || absInt(*recognizedYear-foundYear) <= 1
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// movieFromTMDB — створює storage.Movie з результату TMDB (без Gemini)
func movieFromTMDB(fname string, info *tmdb.MovieInfo) storage.Movie {
	movie := storage.Movie{
		Filename:              fname,
		TmdbID:                info.TMDBID,
		TitleUA:               info.TitleUA,
		TitleEN:               info.TitleEN,
		Year:                  info.Year,
		Plot:                  info.Plot,
		Genres:                info.Genres,
		Cast:                  info.Cast,
		PosterURL:             info.PosterURL,
		LocalPosterPath:       info.LocalPosterPath,
		MediaType:             string(info.MediaType),
		RecognitionSource:     "tmdb",
		RecognitionConfidence: 1,
		VerificationScore:     info.VerificationScore,
		VoteAverage:           info.VoteAverage,
		VoteCount:             info.VoteCount,
	}
	if movie.VerificationScore == 0 {
		movie.VerificationScore = 1
	}
	if info.NeedsReview {
		movie.NeedsReview = true
		movie.ReviewReason = info.ReviewReason
	}
	if info.AmbiguousExact {
		movie.NeedsReview = true
		movie.ReviewReason = "ambiguous_exact"
	}
	return movie
}

func (a *App) movieInfoNeedsTranslation(info *tmdb.MovieInfo) bool {
	if info == nil {
		return false
	}
	needTitle := info.TitleUA == "" || needsTranslation(info.TitleUA)
	needPlot := info.Plot == "" || needsTranslation(info.Plot)
	return needTitle || needPlot
}
