package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// R8: singleflight для деталей видалено. Кожен GetDetails — незалежний
// HTTP-запит із власним context: результат не кешується, не об'єднується з
// одночасними запитами й не успадковує скасування чужого context.
// Ці тести свідомо використовують лише публічний API клієнта та спільні
// фікстури, тому компілюються й на R7 (де частина з них падає — це і є
// регресія колишнього дефекту shared-ctx).

// R8: повторне незалежне звернення завжди отримує свіжу відповідь TMDB,
// навіть якщо та сама identity вже читалася раніше.
func TestR8FreshDetailsAfterTMDBChange(t *testing.T) {
	version := 0
	c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/3/movie/42" {
			version++
			return r2Response(200, r7MovieDetailsBody(fmt.Sprintf("Фільм версія %d", version)))
		}
		return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
	})
	ctx := context.Background()
	first, err := c.GetDetails(ctx, MediaTypeMovie, 42, "")
	if err != nil || first == nil || first.TMDBID != 42 || first.MediaType != MediaTypeMovie {
		t.Fatalf("first: %+v, %v", first, err)
	}
	second, err := c.GetDetails(ctx, MediaTypeMovie, 42, "")
	if err != nil || second == nil || second.TMDBID != 42 {
		t.Fatalf("second: %+v, %v", second, err)
	}
	if second.TitleUA == first.TitleUA {
		t.Errorf("second read returned stale title %q — details were cached", second.TitleUA)
	}
	if len(*calls) != 2 {
		t.Errorf("HTTP calls=%d, want 2 (no details cache): %q", len(*calls), *calls)
	}
	t.Logf("R8 fresh details: titleA=%q titleB=%q calls=%q", first.TitleUA, second.TitleUA, *calls)
}

func TestR8PreviewFreshAfterTMDBChange(t *testing.T) {
	version := 0
	c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/3/movie/42" {
			version++
			return r2Response(200, fmt.Sprintf(`{"id":42,"title":"Превʼю %d","release_date":"2024-01-01","vote_count":%d}`, version, version))
		}
		return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
	})
	ctx := context.Background()
	first, err := c.GetCandidateDetails(ctx, MediaTypeMovie, 42)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.GetCandidateDetails(ctx, MediaTypeMovie, 42)
	if err != nil {
		t.Fatal(err)
	}
	if second.Title == first.Title || second.VoteCount == first.VoteCount {
		t.Errorf("preview repeat returned stale data: %+v vs %+v", first, second)
	}
	if len(*calls) != 2 {
		t.Errorf("HTTP calls=%d, want 2 (no candidate details cache): %q", len(*calls), *calls)
	}
}

// 20 одночасних читачів тієї самої identity: без singleflight кожен виконує
// власний waterfall (20 HTTP) і отримує правильну typed identity; наступний
// виклик після завершення — знову свіжий HTTP. На R7 цей тест падає: група
// об'єднувала читачів в один запит (calls=1).
func TestR8ConcurrentReadersFetchIndependently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		c, wire := r2Fixture(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-release // тримаємо запити в польоті, поки всі читачі стартували
			return r2Response(200, r7MovieDetailsBody("Фільм"))
		})
		const readers = 20
		var wg sync.WaitGroup
		type result struct {
			info *MovieInfo
			err  error
		}
		results := make(chan result, readers)
		for range readers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
				results <- result{info, err}
			}()
		}
		synctest.Wait() // усі читачі або вже у власному HTTP, або (на R7) у черзі групи
		close(release)
		wg.Wait()
		close(results)
		for r := range results {
			if r.err != nil || r.info == nil || r.info.TMDBID != 42 || r.info.MediaType != MediaTypeMovie {
				t.Errorf("reader: %+v, %v", r.info, r.err)
			}
		}
		if got := calls.Load(); got != readers {
			t.Fatalf("independent waterfall requests=%d, want %d (R7 singleflight merged them): %q", got, readers, *wire)
		}
		// Після завершення наступний виклик — свіжий HTTP (кешу немає).
		info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
		if err != nil || info == nil || info.TMDBID != 42 {
			t.Fatalf("read after readers: %+v, %v", info, err)
		}
		if got := calls.Load(); got != readers+1 {
			t.Fatalf("requests after extra read=%d, want %d: %q", got, readers+1, *wire)
		}
		t.Logf("R8 independent readers: calls=%q", *wire)
	})
}

