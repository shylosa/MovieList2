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

// R8 A/B oracle: кожен сценарій проганяється на A (R7, singleflight) і на
// B (R8, незалежні запити) з однаковими фікстурами. Оракул перевіряє typed
// identity (MediaType + TMDB ID) і per-file poster; HTTP-лічильники лише
// логуються — їх порівняння дає A/B-таблицю. Файл свідомо використовує лише
// API й хелпери, що існують і на A, і на B, тому компілюється на обох.

func r8ABClient(t *testing.T, reply func(req *http.Request, n int) (*http.Response, error)) (*Client, *[]string) {
	t.Helper()
	n := 0
	c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
		n++
		return reply(req, n)
	})
	c.postersDir = t.TempDir()
	return c, calls
}

type r8Counts struct{ details, aliases, posters, search, find, total int }

func r8Count(calls []string) r8Counts {
	var c r8Counts
	for _, call := range calls {
		switch {
		case strings.Contains(call, "/alternative_titles"):
			c.aliases++
		case strings.HasPrefix(call, "/t/p/"):
			c.posters++
		case strings.Contains(call, "/search/"):
			c.search++
		case strings.Contains(call, "/find/"):
			c.find++
		default:
			c.details++
		}
	}
	c.total = len(calls)
	return c
}

func r8Log(t *testing.T, name string, calls []string, extra string) {
	c := r8Count(calls)
	t.Logf("R8_AB %s http=%d details=%d aliases=%d posters=%d search=%d find=%d %s calls=%q",
		name, c.total, c.details, c.aliases, c.posters, c.search, c.find, extra, calls)
}

func r8CheckIdentity(t *testing.T, name string, info *MovieInfo, err error, wantType MediaType, wantID int) {
	t.Helper()
	if err != nil || info == nil || info.MediaType != wantType || info.TMDBID != wantID {
		t.Errorf("%s: got %+v, %v; want %s:%d", name, info, err, wantType, wantID)
	}
}

