package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func r3DetailsBody(kind MediaType) string {
	if kind == MediaTypeTV {
		return strings.Replace(r2TVDetails, "73", "42", 1)
	}
	return fixtureMovieDetails
}

func TestR3DetailsResponse(t *testing.T) {
	for _, kind := range []MediaType{MediaTypeMovie, MediaTypeTV} {
		body := r3DetailsBody(kind)
		other := MediaTypeTV
		if kind == MediaTypeTV {
			other = MediaTypeMovie
		}
		otherTitle := `"name":"Another work"`
		if kind == MediaTypeTV {
			otherTitle = `"title":"Another work"`
		}
		for _, tc := range []struct {
			name         string
			status       int
			body         string
			fullRequests int
		}{
			{"valid", 200, body, 1},
			{"different_id", 200, strings.Replace(body, "42", "43", 1), 1},
			{"different_shape", 200, r3DetailsBody(other), 1},
			{"mixed_shape", 200, fmt.Sprintf(`{%s,%s`, otherTitle, body[1:]), 1},
			{"different_reported_type", 200, fmt.Sprintf(`{"media_type":%q,%s`, other, body[1:]), 1},
			{"missing_id", 200, strings.Replace(body, `"id":42,`, "", 1), 1},
			{"zero_id", 200, strings.Replace(body, "42", "0", 1), 1},
			{"not_found", 404, "", 3},
			{"retry_exhausted", 500, "fixture failure", 9},
			{"invalid_json", 200, "{", 9},
		} {
			for _, preview := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/preview=%t", kind, tc.name, preview), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						posterDir := t.TempDir()
						fixed := false
						path := fmt.Sprintf("/3/%s/42", kind)
						c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
							switch req.URL.Path {
							case path:
								if fixed {
									return r2Response(200, body)
								}
								payload := tc.body
								if tc.status == 200 && strings.HasSuffix(payload, "}") && tc.name != "valid" {
									payload = strings.TrimSuffix(payload, "}") + `,"poster_path":"/foreign.jpg"}`
								}
								return r2Response(tc.status, payload)
							case "/t/p/w500/foreign.jpg":
								return r2Response(200, "fixture poster")
							default:
								return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
							}
						})
						c.postersDir = posterDir
						read := func() (r2Identity, string, error) {
							if preview {
								info, err := c.GetCandidateDetails(t.Context(), kind, 42)
								if info == nil {
									return r2Identity{}, "", err
								}
								return r2Identity{info.MediaType, info.TMDBID}, info.PosterURL, err
							}
							info, err := c.GetDetails(t.Context(), kind, 42, "fixture.mkv")
							if info == nil {
								return r2Identity{}, "", err
							}
							return r2Identity{info.MediaType, info.TMDBID}, info.PosterURL, err
						}
						want := r2Identity{kind, 42}
						actual, poster, err := read()
						t.Logf("R3_DETAILS first=%+v poster=%t err=%v calls=%q", actual, poster != "", err, *calls)
						if tc.name == "valid" {
							if actual != want || err != nil {
								t.Errorf("valid details: %+v, %v", actual, err)
							}
						} else if actual != (r2Identity{}) || poster != "" || err == nil {
							t.Errorf("invalid details: %+v, poster=%q, error=%v", actual, poster, err)
						}
						files, readErr := os.ReadDir(posterDir)
						if readErr != nil || len(files) != 0 {
							t.Errorf("poster files after read: %v, %v", files, readErr)
						}
						requests := tc.fullRequests
						if preview && requests > 1 {
							requests /= 3
						}
						var wantCalls []string
						for i := range requests {
							query := "language=uk-UA"
							if !preview {
								retries := 1
								if requests == 9 {
									retries = 3
								}
								query = "append_to_response=credits&language=" + []string{"uk-UA", "ru-RU", "en-US"}[i/retries]
							}
							wantCalls = append(wantCalls, fmt.Sprintf("/%s/42?%s", kind, query))
						}
						r2CheckCalls(t, *calls, wantCalls)
						start := len(*calls)
						fixed = true
						actual, _, err = read()
						t.Logf("R3_DETAILS second=%+v err=%v calls=%q", actual, err, (*calls)[start:])
						if actual != want || err != nil {
							t.Errorf("second read used wrong identity: %+v, %v", actual, err)
						}
						// R7: кешу деталей немає — повторне читання завжди йде в TMDB.
						// Після fixed=true відповідь валідна, тому водоспад зупиняється на uk-UA.
						r2CheckCalls(t, (*calls)[start:], wantCalls[:1])
					})
				})
			}
		}
	}
}