// Головна регресія R8 (замість TestR7SingleflightFirstCallerCancelAffectsWaiters):
// перший caller розпочав HTTP і був скасований — другий caller із власним живим
// context має успішно завершитися. На R7 singleflight ділив один запуск із ctx
// першого caller, тому незалежний waiter теж отримував context.Canceled —
// цей тест там падає.
func TestR8FirstCallerCancelDoesNotAffectIndependentWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var requests atomic.Int32
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-release:
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			}
		})
		ctx1, cancel1 := context.WithCancel(t.Context())
		defer cancel1()
		ctx2, cancel2 := context.WithCancel(t.Context())
		defer cancel2()
		type result struct {
			info *MovieInfo
			err  error
		}
		done1 := make(chan result, 1)
		done2 := make(chan result, 1)
		go func() {
			info, err := c.GetDetails(ctx1, MediaTypeMovie, 42, "")
			done1 <- result{info, err}
		}()
		<-entered // перший запит уже у польоті — на R7 перший caller стає лідером групи
		go func() {
			info, err := c.GetDetails(ctx2, MediaTypeMovie, 42, "")
			done2 <- result{info, err}
		}()
		synctest.Wait() // на R8 другий читач уже у власному HTTP; на R7 — чекає результат групи
		cancel1()       // скасовуємо першого caller, який розпочав HTTP
		r1 := <-done1
		if !errors.Is(r1.err, context.Canceled) {
			t.Errorf("first caller error=%v, want context.Canceled", r1.err)
		}
		if r1.info != nil {
			t.Errorf("canceled first caller returned details: %+v", r1.info)
		}
		close(release) // відпускаємо решту запитів
		r2 := <-done2
		if r2.err != nil {
			t.Fatalf("independent second caller inherited first caller cancellation: %v (calls=%q)", r2.err, *calls)
		}
		if r2.info == nil || r2.info.TMDBID != 42 || r2.info.MediaType != MediaTypeMovie {
			t.Fatalf("second caller identity: %+v", r2.info)
		}
		if got := requests.Load(); got != 2 {
			t.Fatalf("requests=%d, want 2 independent waterfalls: %q", got, *calls)
		}
		// Наступний виклик із живим ctx — свіжий успішний HTTP.
		info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
		if err != nil || info == nil || info.TMDBID != 42 {
			t.Errorf("read after cancel: %+v, %v", info, err)
		}
		if got := requests.Load(); got != 3 {
			t.Errorf("requests after recovery=%d, want 3: %q", got, *calls)
		}
		t.Logf("R8 first-cancel independence: calls=%q", *calls)
	})
}

// Зворотний напрямок: другий caller скасовує власний context під час HTTP —
// отримує context.Canceled, а перший caller із живим context завершується
// успішно. Цей напрямок працював і в R7 (waiter обробляв власне скасування).
func TestR8SecondCallerCancelDoesNotAffectFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-release:
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			}
		})
		ctx1 := t.Context()
		ctx2, cancel2 := context.WithCancel(t.Context())
		defer cancel2()
		type result struct {
			info *MovieInfo
			err  error
		}
		done1 := make(chan result, 1)
		done2 := make(chan result, 1)
		go func() {
			info, err := c.GetDetails(ctx1, MediaTypeMovie, 42, "")
			done1 <- result{info, err}
		}()
		<-entered
		go func() {
			info, err := c.GetDetails(ctx2, MediaTypeMovie, 42, "")
			done2 <- result{info, err}
		}()
		synctest.Wait()
		cancel2()
		if r2 := <-done2; !errors.Is(r2.err, context.Canceled) {
			t.Errorf("second caller error=%v, want context.Canceled", r2.err)
		}
		close(release)
		r1 := <-done1
		if r1.err != nil || r1.info == nil || r1.info.TMDBID != 42 || r1.info.MediaType != MediaTypeMovie {
			t.Fatalf("first caller: %+v, %v", r1.info, r1.err)
		}
		t.Logf("R8 second-cancel independence: calls=%q", *calls)
	})
}

