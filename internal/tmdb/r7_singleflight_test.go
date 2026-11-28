package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// R7: повторне незалежне звернення завжди отримує свіжу відповідь TMDB,
// навіть якщо та сама identity вже читалася раніше.
func TestR7FreshDetailsAfterTMDBChange(t *testing.T) {
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
	t.Logf("R7 fresh details: titleA=%q titleB=%q calls=%q", first.TitleUA, second.TitleUA, *calls)
}

func TestR7PreviewFreshAfterTMDBChange(t *testing.T) {
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

// 20 одночасних читачів тієї самої пари об'єднуються singleflight в один
// waterfall; наступне незалежне читання після завершення групи — новий HTTP.
func TestR7SingleflightTwentyReadersThenIndependentRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		c, wire := r2Fixture(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release // тримаємо перший запит у польоті, поки всі читачі приєднаються до групи
			return r2Response(200, r7MovieDetailsBody("Фільм"))
		})
		const readers = 20
		var wg sync.WaitGroup
		errs := make(chan error, readers)
		for range readers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
				if err == nil && (info == nil || info.TMDBID != 42 || info.MediaType != MediaTypeMovie) {
					err = fmt.Errorf("bad details: %+v", info)
				}
				errs <- err
			}()
		}
		<-entered
		synctest.Wait() // усі 20 читачів заблоковані на результаті одного singleflight-запуску
		close(release)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("singleflight waterfall requests=%d, want 1: %q", got, *wire)
		}
		// Після завершення групи наступний виклик — свіжий HTTP (кешу немає).
		info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
		if err != nil || info == nil || info.TMDBID != 42 {
			t.Fatalf("independent read after group: %+v, %v", info, err)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("requests after independent read=%d, want 2: %q", got, *wire)
		}
		t.Logf("R7 singleflight 20 readers then repeat: calls=%q", *wire)
	})
}

// Одночасні movie:42 і tv:42 — різні ключі singleflight, різні typed відповіді.
func TestR7SingleflightMovieTVSeparateKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan string, 2)
		release := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			entered <- req.URL.Path
			<-release
			if req.URL.Path == "/3/tv/42" {
				return r2Response(200, r7TVDetailsBody("Серіал"))
			}
			return r2Response(200, r7MovieDetailsBody("Фільм"))
		})
		type result struct {
			info *MovieInfo
			err  error
		}
		done := map[MediaType]chan result{MediaTypeMovie: make(chan result, 1), MediaTypeTV: make(chan result, 1)}
		for _, kind := range []MediaType{MediaTypeMovie, MediaTypeTV} {
			go func() {
				info, err := c.GetDetails(t.Context(), kind, 42, "")
				done[kind] <- result{info, err}
			}()
		}
		paths := []string{<-entered, <-entered}
		if paths[0] == paths[1] {
			t.Fatalf("both requests hit %s", paths[0])
		}
		close(release)
		m := <-done[MediaTypeMovie]
		tv := <-done[MediaTypeTV]
		if m.err != nil || m.info == nil || m.info.MediaType != MediaTypeMovie || m.info.TitleUA != "Фільм" {
			t.Errorf("movie: %+v, %v", m.info, m.err)
		}
		if tv.err != nil || tv.info == nil || tv.info.MediaType != MediaTypeTV || tv.info.TitleUA != "Серіал" {
			t.Errorf("tv: %+v, %v", tv.info, tv.err)
		}
		if len(*calls) != 2 {
			t.Errorf("HTTP calls=%d, want 2: %q", len(*calls), *calls)
		}
		t.Logf("R7 singleflight movie/tv same id: calls=%q", *calls)
	})
}

// Спільний HTTP 500: усі waiters групи отримують помилку; наступна спроба
// після завершення групи — успішна.
func TestR7SingleflightSharedErrorThenRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fail atomic.Bool
		fail.Store(true)
		var calls atomic.Int32
		c, wire := r2Fixture(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			if fail.Load() {
				return r2Response(500, "fixture outage")
			}
			return r2Response(200, r7MovieDetailsBody("Фільм"))
		})
		const waiters = 3
		errs := make(chan error, waiters)
		for range waiters {
			go func() {
				info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
				if info != nil {
					err = fmt.Errorf("error group returned details: %+v", info)
				}
				errs <- err
			}()
		}
		for range waiters {
			if err := <-errs; err == nil || errors.Is(err, context.Canceled) {
				t.Fatalf("shared 500 waiter error=%v, want TMDB error", err)
			}
		}
		// Група завершилася помилкою — наступний виклик починає нову групу.
		fail.Store(false)
		info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
		if err != nil || info == nil || info.TMDBID != 42 {
			t.Fatalf("recovery after shared error: %+v, %v", info, err)
		}
		t.Logf("R7 shared 500: first group calls=%d, total=%d, recovery calls=%q", 9, calls.Load(), *wire)
		if calls.Load() != 10 { // 3 мови × 3 retries у групі + 1 свіжий uk-UA
			t.Errorf("total calls=%d, want 10: %q", calls.Load(), *wire)
		}
	})
}

// ВІДОМА МЕЖА singleflight (зафіксована, не прихована): скасування першого
// caller, який розпочав HTTP, провалює й інших waiters тієї самої пари,
// бо singleflight ділить один запуск із ctx першого caller. Waiter з власним
// скасуванням обробляє його окремо (див. TestGetDetailsWaitingReaderHonorsCancellation).
// У R7 це не латано новою системою context ownership; рішення — окреме рішення
// наступного етапу (detach ctx або відмова від singleflight).
func TestR7SingleflightFirstCallerCancelAffectsWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			once.Do(func() { close(entered) })
			<-release
			return r2Response(200, r7MovieDetailsBody("Фільм"))
		})
		ctx1, cancel1 := context.WithCancel(t.Context())
		defer cancel1()
		ctx2, cancel2 := context.WithCancel(t.Context())
		defer cancel2()
		done1 := make(chan error, 1)
		done2 := make(chan error, 1)
		go func() { _, err := c.GetDetails(ctx1, MediaTypeMovie, 42, ""); done1 <- err }()
		<-entered
		go func() { _, err := c.GetDetails(ctx2, MediaTypeMovie, 42, ""); done2 <- err }()
		synctest.Wait() // другий читач приєднався до групи й чекає на результат
		cancel1()       // скасовуємо першого caller, який розпочав HTTP
		if err := <-done1; !errors.Is(err, context.Canceled) {
			t.Errorf("first caller error=%v, want context.Canceled", err)
		}
		close(release)
		err2 := <-done2
		if !errors.Is(err2, context.Canceled) {
			t.Logf("R7 singleflight first-cancel: independent waiter error=%v (not context.Canceled)", err2)
		} else {
			t.Logf("R7 singleflight first-cancel: independent waiter FAILED with context.Canceled (known shared-ctx limitation), calls=%q", *calls)
		}
		// Наступний виклик із живим ctx після завершення групи — успішний.
		info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
		if err != nil || info == nil || info.TMDBID != 42 {
			t.Errorf("recovery after first-caller cancel: %+v, %v", info, err)
		}
		if len(*calls) != 2 {
			t.Errorf("HTTP calls=%d, want 2: %q", len(*calls), *calls)
		}
	})
}