func TestR3DetailsCallers(t *testing.T) {
	for _, mode := range []string{"ranked", "exact", "imdb_movie", "imdb_tv", "refresh", "waterfall"} {
		for _, valid := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/valid=%t", mode, valid), func(t *testing.T) {
				kind := MediaTypeMovie
				if mode == "imdb_tv" {
					kind = MediaTypeTV
				}
				detailsPath := fmt.Sprintf("/3/%s/42", kind)
				c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
					switch req.URL.Path {
					case "/3/search/movie":
						return r2Response(200, r2Movie)
					case "/3/find/tt1234567":
						return r2Response(200, fmt.Sprintf(`{"%s_results":[{"id":42}]}`, kind))
					case detailsPath:
						body := r3DetailsBody(kind)
						if mode == "waterfall" && req.URL.Query().Get("language") == "uk-UA" {
							return r2Response(200, `{"id":42,"title":"Signal","overview":"English plot"}`)
						}
						if !valid {
							body = strings.Replace(body, "42", "43", 1)
							body = strings.TrimSuffix(body, "}") + `,"poster_path":"/foreign.jpg"}`
						}
						return r2Response(200, body)
					case "/t/p/w500/foreign.jpg":
						return r2Response(200, "fixture poster")
					default:
						return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
					}
				})
				c.postersDir = t.TempDir()
				ctx := t.Context()
				var got *MovieInfo
				var err error
				switch mode {
				case "ranked":
					got, err = c.searchAndFetch(ctx, "Signal", 0, 2024, kind, "fixture.mkv", ParsedFile{})
				case "exact":
					got, err = c.SearchExactTitle(ctx, "Signal", 2024, kind, true, "fixture.mkv")
				case "imdb_movie", "imdb_tv":
					got, err = c.FetchByIMDB(ctx, "tt1234567", "fixture.mkv")
				case "refresh":
					got, err = c.RefreshDetails(ctx, kind, 42)
				case "waterfall":
					got, err = c.GetDetails(ctx, kind, 42, "fixture.mkv")
				}
				t.Logf("R3_CALLER info=%+v err=%v calls=%q", got, err, *calls)
				if valid && (got == nil || got.MediaType != kind || got.TMDBID != 42 || err != nil) {
					t.Errorf("valid details: %+v, %v", got, err)
				}
				if !valid && (got != nil || err == nil) {
					t.Errorf("invalid details: %+v, %v", got, err)
				}
				wantDetails := 1
				if mode == "waterfall" {
					wantDetails = 2
					if valid {
						wantDetails = 3
					}
				}
				total := wantDetails
				if mode == "exact" || mode == "ranked" || strings.HasPrefix(mode, "imdb") {
					total++
				}
				if len(*calls) != total || slices.Contains(*calls, "/t/p/w500/foreign.jpg?") {
					t.Errorf("unexpected HTTP calls %q; want %d", *calls, total)
				}
			})
		}
	}
}

func TestR3DetailsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		entered := make(chan struct{})
		var once sync.Once
		var blocking atomic.Bool
		blocking.Store(true)
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			if blocking.Load() {
				once.Do(func() { close(entered) })
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			return r2Response(200, fixtureMovieDetails)
		})
		done := make(chan error, 1)
		go func() {
			got, err := c.GetDetails(ctx, MediaTypeMovie, 42, "")
			if got != nil {
				t.Errorf("cancellation returned details: %+v", got)
			}
			done <- err
		}()
		<-entered
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v; want context.Canceled", err)
		}
		synctest.Wait()
		r2CheckCalls(t, *calls, []string{"/movie/42?append_to_response=credits&language=uk-UA"})
		// R7: скасований запит ніде не зберігається — наступний виклик робить свіжий HTTP.
		blocking.Store(false)
		fresh, err := c.GetDetails(t.Context(), MediaTypeMovie, 42, "")
		if err != nil || fresh == nil || fresh.TMDBID != 42 {
			t.Errorf("read after cancellation: %+v, %v", fresh, err)
		}
		r2CheckCalls(t, (*calls)[1:], []string{"/movie/42?append_to_response=credits&language=uk-UA"})
	})
}

func TestR3DetailsRetryKeepsResponsesSeparate(t *testing.T) {
	for _, kind := range []MediaType{MediaTypeMovie, MediaTypeTV} {
		t.Run(string(kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				valid := r3DetailsBody(kind)
				field := "title"
				if kind == MediaTypeTV {
					field = "name"
				}
				first := fmt.Sprintf(`{"id":42,"%s":999}`, field)
				second := strings.Replace(valid, `"id":42,`, "", 1)
				attempt := 0
				c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
					attempt++
					if attempt == 1 {
						return r2Response(200, first)
					}
					return r2Response(200, second)
				})
				got, err := c.GetDetails(t.Context(), kind, 42, "")
				t.Logf("R3_DETAILS_RETRY info=%+v err=%v calls=%q", got, err, *calls)
				if got != nil || err == nil {
					t.Errorf("retry inherited previous ID: %+v, %v", got, err)
				}
				call := fmt.Sprintf("/%s/42?append_to_response=credits&language=uk-UA", kind)
				r2CheckCalls(t, *calls, []string{call, call})
			})
		})
	}
}