// Спільний parent context (scan STOP): скасування parent перериває обох
// callers і не запускає нові етапи waterfall чи постерів.
func TestR8SharedParentCancelStopsBothCallers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-release:
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			}
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 2)
		for range 2 {
			go func() {
				info, err := c.GetDetails(ctx, MediaTypeMovie, 42, "")
				if info != nil {
					err = fmt.Errorf("canceled call returned details: %+v", info)
				}
				done <- err
			}()
		}
		synctest.Wait() // усі запити в польоті
		cancel()        // STOP спільного parent
		for range 2 {
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("caller error=%v, want context.Canceled", err)
			}
		}
		close(release)
		// Після скасування не почалися нові етапи waterfall (лише uk-UA) і постерів.
		for _, call := range *calls {
			if !strings.Contains(call, "language=uk-UA") {
				t.Errorf("unexpected stage after STOP: %q", call)
			}
			if strings.HasPrefix(call, "/t/p/") {
				t.Errorf("poster requested after STOP: %q", call)
			}
		}
		if len(*calls) > 2 {
			t.Errorf("too many requests after STOP: %q", *calls)
		}
		t.Logf("R8 shared parent STOP: calls=%q", *calls)
	})
}

// STOP під час завантаження постера: деталі вже отримані (uk-UA, ранній вихід),
// запит постера перервано — повертається context.Canceled, тимчасових файлів
// не лишається, нових запитів немає.
func TestR8CancelDuringPosterDownload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		postersDir := t.TempDir()
		posterEntered := make(chan struct{}, 1)
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			case "/t/p/w500/movie.jpg":
				posterEntered <- struct{}{}
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-release:
					return r2Response(200, "poster")
				}
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		c.postersDir = postersDir
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			info, err := c.GetDetails(ctx, MediaTypeMovie, 42, "film.mkv")
			if info != nil {
				err = fmt.Errorf("canceled poster call returned details: %+v", info)
			}
			done <- err
		}()
		<-posterEntered // постер у польоті
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want context.Canceled", err)
		}
		close(release)
		want := []string{"/movie/42?append_to_response=credits&language=uk-UA", "/t/p/w500/movie.jpg?"}
		r2CheckCalls(t, *calls, want)
		entries, err := os.ReadDir(postersDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("poster dir not empty after STOP: %v", entries)
		}
		t.Logf("R8 cancel during poster: calls=%q", *calls)
	})
}

// Один HTTP-запит повертає 500, інший успішний: незалежні операції не
// обмінюються помилками. Маршрутизація за значенням context робить розподіл
// детермінованим: caller A завжди отримує 500, caller B — 200. На R7 grupo
// виконувалася з ctx першого caller, тому B успадковував помилку A.
func TestR8ErrorIsolationBetweenIndependentCallers(t *testing.T) {
	type ctxKey struct{}
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			if req.Context().Value(ctxKey{}) == "fail" {
				return r2Response(500, "fixture outage")
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-release:
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			}
		})
		ctxA := context.WithValue(t.Context(), ctxKey{}, "fail")
		ctxB := context.WithValue(t.Context(), ctxKey{}, "ok")
		type result struct {
			info *MovieInfo
			err  error
		}
		doneA := make(chan result, 1)
		doneB := make(chan result, 1)
		go func() {
			info, err := c.GetDetails(ctxA, MediaTypeMovie, 42, "")
			doneA <- result{info, err}
		}()
		<-entered // A перший — на R7 він лідер shared-запуску
		go func() {
			info, err := c.GetDetails(ctxB, MediaTypeMovie, 42, "")
			doneB <- result{info, err}
		}()
		synctest.Wait()
		close(release)
		rA := <-doneA
		if rA.err == nil || errors.Is(rA.err, context.Canceled) || !strings.Contains(rA.err.Error(), "500") {
			t.Errorf("caller A error=%v, want TMDB HTTP 500 after retries", rA.err)
		}
		if rA.info != nil {
			t.Errorf("failed caller A returned details: %+v", rA.info)
		}
		rB := <-doneB
		if rB.err != nil {
			t.Fatalf("independent caller B inherited A's HTTP 500: %v (calls=%q)", rB.err, *calls)
		}
		if rB.info == nil || rB.info.TMDBID != 42 || rB.info.MediaType != MediaTypeMovie {
			t.Fatalf("caller B identity: %+v", rB.info)
		}
		t.Logf("R8 error isolation: calls=%q", *calls)
	})
}

