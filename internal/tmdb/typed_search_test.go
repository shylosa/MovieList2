package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/time/rate"
)

type searchFixtureTransport func(*http.Request) (*http.Response, error)

func (f searchFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newSearchFixture(t *testing.T, reply func(string) (int, string)) (*Client, *[]string) {
	t.Helper()
	var calls []string
	transport := searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		values := req.URL.Query()
		if req.Method != http.MethodGet || req.URL.Host != "api.themoviedb.org" || values.Get("api_key") != "fixture-key" {
			t.Error("unexpected method, host or fixture API key")
		}
		t.Logf("R1_WIRE %s %s %s?%s", t.Name(), req.Method, req.URL.EscapedPath(), strings.ReplaceAll(req.URL.RawQuery, "api_key=fixture-key", "api_key=REDACTED"))
		values.Set("api_key", "REDACTED")
		call := strings.TrimPrefix(req.URL.Path, "/3") + "?" + values.Encode()
		calls = append(calls, call)
		status, body := reply(call)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return &Client{
		apiKey: "fixture-key", client: &http.Client{Transport: transport},
		rateLimiter: rate.NewLimiter(rate.Inf, 1),
	}, &calls
}

func assertSearchFixture(t *testing.T, got, want []string, result any, err error) {
	t.Helper()
	outcome, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	t.Logf("R1 %s count=%d requests=%q result=%s error=%q", t.Name(), len(got), got, outcome, fmt.Sprint(err))
	if !slices.Equal(got, want) {
		t.Fatalf("HTTP requests = %q; want %q", got, want)
	}
}

const fixtureMovieDetails = `{"id":42,"title":"Фільм","original_title":"Signal","release_date":"2024-01-01","overview":"Опис фільму"}`
const fixtureTVDetails = `{"id":42,"name":"Серіал","original_name":"Signal","first_air_date":"2024-01-01","overview":"Опис серіалу"}`
const fixtureMovieDetailsCall = "/movie/42?api_key=REDACTED&append_to_response=credits&language=uk-UA"
const fixtureTVDetailsCall = "/tv/42?api_key=REDACTED&append_to_response=credits&language=uk-UA"

func TestTypedSearchBaselineAutomatic(t *testing.T) {
	t.Run("movie_no_query_year_keeps_target_year", func(t *testing.T) {
		const search = "/search/movie?api_key=REDACTED&language=en-US&query=Signal"
		c, calls := newSearchFixture(t, func(call string) (int, string) {
			switch call {
			case search:
				return 200, `{"results":[{"id":41,"title":"Signal","original_title":"Signal","release_date":"2025-01-01","popularity":100},{"id":42,"title":"Signal","original_title":"Signal","release_date":"2024-01-01"}]}`
			case fixtureMovieDetailsCall:
				return 200, fixtureMovieDetails
			default:
				return 200, `{"results":[]}`
			}
		})
		got, err := c.SearchWithFallbacks(context.Background(), ParsedFile{CleanTitle: "Signal", Year: 2024, MediaType: MediaTypeMovie}, "Signal.mkv")
		assertSearchFixture(t, *calls, []string{search + "&year=2024", search, fixtureMovieDetailsCall}, got, err)
		if err != nil || got == nil || got.TMDBID != 42 || got.MediaType != MediaTypeMovie {
			t.Fatalf("expected movie:42, got %+v, %v", got, err)
		}
	})

	t.Run("tv_ukrainian_early_exit", func(t *testing.T) {
		const search = "/search/tv?api_key=REDACTED&first_air_date_year=2024&language=uk-UA&query=%D0%86%D0%B4%D0%B5%D1%8F"
		c, calls := newSearchFixture(t, func(call string) (int, string) {
			switch call {
			case search:
				return 200, `{"results":[{"id":42,"name":"Ідея","original_name":"Ідея","first_air_date":"2024-01-01"}]}`
			case fixtureTVDetailsCall:
				return 200, strings.Replace(fixtureTVDetails, "Signal", "Ідея", 1)
			default:
				return 404, ""
			}
		})
		got, err := c.searchAndFetch(context.Background(), "Ідея", 2024, 2024, MediaTypeTV, "", ParsedFile{})
		assertSearchFixture(t, *calls, []string{search, fixtureTVDetailsCall}, got, err)
		if err != nil || got == nil || got.TMDBID != 42 || got.MediaType != MediaTypeTV {
			t.Fatalf("expected tv:42, got %+v, %v", got, err)
		}
	})

	t.Run("unknown_type_and_cyrillic_url", func(t *testing.T) {
		const query = "&query=%D0%81%D0%B6+%2B+A%26B%3F%2F%23"
		c, calls := newSearchFixture(t, func(string) (int, string) { return 200, `{"results":[]}` })
		got, err := c.searchAndFetch(context.Background(), "Ёж + A&B?/#", 2024, 2024, "", "", ParsedFile{})
		assertSearchFixture(t, *calls, []string{
			"/search/movie?api_key=REDACTED&language=uk-UA" + query + "&year=2024",
			"/search/tv?api_key=REDACTED&first_air_date_year=2024&language=uk-UA" + query,
			"/search/movie?api_key=REDACTED&language=ru-RU" + query + "&year=2024",
			"/search/tv?api_key=REDACTED&first_air_date_year=2024&language=ru-RU" + query,
		}, got, err)
		if err != nil || got != nil {
			t.Fatalf("expected a clean miss, got %+v, %v", got, err)
		}
	})
}

func TestTypedSearchBaselineExact(t *testing.T) {
	const movie = "/search/movie?api_key=REDACTED&language=en-US&query=Signal"
	const tv = "/search/tv?api_key=REDACTED&language=en-US&query=Signal"
	const alias = "/tv/42/alternative_titles?api_key=REDACTED"
	for _, tt := range []struct {
		name       string
		year       int
		preferred  MediaType
		strict     bool
		wantType   MediaType
		wantAlias  string
		wantReview string
		wantCalls  []string
	}{
		{"open_year_beats_preferred_type", 2024, MediaTypeMovie, false, MediaTypeTV, "Signal", "media_type_conflict", []string{movie, tv, alias, fixtureTVDetailsCall}},
		{"open_type_breaks_tie", 0, MediaTypeTV, false, MediaTypeTV, "Signal", "", []string{movie, tv, alias, fixtureTVDetailsCall}},
		{"strict_movie_accepts_exact_despite_year", 2024, MediaTypeMovie, true, MediaTypeMovie, "", "year_conflict", []string{movie, fixtureMovieDetailsCall}},
		{"strict_tv_accepts_alias", 2024, MediaTypeTV, true, MediaTypeTV, "Signal", "", []string{tv, alias, fixtureTVDetailsCall}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, calls := newSearchFixture(t, func(call string) (int, string) {
				switch call {
				case movie:
					return 200, `{"results":[{"id":42,"title":"Signal","original_title":"Signal","release_date":"1999-01-01"}]}`
				case tv:
					return 200, `{"results":[{"id":42,"name":"Other Title","first_air_date":"2024-01-01"}]}`
				case alias:
					return 200, `{"results":[{"title":"Signal"}]}`
				case fixtureMovieDetailsCall:
					return 200, strings.Replace(fixtureMovieDetails, "2024", "1999", 1)
				case fixtureTVDetailsCall:
					return 200, strings.Replace(fixtureTVDetails, "Signal", "Other Title", 1)
				default:
					return 404, ""
				}
			})
			got, err := c.SearchExactTitle(context.Background(), "Signal", tt.year, tt.preferred, tt.strict, "")
			assertSearchFixture(t, *calls, tt.wantCalls, got, err)
			if err != nil || got == nil || got.TMDBID != 42 || got.MediaType != tt.wantType ||
				got.MatchedAlias != tt.wantAlias || got.ReviewReason != tt.wantReview || got.NeedsReview != (tt.wantReview != "") {
				t.Fatalf("unexpected exact result %+v, %v", got, err)
			}
		})
	}
	t.Run("unverified_fuzzy_title_is_rejected", func(t *testing.T) {
		const movieAlias = "/movie/42/alternative_titles?api_key=REDACTED"
		c, calls := newSearchFixture(t, func(call string) (int, string) {
			if call == movie {
				return 200, `{"results":[{"id":42,"title":"Signal Lost","release_date":"2024-01-01"}]}`
			}
			return 200, `{"titles":[{"title":"Other Title"}]}`
		})
		got, err := c.SearchExactTitle(context.Background(), "Signal", 2024, MediaTypeMovie, true, "")
		assertSearchFixture(t, *calls, []string{movie, movieAlias}, got, err)
		if err != nil || got != nil {
			t.Fatalf("expected a clean miss, got %+v, %v", got, err)
		}
	})
}