func TestR8ABScenarios(t *testing.T) {
	ctx := context.Background()

	// 1. Один GetDetails.
	t.Run("single_get_details", func(t *testing.T) {
		c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			case "/t/p/w500/movie.jpg":
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		info, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		r8CheckIdentity(t, "single", info, err, MediaTypeMovie, 42)
		r8Log(t, "single_get_details", *calls, fmt.Sprintf("poster=%q", info.LocalPosterPath))
	})

	// 2. Послідовні повтори GetDetails — завжди свіжий HTTP.
	t.Run("sequential_repeat_get_details", func(t *testing.T) {
		c, calls := r8ABClient(t, func(req *http.Request, n int) (*http.Response, error) {
			if req.URL.Path == "/3/movie/42" {
				return r2Response(200, r7MovieDetailsBody(fmt.Sprintf("Фільм версія %d", n)))
			}
			if strings.HasPrefix(req.URL.Path, "/t/p/") {
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		first, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		r8CheckIdentity(t, "first", first, err, MediaTypeMovie, 42)
		second, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		r8CheckIdentity(t, "second", second, err, MediaTypeMovie, 42)
		r8Log(t, "sequential_repeat", *calls, fmt.Sprintf("fresh=%t titleA=%q titleB=%q", second.TitleUA != first.TitleUA, first.TitleUA, second.TitleUA))
	})

	// 3. 2 одночасні callers тієї самої identity (бар'єр, synctest).
	t.Run("two_concurrent_same_identity", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
				<-release
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			})
			done := make(chan error, 2)
			for range 2 {
				go func() {
					info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
					if err == nil && (info == nil || info.TMDBID != 42 || info.MediaType != MediaTypeMovie) {
						err = fmt.Errorf("bad identity: %+v", info)
					}
					done <- err
				}()
			}
			synctest.Wait()
			close(release)
			for range 2 {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			r8Log(t, "two_concurrent_same_identity", *calls, "")
		})
	})

	// 4. 20 одночасних callers тієї самої identity.
	t.Run("twenty_concurrent_same_identity", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
				<-release
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			})
			const readers = 20
			done := make(chan error, readers)
			for range readers {
				go func() {
					info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
					if err == nil && (info == nil || info.TMDBID != 42) {
						err = fmt.Errorf("bad identity: %+v", info)
					}
					done <- err
				}()
			}
			synctest.Wait()
			close(release)
			for range readers {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			r8Log(t, "twenty_concurrent_same_identity", *calls, "")
		})
	})

	// 5. 20 callers із різними identity.
	t.Run("twenty_different_identities", func(t *testing.T) {
		c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			if strings.HasPrefix(req.URL.Path, "/3/movie/") {
				var id int
				fmt.Sscanf(req.URL.Path, "/3/movie/%d", &id)
				return r2Response(200, fmt.Sprintf(`{"id":%d,"title":"Фільм %d","original_title":"Film","release_date":"2024-01-01","overview":"Опис","genres":[]}`, id, id))
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := 100 + i
				info, err := c.GetDetails(ctx, MediaTypeMovie, id, "")
				if err == nil && (info == nil || info.TMDBID != id || info.MediaType != MediaTypeMovie) {
					err = fmt.Errorf("bad identity: %+v", info)
				}
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
		r8Log(t, "twenty_different_identities", *calls, "")
	})

	// 6. Movie та TV з однаковим числовим ID — відповіді не змішуються.
	t.Run("movie_tv_same_numeric_id", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
				<-release
				switch req.URL.Path {
				case "/3/movie/42":
					return r2Response(200, r7MovieDetailsBody("Фільм"))
				case "/3/tv/42":
					return r2Response(200, r7TVDetailsBody("Серіал"))
				}
				return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
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
			synctest.Wait()
			close(release)
			m := <-done[MediaTypeMovie]
			tv := <-done[MediaTypeTV]
			r8CheckIdentity(t, "movie", m.info, m.err, MediaTypeMovie, 42)
			r8CheckIdentity(t, "tv", tv.info, tv.err, MediaTypeTV, 42)
			if m.info.TitleUA == tv.info.TitleUA {
				t.Errorf("movie and tv share title %q", m.info.TitleUA)
			}
			r8Log(t, "movie_tv_same_numeric_id", *calls, fmt.Sprintf("movie=%q tv=%q", m.info.TitleUA, tv.info.TitleUA))
		})
	})

	// 7. Скасування першого caller. На A (R7) waiter успадковує context.Canceled;
	// на B — успішно завершується. Тут лише логуємо різницю; строга регресія
	// для B — у r8_independent_details_test.go.
	t.Run("cancel_first_caller", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
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
			done1 := make(chan error, 1)
			done2 := make(chan error, 1)
			go func() { _, err := c.GetDetails(ctx1, MediaTypeMovie, 42, ""); done1 <- err }()
			<-entered
			go func() { _, err := c.GetDetails(ctx2, MediaTypeMovie, 42, ""); done2 <- err }()
			synctest.Wait()
			cancel1()
			err1 := <-done1
			close(release)
			err2 := <-done2
			if !errors.Is(err1, context.Canceled) {
				t.Errorf("first caller error=%v, want context.Canceled", err1)
			}
			r8Log(t, "cancel_first_caller", *calls, fmt.Sprintf("err1=%v err2=%v", err1, err2))
		})
	})

	// 8. Скасування другого caller: перший продовжує роботу (працює на A і B).
	t.Run("cancel_second_caller", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
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
			done1 := make(chan error, 1)
			done2 := make(chan error, 1)
			go func() { info, err := c.GetDetails(ctx1, MediaTypeMovie, 42, ""); _ = info; done1 <- err }()
			<-entered
			go func() { _, err := c.GetDetails(ctx2, MediaTypeMovie, 42, ""); done2 <- err }()
			synctest.Wait()
			cancel2()
			if err := <-done2; !errors.Is(err, context.Canceled) {
				t.Errorf("second caller error=%v, want context.Canceled", err)
			}
			close(release)
			if err := <-done1; err != nil {
				t.Errorf("first caller error=%v, want nil", err)
			}
			r8Log(t, "cancel_second_caller", *calls, "")
		})
	})

	// 9. Скасування спільного parent context перериває обох і не запускає нові етапи.
	t.Run("cancel_shared_parent", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-release:
					return r2Response(200, r7MovieDetailsBody("Фільм"))
				}
			})
			shared, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 2)
			for range 2 {
				go func() { _, err := c.GetDetails(shared, MediaTypeMovie, 42, ""); done <- err }()
			}
			synctest.Wait()
			cancel()
			for range 2 {
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("caller error=%v, want context.Canceled", err)
				}
			}
			close(release)
			for _, call := range *calls {
				if !strings.Contains(call, "language=uk-UA") || strings.HasPrefix(call, "/t/p/") {
					t.Errorf("unexpected stage after STOP: %q", call)
				}
			}
			r8Log(t, "cancel_shared_parent", *calls, "")
		})
	})

	// 10. HTTP outage та retry: 3 мови × 3 спроби = 9 HTTP і помилка, далі recovery.
	t.Run("http_outage_and_retry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var fail atomic.Bool
			fail.Store(true)
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
				if fail.Load() {
					return r2Response(500, "fixture outage")
				}
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			})
			info, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
			if err == nil || info != nil || !strings.Contains(err.Error(), "500") {
				t.Errorf("outage: %+v, %v — want HTTP 500 error", info, err)
			}
			fail.Store(false)
			info, err = c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
			r8CheckIdentity(t, "recovery", info, err, MediaTypeMovie, 42)
			r8Log(t, "http_outage_and_retry", *calls, "")
		})
	})

	// 11. Два filename з однаковою identity — окремі per-file постери.
	t.Run("two_files_same_identity", func(t *testing.T) {
		c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			case "/t/p/w500/movie.jpg":
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		a, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		r8CheckIdentity(t, "a", a, err, MediaTypeMovie, 42)
		b, err := c.GetDetails(ctx, MediaTypeMovie, 42, "b.mkv")
		r8CheckIdentity(t, "b", b, err, MediaTypeMovie, 42)
		if a.LocalPosterPath == "" || a.LocalPosterPath == b.LocalPosterPath {
			t.Errorf("posters not separated: %q vs %q", a.LocalPosterPath, b.LocalPosterPath)
		}
		for _, p := range []string{a.LocalPosterPath, b.LocalPosterPath} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("poster missing: %s: %v", p, err)
			}
		}
		r8Log(t, "two_files_same_identity", *calls, fmt.Sprintf("posterA=%q posterB=%q", a.LocalPosterPath, b.LocalPosterPath))
	})

	// 12. Preview → confirm.
	t.Run("preview_then_confirm", func(t *testing.T) {
		c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				if req.URL.Query().Get("append_to_response") != "" {
					return r2Response(200, r7MovieDetailsBody("Фільм"))
				}
				return r2Response(200, `{"id":42,"title":"Превʼю","original_title":"Signal","release_date":"2024-01-01","vote_average":7.5,"vote_count":100}`)
			case "/t/p/w500/movie.jpg":
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		preview, err := c.GetCandidateDetails(ctx, MediaTypeMovie, 42)
		if err != nil || preview == nil || preview.TMDBID != 42 || preview.MediaType != MediaTypeMovie {
			t.Fatalf("preview: %+v, %v", preview, err)
		}
		confirmed, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		r8CheckIdentity(t, "confirm", confirmed, err, MediaTypeMovie, 42)
		r8Log(t, "preview_then_confirm", *calls, fmt.Sprintf("previewTitle=%q confirmed=%q", preview.Title, confirmed.TitleUA))
	})

	// 13. Refresh після GetDetails.
	t.Run("refresh_after_get", func(t *testing.T) {
		c, calls := r8ABClient(t, func(req *http.Request, n int) (*http.Response, error) {
			if req.URL.Path == "/3/movie/42" {
				return r2Response(200, r7MovieDetailsBody(fmt.Sprintf("Фільм версія %d", n)))
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		got, err := c.GetDetails(ctx, MediaTypeMovie, 42, "")
		r8CheckIdentity(t, "get", got, err, MediaTypeMovie, 42)
		refreshed, err := c.RefreshDetails(ctx, MediaTypeMovie, 42)
		r8CheckIdentity(t, "refresh", refreshed, err, MediaTypeMovie, 42)
		r8Log(t, "refresh_after_get", *calls, fmt.Sprintf("fresh=%t", refreshed.TitleUA != got.TitleUA))
	})

	// 14. Repair-рівень: RefreshDetails за збереженою typed identity (TV).
	t.Run("repair_refresh_tv", func(t *testing.T) {
		c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			if req.URL.Path == "/3/tv/42" {
				return r2Response(200, r7TVDetailsBody("Серіал"))
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		info, err := c.RefreshDetails(ctx, MediaTypeTV, 42)
		r8CheckIdentity(t, "repair", info, err, MediaTypeTV, 42)
		r8Log(t, "repair_refresh_tv", *calls, "")
	})

	// 15. Скасування під час завантаження постера.
	t.Run("cancel_during_poster", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			posterEntered := make(chan struct{}, 1)
			release := make(chan struct{})
			c, calls := r8ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
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
			opCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.GetDetails(opCtx, MediaTypeMovie, 42, "film.mkv"); done <- err }()
			<-posterEntered
			synctest.Wait()
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("error=%v, want context.Canceled", err)
			}
			close(release)
			r8Log(t, "cancel_during_poster", *calls, "")
		})
	})
}
