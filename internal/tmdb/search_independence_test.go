package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"golang.org/x/time/rate"
)

const r2Empty = `{"results":[]}`
const r2Movie = `{"results":[{"id":42,"title":"Signal","original_title":"Signal","release_date":"2024-01-01"}]}`
const r2TV = `{"results":[{"id":73,"name":"Signal Returns","original_name":"Signal","first_air_date":"2024-01-01"}]}`
const r2TVDetails = `{"id":73,"name":"Серіал","original_name":"Signal","first_air_date":"2022-01-01","overview":"Опис серіалу"}`

func r2Fixture(reply searchFixtureTransport) (*Client, *[]string) {
	var mu sync.Mutex
	var calls []string
	c := &Client{apiKey: "fixture-key", rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	c.client = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		query.Del("api_key")
		mu.Lock()
		calls = append(calls, strings.TrimPrefix(req.URL.Path, "/3")+"?"+query.Encode())
		mu.Unlock()
		return reply(req)
	})}
	return c, &calls
}

func r2Response(status int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func r2Parsed(year int) ParsedFile {
	return ParsedFile{CleanTitle: "Signal", Year: year, MediaType: MediaTypeMovie, TitleLang: TitleLangLatin}
}

type r2Identity struct {
	typeID MediaType
	id     int
}

// The oracle is attached to the input filename by each fixture. A nil result
// counts as unresolved, including when the fixture expects no valid match.
func r2Check(t *testing.T, filename string, got *MovieInfo, err error, want r2Identity, calls []string) {
	t.Helper()
	var actual r2Identity
	if got != nil {
		actual = r2Identity{got.MediaType, got.TMDBID}
	}
	t.Logf("R2_FILE %s actual=%s:%d oracle=%s:%d calls=%q", filename, actual.typeID, actual.id, want.typeID, want.id, calls)
	if err != nil || actual != want || (want.id == 0 && got != nil) {
		t.Errorf("%s: got %+v, %v; want %+v", filename, got, err, want)
	}
}

func r2CheckCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("HTTP calls = %q; want %q", got, want)
	}
}

func TestR2FolderAfterIndependentMiss(t *testing.T) {
	const movie = "/search/movie?language=en-US&query=Signal"
	const tv = "/search/tv?language=en-US&query=Signal"
	const folder = "/search/movie?language=en-US&query=Beacon&year=2024"
	const details = "/movie/42?append_to_response=credits&language=uk-UA"
	for _, order := range []string{"root_first", "nested_first"} {
		t.Run(order, func(t *testing.T) {
			c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("query") == "Beacon" {
					return r2Response(200, strings.ReplaceAll(r2Movie, "Signal", "Beacon"))
				}
				if req.URL.Path == "/3/movie/42" {
					return r2Response(200, strings.ReplaceAll(fixtureMovieDetails, "Signal", "Beacon"))
				}
				return r2Response(200, r2Empty)
			})
			root := filepath.Join(t.TempDir(), "Beacon (2024)")
			c.SetMediaRoot(root)
			paths := []string{"Signal.mkv", filepath.Join("Beacon (2024)", "Signal.mkv")}
			if order == "nested_first" {
				slices.Reverse(paths)
			}
			for _, path := range paths {
				start := len(*calls)
				parsed := r2Parsed(2024)
				parsed.ParentDir = "Beacon (2024)"
				want := r2Identity{}
				wantCalls := []string{movie + "&year=2024", movie, tv}
				if filepath.Dir(path) != "." {
					want = r2Identity{MediaTypeMovie, 42}
					wantCalls = append(wantCalls, folder, details)
				}
				got, err := c.FetchFromParsed(context.Background(), parsed, filepath.Join(root, path))
				r2Check(t, path, got, err, want, (*calls)[start:])
				r2CheckCalls(t, (*calls)[start:], wantCalls)
			}
		})
	}
}