func TestTypedSearchBaselineCandidates(t *testing.T) {
	const query = "&query=Zhertva+obstoyatelstv"
	const cyrillic = "&query=%D0%96%D0%B5%D1%80%D1%82%D0%B2%D0%B0+%D0%BE%D0%B1%D1%81%D1%82%D0%BE%D1%8F%D1%82%D0%B5%D0%BB%D1%8C%D1%81%D1%82%D0%B2"
	const movie = "/search/movie?api_key=REDACTED&language="
	const tv = "/search/tv?api_key=REDACTED&language="
	c, calls := newSearchFixture(t, func(call string) (int, string) {
		switch call {
		case movie + "en-US" + query:
			return 200, `{"results":[{"id":9,"title":"Zhertva obstoyatelstv","release_date":"2000-01-01","popularity":100}]}`
		case movie + "en-US" + cyrillic:
			return 200, `{"results":[{"id":42,"title":"Жертва обстоятельств","release_date":"2024-01-01","popularity":5},{"id":13,"title":"Жертва обстоятельств","release_date":"2024-01-01","popularity":1},{"id":12,"title":"Жертва обстоятельств","release_date":"2024-01-01","popularity":1},{"id":8,"title":"Other","release_date":"2024-01-01","popularity":900}]}`
		case tv + "en-US" + cyrillic:
			return 200, `{"results":[{"id":42,"name":"Жертва обстоятельств","first_air_date":"2024-01-01","popularity":5},{"id":4,"name":"Жертва обстоятельств","first_air_date":"2025-01-01","popularity":700}]}`
		default:
			return 200, `{"results":[]}`
		}
	})
	got, err := c.SearchCandidates(context.Background(), "Zhertva obstoyatelstv", 2024, "auto")
	assertSearchFixture(t, *calls, []string{
		movie + "en-US" + query, tv + "en-US" + query,
		movie + "uk-UA" + cyrillic, tv + "uk-UA" + cyrillic,
		movie + "ru-RU" + cyrillic, tv + "ru-RU" + cyrillic,
		movie + "en-US" + cyrillic, tv + "en-US" + cyrillic,
	}, got, err)
	var pairs []string
	for _, item := range got {
		pairs = append(pairs, fmt.Sprintf("%s:%d", item.MediaType, item.TMDBID))
	}
	want := []string{"tv:42", "movie:42", "movie:12", "movie:13", "tv:4"}
	if err != nil || !slices.Equal(pairs, want) {
		t.Fatalf("candidate order = %v, %v; want %v", pairs, err, want)
	}
}