// Два файли посилаються на одну TMDB identity: identity правильна в обох,
// LocalPosterPath не змішується. На R8 — два незалежні details HTTP (на R7
// singleflight давав 1, цей тест там падає за лічильником).
func TestR8TwoFilesSameIdentityConcurrentSeparatePosters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		postersDir := t.TempDir()
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				<-release
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			case "/t/p/w500/movie.jpg":
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		c.postersDir = postersDir
		type result struct {
			info *MovieInfo
			err  error
		}
		done := make(chan result, 2)
		for _, name := range []string{"a.mkv", "b.mkv"} {
			go func() {
				info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, name)
				done <- result{info, err}
			}()
		}
		synctest.Wait() // обидва details одночасно в польоті
		close(release)
		infos := make([]*MovieInfo, 0, 2)
		for range 2 {
			r := <-done
			if r.err != nil || r.info == nil || r.info.TMDBID != 42 || r.info.MediaType != MediaTypeMovie {
				t.Fatalf("file result: %+v, %v", r.info, r.err)
			}
			infos = append(infos, r.info)
		}
		if infos[0].LocalPosterPath == "" || infos[0].LocalPosterPath == infos[1].LocalPosterPath {
			t.Fatalf("posters not separated: %q vs %q", infos[0].LocalPosterPath, infos[1].LocalPosterPath)
		}
		for _, p := range []string{infos[0].LocalPosterPath, infos[1].LocalPosterPath} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("poster missing on disk: %s: %v", p, err)
			}
		}
		details := 0
		posters := 0
		for _, call := range *calls {
			if strings.HasPrefix(call, "/movie/42") {
				details++
			}
			if strings.HasPrefix(call, "/t/p/") {
				posters++
			}
		}
		if details != 2 {
			t.Errorf("details requests=%d, want 2 independent: %q", details, *calls)
		}
		if posters != 2 {
			t.Errorf("poster requests=%d, want 2 per-file: %q", posters, *calls)
		}
		t.Logf("R8 two files same identity: details=%d posters=%d posterA=%q posterB=%q", details, posters, infos[0].LocalPosterPath, infos[1].LocalPosterPath)
	})
}

// Окремо: незалежні context scan і ручної операції редактора над однією
// identity — обидва успішні з власними HTTP; скасування context редактора не
// впливає на scan context, і наступний scan-виклик після цього працює.
func TestR8IndependentScanAndEditorContexts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var requests atomic.Int32
		entered := make(chan struct{}, 4)
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-release:
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			}
		})
		scanCtx := t.Context()
		editorCtx, cancelEditor := context.WithCancel(t.Context())
		defer cancelEditor()
		type result struct {
			info *MovieInfo
			err  error
		}
		scanDone := make(chan result, 1)
		editorDone := make(chan result, 1)
		go func() {
			info, err := c.GetDetails(scanCtx, MediaTypeMovie, 42, "")
			scanDone <- result{info, err}
		}()
		<-entered
		go func() {
			info, err := c.GetDetails(editorCtx, MediaTypeMovie, 42, "")
			editorDone <- result{info, err}
		}()
		synctest.Wait()
		cancelEditor() // редактор закрив операцію
		if r := <-editorDone; !errors.Is(r.err, context.Canceled) {
			t.Errorf("editor error=%v, want context.Canceled", r.err)
		}
		close(release)
		if r := <-scanDone; r.err != nil || r.info == nil || r.info.TMDBID != 42 {
			t.Fatalf("scan after editor cancel: %+v, %v", r.info, r.err)
		}
		if got := requests.Load(); got != 2 {
			t.Fatalf("requests=%d, want 2 independent: %q", got, *calls)
		}
		// Наступний scan-виклик після скасування редактора — свіжий успішний HTTP.
		info, err := c.GetDetails(scanCtx, MediaTypeMovie, 42, "")
		if err != nil || info == nil || info.TMDBID != 42 {
			t.Errorf("scan reread: %+v, %v", info, err)
		}
		if got := requests.Load(); got != 3 {
			t.Errorf("requests after scan reread=%d, want 3: %q", got, *calls)
		}
		t.Logf("R8 scan/editor contexts: calls=%q", *calls)
	})
}