func TestR2PositiveFileContext(t *testing.T) {
	for _, name := range []string{"shared_identity", "different_context", "year_guard"} {
		t.Run(name, func(t *testing.T) {
			c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/3/search/movie":
					return r2Response(200, r2Movie)
				case "/3/search/tv":
					return r2Response(200, `{"results":[{"id":73,"name":"Signa","original_name":"Outpost","original_language":"ru","popularity":50,"first_air_date":"2024-01-01"}]}`)
				case "/3/tv/73/alternative_titles":
					return r2Response(200, `{"results":[]}`)
				case "/3/movie/42":
					body := fixtureMovieDetails
					if name == "year_guard" {
						return r2Response(200, strings.ReplaceAll(body, "2024", "1999"))
					}
					return r2Response(200, strings.TrimSuffix(body, "}")+`,"poster_path":"/movie.jpg"}`)
				case "/3/tv/73":
					return r2Response(200, strings.TrimSuffix(r2TVDetails, "}")+`,"poster_path":"/tv.jpg"}`)
				case "/t/p/w500/movie.jpg", "/t/p/w500/tv.jpg":
					return r2Response(200, req.URL.Path)
				default:
					t.Errorf("unexpected path %s", req.URL.Path)
					return r2Response(404, "")
				}
			})
			c.postersDir = t.TempDir()
			first := filepath.Join("Alpha", "Signal.mkv")
			second := filepath.Join("Beta", "Signal.mkv")
			if name == "different_context" {
				second = filepath.Join("сезон 2", "Signal.mkv")
			}
			var firstResult *MovieInfo
			for i, path := range []string{first, second} {
				parsed := r2Parsed(2024)
				parsed.ParentDir = filepath.Dir(path)
				want := r2Identity{MediaTypeMovie, 42}
				year, searchTitle, poster, review := "2024", "Signal", "42_Alpha_Signal_mkv.jpg", ""
				wantCalls := []string{"/search/movie?language=en-US&query=Signal&year=2024"}
				if i == 0 {
					wantCalls = append(wantCalls, "/movie/42?append_to_response=credits&language=uk-UA")
				} else {
					poster = "42_Beta_Signal_mkv.jpg"
				}
				switch {
				case name == "year_guard":
					want = r2Identity{}
				case name == "different_context" && i == 1:
					// Identical parser hints are resolved by the existing filename series rule.
					want = r2Identity{MediaTypeTV, 73}
					year, searchTitle, poster, review = "2022", "Signa", "73_______2_Signal_mkv.jpg", "low_verification_score"
					wantCalls = []string{"/search/tv?first_air_date_year=2024&language=en-US&query=Signal", "/tv/73/alternative_titles?", "/tv/73?append_to_response=credits&language=uk-UA", "/t/p/w500/tv.jpg?"}
				default:
					wantCalls = append(wantCalls, "/t/p/w500/movie.jpg?")
				}
				start := len(*calls)
				fetch := c.FetchFromParsed
				if name == "shared_identity" && i == 1 {
					fetch = c.SearchWithFallbacks
				}
				got, err := fetch(context.Background(), parsed, path)
				r2Check(t, path, got, err, want, (*calls)[start:])
				r2CheckCalls(t, (*calls)[start:], wantCalls)
				if got != nil && want.id > 0 {
					if got.Year != year || got.SearchTitle != searchTitle || got.NeedsReview != (review != "") || got.ReviewReason != review || got.LocalPosterPath != filepath.Join(c.postersDir, poster) {
						t.Errorf("%s: unexpected year, search title, review or poster: %+v", path, got)
					}
					if _, err := os.Stat(got.LocalPosterPath); err != nil {
						t.Error(err)
					}
					if got == firstResult {
						t.Error("files share a MovieInfo pointer")
					}
				}
				if i == 0 {
					firstResult = got
				}
			}
		})
	}
}

func TestR2IndependentSearchAfterMissOrError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		attempts int
	}{
		{"cold", 0, "", 0},
		{"success", 200, r2Movie, 1},
		{"empty", 200, r2Empty, 1},
		{"rejected", 200, `{"results":[{"id":9,"title":"Signal","original_title":"Signal","release_date":"1999-01-01"}]}`, 1},
		{"http_500", 500, "fixture failure", 3},
		{"http_404", 404, "", 1},
		{"invalid_json", 200, `{"results":`, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				warm := false
				const dated = "/search/movie?language=en-US&query=Signal&year=2024"
				const undated = "/search/movie?language=en-US&query=Signal"
				const details = "/movie/42?append_to_response=credits&language=uk-UA"
				c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/3/movie/42" {
						return r2Response(200, fixtureMovieDetails)
					}
					if !warm {
						if req.URL.Path == "/3/search/movie" {
							return r2Response(tc.status, tc.body)
						}
						return r2Response(200, r2Empty)
					}
					return r2Response(200, r2Movie)
				})
				var wantCalls []string
				for _, search := range []string{dated, undated} {
					for range tc.attempts {
						wantCalls = append(wantCalls, search)
					}
				}
				wantCalls = append(wantCalls, "/search/tv?language=en-US&query=Signal")
				var firstIdentity r2Identity
				secondCalls := []string{dated, details}
				if tc.name == "success" {
					firstIdentity = r2Identity{MediaTypeMovie, 42}
					wantCalls = []string{dated, details}
					secondCalls = []string{dated}
				}
				if tc.attempts > 0 {
					got, err := c.FetchFromParsed(context.Background(), r2Parsed(2024), "Signal.mkv")
					if tc.status >= 400 || tc.name == "invalid_json" {
						if got != nil || err == nil {
							t.Fatalf("failed TMDB lookup = %v, %v", got, err)
						}
					} else {
						r2Check(t, "Signal.mkv", got, err, firstIdentity, *calls)
					}
					r2CheckCalls(t, *calls, wantCalls)
				}
				start := len(*calls)
				warm = true
				path := filepath.Join("Beta", "Signal.mkv")
				got, err := c.FetchFromParsed(context.Background(), r2Parsed(2024), path)
				r2Check(t, path, got, err, r2Identity{MediaTypeMovie, 42}, (*calls)[start:])
				r2CheckCalls(t, (*calls)[start:], secondCalls)
			})
		})
	}
}