func TestTypedSearchBaselineHTTP(t *testing.T) {
	const search = "/search/movie?api_key=REDACTED&language=en-US&query=Signal"
	t.Run("retry_invalid_json_then_decode", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			attempt := 0
			c, calls := newSearchFixture(t, func(string) (int, string) {
				attempt++
				if attempt == 1 {
					return 200, `{"results":`
				}
				return 200, `{"results":[{"id":42,"title":"Signal","release_date":"2024-01-01"}]}`
			})
			got, err := c.SearchCandidates(context.Background(), "Signal", 2024, "movie")
			assertSearchFixture(t, *calls, []string{search, search}, got, err)
			if err != nil || len(got) != 1 || got[0].TMDBID != 42 {
				t.Fatalf("expected decoded movie:42 after retry, got %+v, %v", got, err)
			}
		})
	})
	t.Run("not_found_is_returned_without_retry", func(t *testing.T) {
		c, calls := newSearchFixture(t, func(string) (int, string) { return 404, "" })
		got, err := c.SearchExactTitle(context.Background(), "Signal", 0, MediaTypeMovie, false, "")
		assertSearchFixture(t, *calls, []string{search}, got, err)
		if got != nil || !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %+v, %v", got, err)
		}
	})
	t.Run("exhausted_retry_returns_http_error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c, calls := newSearchFixture(t, func(string) (int, string) { return 500, "fixture failure" })
			got, err := c.SearchCandidates(context.Background(), "Signal", 2024, "movie")
			assertSearchFixture(t, *calls, []string{search, search, search}, got, err)
			if got != nil || err == nil || err.Error() != "TMDB HTTP 500" {
				t.Fatalf("expected HTTP 500 after retry exhaustion, got %+v, %v", got, err)
			}
		})
	})
	t.Run("cancel_during_backoff", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(500*time.Millisecond, cancel)
			c, calls := newSearchFixture(t, func(string) (int, string) { return 500, "fixture failure" })
			got, err := c.SearchExactTitle(ctx, "Signal", 0, MediaTypeMovie, false, "")
			assertSearchFixture(t, *calls, []string{search}, got, err)
			if got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %+v, %v", got, err)
			}
		})
	})
}
