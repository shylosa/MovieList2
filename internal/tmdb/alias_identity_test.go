package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
)

const r3MovieAliases = `{"id":42,"titles":[{"title":"Crimson Horizon"}]}`
const r3TVAliases = `{"id":42,"results":[{"title":"Silver Junction"}]}`

func TestR3AliasCollision(t *testing.T) {
	type entity struct {
		kind   MediaType
		title  string
		alias  string
		search string
		alts   string
		detail string
	}
	movie := entity{MediaTypeMovie, "Alpha", "Crimson Horizon", strings.ReplaceAll(r2Movie, "Signal", "Alpha"),
		r3MovieAliases, strings.ReplaceAll(fixtureMovieDetails, "Signal", "Alpha")}
	tv := entity{MediaTypeTV, "Beta", "Silver Junction", strings.NewReplacer("Signal", "Beta", "73", "42").Replace(r2TV),
		r3TVAliases, strings.NewReplacer("Signal", "Beta", "73", "42").Replace(r2TVDetails)}
	for _, ranked := range []bool{false, true} {
		for _, shape := range []string{"distinct", "movie_empty", "tv_empty"} {
			for _, movieFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("ranked=%t/%s/movie_first=%t", ranked, shape, movieFirst), func(t *testing.T) {
					first, second := movie, tv
					if shape == "movie_empty" {
						first.alts = `{"id":42,"titles":[]}`
					}
					if shape == "tv_empty" {
						second.alts = `{"id":42,"results":[]}`
					}
					if !movieFirst {
						first, second = second, first
					}
					c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
						for _, e := range []entity{first, second} {
							switch req.URL.Path {
							case "/3/search/" + string(e.kind):
								return r2Response(200, e.search)
							case fmt.Sprintf("/3/%s/42/alternative_titles", e.kind):
								return r2Response(200, e.alts)
							case fmt.Sprintf("/3/%s/42", e.kind):
								return r2Response(200, e.detail)
							}
						}
						return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
					})
					for _, check := range []struct {
						e     entity
						query string
						hit   bool
						cold  bool
					}{
						{first, first.alias, strings.Contains(first.alts, first.alias), true},
						{second, second.alias, strings.Contains(second.alts, second.alias), true},
						{second, first.alias, false, false},
						{first, first.alias, strings.Contains(first.alts, first.alias), false},
					} {
						start := len(*calls)
						var got *MovieInfo
						var err error
						if ranked {
							got, err = c.searchAndFetch(context.Background(), check.query, 0, 2024, check.e.kind, "", ParsedFile{})
						} else {
							got, err = c.SearchExactTitle(context.Background(), check.query, 2024, check.e.kind, true, "")
						}
						want := r2Identity{}
						if check.hit {
							want = r2Identity{check.e.kind, 42}
						}
						r2Check(t, fmt.Sprintf("%s/%s", check.e.kind, check.query), got, err, want, (*calls)[start:])
						if check.hit && got != nil && got.MatchedAlias != check.query {
							t.Errorf("matched alias = %q, want %q", got.MatchedAlias, check.query)
						}
						wantCalls := []string{
							fmt.Sprintf("/search/%s?language=en-US&query=%s", check.e.kind, strings.ReplaceAll(check.query, " ", "+")),
							fmt.Sprintf("/%s/42/alternative_titles?", check.e.kind),
						}
						if check.hit && check.cold {
							wantCalls = append(wantCalls, fmt.Sprintf("/%s/42?append_to_response=credits&language=uk-UA", check.e.kind))
						}
						r2CheckCalls(t, (*calls)[start:], wantCalls)
					}
				})
			}
		}
	}
}

func TestR3AliasFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		attempt int
	}{
		{"not_found", 404, "", 1},
		{"retry_exhausted", 500, "fixture failure", 3},
		{"invalid_json", 200, "{", 3},
		{"cancel", 500, "fixture failure", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
					switch req.URL.Path {
					case "/3/search/movie":
						return r2Response(200, r2Movie)
					case "/3/movie/42/alternative_titles":
						if tc.name == "cancel" {
							cancel()
						}
						return r2Response(tc.status, tc.body)
					default:
						t.Errorf("unexpected endpoint %s", req.URL.Path)
						return r2Response(404, "")
					}
				})
				got, err := c.SearchExactTitle(ctx, "Crimson Horizon", 2024, MediaTypeMovie, true, "")
				t.Logf("R3_ALIAS_FAILURE info=%v err=%v calls=%q", got, err, *calls)
				if got != nil || err != nil {
					t.Fatalf("alias failure became an exact match or wrong error: %+v, %v", got, err)
				}
				wantCalls := []string{"/search/movie?language=en-US&query=Crimson+Horizon"}
				for range tc.attempt {
					wantCalls = append(wantCalls, "/movie/42/alternative_titles?")
				}
				r2CheckCalls(t, *calls, wantCalls)
			})
		})
	}
}

func TestR3AliasCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan struct{})
		c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
			close(started)
			return r2Response(500, "fixture failure")
		})
		done := make(chan error, 1)
		go func() {
			titles, err := c.getAlternativeTitles(ctx, 42, MediaTypeTV)
			if len(titles) != 0 {
				t.Errorf("canceled alias request returned %q", titles)
			}
			done <- err
		}()
		<-started
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("alias error = %v; want context.Canceled", err)
		}
		r2CheckCalls(t, *calls, []string{"/tv/42/alternative_titles?"})
	})
}

func TestR3AliasParallel(t *testing.T) {
	type result struct {
		titles []string
		err    error
	}
	for _, first := range []string{"movie", "tv"} {
		t.Run(first+"_finishes_first", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				entered := make(chan string, 2)
				release := map[string]chan struct{}{"movie": make(chan struct{}), "tv": make(chan struct{})}
				done := map[string]chan result{"movie": make(chan result, 1), "tv": make(chan result, 1)}
				want := map[string][]string{"movie": {"Crimson Horizon"}, "tv": {"Silver Junction"}}
				c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
					kind := strings.Split(req.URL.Path, "/")[2]
					entered <- req.URL.Path
					<-release[kind]
					if kind == "movie" {
						return r2Response(200, r3MovieAliases)
					}
					return r2Response(200, r3TVAliases)
				})
				for _, kind := range []string{"movie", "tv"} {
					go func() {
						titles, err := c.getAlternativeTitles(ctx, 42, MediaType(kind))
						done[kind] <- result{titles, err}
					}()
				}
				paths := []string{<-entered, <-entered}
				slices.Sort(paths)
				r2CheckCalls(t, paths, []string{"/3/movie/42/alternative_titles", "/3/tv/42/alternative_titles"})
				second := "tv"
				if first == second {
					second = "movie"
				}
				for _, kind := range []string{first, second} {
					close(release[kind])
					got := <-done[kind]
					if got.err != nil || !slices.Equal(got.titles, want[kind]) {
						t.Errorf("%s: aliases=%q err=%v", kind, got.titles, got.err)
					}
				}
				slices.Sort(*calls)
				r2CheckCalls(t, *calls, []string{strings.TrimPrefix(paths[0], "/3") + "?", strings.TrimPrefix(paths[1], "/3") + "?"})
			})
		})
	}
}