func TestR2LocalAttemptOrder(t *testing.T) {
	const query = "&query=%D0%86%D0%B4%D0%B5%D1%8F"
	const movie = "/search/movie?language="
	const tv = "/search/tv?language="
	want := []string{
		movie + "uk-UA" + query + "&year=2024", movie + "ru-RU" + query + "&year=2024",
		movie + "uk-UA" + query, movie + "ru-RU" + query,
		tv + "uk-UA" + query, tv + "ru-RU" + query,
	}
	c, calls := r2Fixture(func(*http.Request) (*http.Response, error) { return r2Response(200, r2Empty) })
	parsed := ParsedFile{CleanTitle: "Ідея", Year: 2024, MediaType: MediaTypeMovie, TitleLang: TitleLangCyrillic}
	for _, file := range []string{"Ідея.mkv", "Ідея.mp4"} {
		ctx := withSearchAttempts(context.Background())
		start := len(*calls)
		got, err := c.SearchWithFallbacks(ctx, parsed, file)
		r2Check(t, file, got, err, r2Identity{}, (*calls)[start:])
		r2CheckCalls(t, (*calls)[start:], want)
		start = len(*calls)
		if got, err := c.SearchWithFallbacks(ctx, parsed, file); got != nil || err != nil {
			t.Errorf("unexpected result after replay within the same cascade: %+v, %v", got, err)
		}
		r2CheckCalls(t, (*calls)[start:], nil)
	}
}

func TestR2IMDbPrecedenceAndMissFallback(t *testing.T) {
	const dated = "/search/movie?language=en-US&query=Signal&year=2024"
	const movieDetails = "/movie/42?append_to_response=credits&language=uk-UA"
	const tvDetails = "/tv/73?append_to_response=credits&language=uk-UA"
	c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/3/search/movie":
			return r2Response(200, r2Movie)
		case "/3/movie/42":
			return r2Response(200, fixtureMovieDetails)
		case "/3/find/tt1234567":
			return r2Response(200, `{"tv_results":[{"id":73}]}`)
		case "/3/find/tt7654321":
			return r2Response(200, `{"movie_results":[],"tv_results":[]}`)
		case "/3/tv/73":
			return r2Response(200, r2TVDetails)
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			return r2Response(404, "")
		}
	})
	for _, tc := range []struct {
		filename, imdb string
		want           r2Identity
		wire           []string
	}{
		{"Signal.mkv", "", r2Identity{MediaTypeMovie, 42}, []string{dated, movieDetails}},
		{"Signal.tt1234567.mkv", "tt1234567", r2Identity{MediaTypeTV, 73}, []string{"/find/tt1234567?external_source=imdb_id&language=uk-UA", tvDetails}},
		{"Signal.tt7654321.mkv", "tt7654321", r2Identity{MediaTypeMovie, 42}, []string{"/find/tt7654321?external_source=imdb_id&language=uk-UA", dated}},
	} {
		start := len(*calls)
		parsed := r2Parsed(2024)
		parsed.IMDBID = tc.imdb
		got, err := c.FetchFromParsed(context.Background(), parsed, tc.filename)
		r2Check(t, tc.filename, got, err, tc.want, (*calls)[start:])
		r2CheckCalls(t, (*calls)[start:], tc.wire)
	}
}

type r2CancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r r2CancelOnClose) Close() error {
	r.cancel()
	return r.ReadCloser.Close()
}

func TestR2Cancellation(t *testing.T) {
	for _, phase := range []string{"before_search", "between_attempts", "retry_wait", "before_details"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent := context.Background()
				ctx, cancel := context.WithCancel(parent)
				defer cancel()
				started := make(chan struct{})
				c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/3/movie/42" {
						return r2Response(200, fixtureMovieDetails)
					}
					if ctx.Err() != nil {
						return r2Response(200, r2Movie)
					}
					switch phase {
					case "retry_wait":
						close(started)
						return r2Response(500, "fixture failure")
					case "between_attempts":
						resp, _ := r2Response(200, r2Empty)
						resp.Body = r2CancelOnClose{resp.Body, cancel}
						return resp, nil
					default:
						resp, _ := r2Response(200, r2Movie)
						resp.Body = r2CancelOnClose{resp.Body, cancel}
						return resp, nil
					}
				})
				if phase == "before_search" {
					cancel()
				}
				done := make(chan error, 1)
				go func() {
					got, err := c.FetchFromParsed(ctx, r2Parsed(2024), "Signal.mkv")
					if got != nil {
						t.Errorf("canceled call returned %+v", got)
					}
					done <- err
				}()
				if phase == "retry_wait" {
					<-started
					synctest.Wait()
					cancel()
				}
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v; want context.Canceled", err)
				}
				const dated = "/search/movie?language=en-US&query=Signal&year=2024"
				if phase == "before_search" {
					r2CheckCalls(t, *calls, nil)
				} else {
					r2CheckCalls(t, *calls, []string{dated})
				}
				t.Logf("R2_CANCEL calls=%q", *calls)
				start := len(*calls)
				got, err := c.FetchFromParsed(parent, r2Parsed(2024), "Signal.mkv")
				r2Check(t, "Signal.mkv", got, err, r2Identity{MediaTypeMovie, 42}, (*calls)[start:])
				r2CheckCalls(t, (*calls)[start:], []string{dated, "/movie/42?append_to_response=credits&language=uk-UA"})
			})
		})
	}
}

func TestR2ParallelFileContexts(t *testing.T) {
	type result struct {
		info *MovieInfo
		err  error
	}
	for _, first := range []MediaType{MediaTypeMovie, MediaTypeTV} {
		t.Run(string(first)+"_finishes_first", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan MediaType, 2)
				release := map[MediaType]chan struct{}{MediaTypeMovie: make(chan struct{}), MediaTypeTV: make(chan struct{})}
				done := map[MediaType]chan result{MediaTypeMovie: make(chan result, 1), MediaTypeTV: make(chan result, 1)}
				paths := map[MediaType]string{MediaTypeMovie: filepath.Join("Alpha", "Signal.mkv"), MediaTypeTV: filepath.Join("сезон 2", "Signal.mkv")}
				c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
					switch req.URL.Path {
					case "/3/search/movie":
						entered <- MediaTypeMovie
						<-release[MediaTypeMovie]
						return r2Response(200, r2Movie)
					case "/3/search/tv":
						entered <- MediaTypeTV
						<-release[MediaTypeTV]
						return r2Response(200, strings.ReplaceAll(r2TV, "2024", "2022"))
					case "/3/movie/42":
						return r2Response(200, fixtureMovieDetails)
					case "/3/tv/73":
						return r2Response(200, r2TVDetails)
					default:
						return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
					}
				})
				parent := context.Background()
				for kind, path := range paths {
					go func() {
						parsed := r2Parsed(2024)
						if kind == MediaTypeTV {
							parsed.Year = 2022
						}
						parsed.ParentDir = filepath.Dir(path)
						got, err := c.FetchFromParsed(parent, parsed, path)
						done[kind] <- result{got, err}
					}()
				}
				if a, b := <-entered, <-entered; a == b {
					t.Errorf("both searches used %s", a)
				}
				second := MediaTypeTV
				if first == second {
					second = MediaTypeMovie
				}
				close(release[first])
				a := <-done[first]
				close(release[second])
				b := <-done[second]
				ids := map[MediaType]int{MediaTypeMovie: 42, MediaTypeTV: 73}
				r2Check(t, paths[first], a.info, a.err, r2Identity{first, ids[first]}, nil)
				r2Check(t, paths[second], b.info, b.err, r2Identity{second, ids[second]}, nil)
				t.Logf("R2_PARALLEL calls=%q", *calls)
				if a.info == b.info || len(*calls) != 4 || (*calls)[2] != fmt.Sprintf("/%s/%d?append_to_response=credits&language=uk-UA", first, ids[first]) {
					t.Errorf("unexpected results or HTTP order: %v", *calls)
				}
			})
		})
	}
}
