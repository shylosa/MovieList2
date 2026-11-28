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
	"time"

	"golang.org/x/time/rate"
)

func TestR4AutomaticOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, want                 string
		search, details, find, alt int
	}{
		{"empty", "NO_MATCH", 3, 0, 0, 0},
		{"wrong_year", "NO_MATCH", 1, 1, 0, 0},
		{"weak_title", "NO_MATCH", 3, 0, 0, 2},
		{"outage", "TMDB_ERROR", 9, 0, 0, 0},
		{"partial_outage", "TMDB_ERROR", 5, 0, 0, 0},
		{"partial_success", "movie:42", 4, 1, 0, 0},
		{"network", "TMDB_ERROR", 5, 0, 0, 0},
		{"malformed", "TMDB_ERROR", 5, 0, 0, 0},
		{"partial_json", "TMDB_ERROR", 5, 0, 0, 0},
		{"missing_results", "TMDB_ERROR", 5, 0, 0, 0},
		{"search_movie_404", "TMDB_ERROR", 3, 0, 0, 0},
		{"search_tv_404", "TMDB_ERROR", 3, 0, 0, 0},
		{"movie_details_404", "TMDB_ERROR", 3, 3, 0, 0},
		{"tv_details_404", "TMDB_ERROR", 3, 3, 0, 0},
		{"details_mismatch", "TMDB_ERROR", 3, 1, 0, 0},
		{"details_recovery", "tv:73", 3, 2, 0, 0},
		{"alias_failure", "TMDB_ERROR", 3, 0, 0, 1},
		{"alias_invalid", "TMDB_ERROR", 3, 0, 0, 3},
		{"imdb_found", "tv:73", 0, 1, 1, 0},
		{"imdb_empty", "NO_MATCH", 3, 0, 1, 0},
		{"imdb_404", "TMDB_ERROR", 3, 0, 1, 0},
		{"imdb_outage", "TMDB_ERROR", 3, 0, 3, 0},
		{"imdb_title_recovery", "movie:42", 1, 1, 1, 0},
		{"language_recovery", "movie:42", 4, 1, 0, 0},
		{"language_error_year_rejected", "TMDB_ERROR", 7, 1, 0, 0},
		{"language_details_recovery", "movie:43", 2, 2, 0, 0},
		{"transliteration_recovery", "movie:42", 7, 1, 0, 0},
		{"folder_transliteration_recovery", "movie:42", 8, 1, 0, 0},
		{"poster_error", "movie:42", 1, 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parsed := r2Parsed(2024)
				path := "Signal.mkv"
				if strings.HasPrefix(tc.name, "imdb_") {
					parsed.IMDBID = "tt1234567"
				}
				if strings.HasPrefix(tc.name, "language_") || tc.name == "transliteration_recovery" {
					parsed.CleanTitle, parsed.TitleLang = "Сигнал", TitleLangCyrillic
				}
				if tc.name == "folder_transliteration_recovery" {
					parsed.CleanTitle = "Opaque"
					parsed.ParentDir = "Сигнал"
					path = "Opaque.mkv"
				}
				fixed := false
				c, calls := r2Fixture(func(r *http.Request) (*http.Response, error) {
					endpoint := r.URL.Path
					q := r.URL.Query()
					dated := q.Get("year") != ""
					uk := q.Get("language") == "uk-UA"
					switch endpoint {
					case "/3/find/tt1234567":
						switch tc.name {
						case "imdb_found":
							return r2Response(200, `{"movie_results":[],"tv_results":[{"id":73}]}`)
						case "imdb_empty":
							return r2Response(200, `{"movie_results":[],"tv_results":[]}`)
						case "imdb_outage":
							return r2Response(500, "")
						default:
							return r2Response(404, "")
						}
					case "/3/search/movie", "/3/search/tv":
						movie := endpoint == "/3/search/movie"
						switch {
						case fixed:
							return r2Response(200, r2Movie)
						case tc.name == "outage" || (dated && slices.Contains([]string{"partial_outage", "partial_success"}, tc.name)):
							return r2Response(500, "fixture outage")
						case tc.name == "network" && dated:
							return nil, errors.ErrUnsupported
						case tc.name == "malformed" && dated:
							return r2Response(200, `{"results":`)
						case tc.name == "partial_json" && dated:
							return r2Response(200, `{"results":[{"id":42,"title":"Signal","popularity":"invalid"}]}`)
						case tc.name == "missing_results" && dated:
							return r2Response(200, `{}`)
						case (tc.name == "search_movie_404" && dated) || (tc.name == "search_tv_404" && !movie):
							return r2Response(404, "")
						case tc.name == "language_recovery" && dated && uk:
							return r2Response(500, "")
						case tc.name == "language_error_year_rejected" && dated && uk:
							return r2Response(500, "")
						case tc.name == "transliteration_recovery" && uk && dated, tc.name == "folder_transliteration_recovery" && q.Get("query") == "Сигнал" && uk:
							return r2Response(500, "")
						case (tc.name == "language_recovery" || tc.name == "language_error_year_rejected") && dated && q.Get("query") == "Сигнал", tc.name == "language_details_recovery" && movie && q.Get("query") == "Сигнал":
							id := 42
							if tc.name == "language_details_recovery" && !uk {
								id = 43
							}
							return r2Response(200, fmt.Sprintf(`{"results":[{"id":%d,"title":"Сигнал","original_title":"Сигнал","release_date":"2024-01-01"}]}`, id))
						case (tc.name == "transliteration_recovery" || tc.name == "folder_transliteration_recovery") && strings.EqualFold(q.Get("query"), "Signal"):
							return r2Response(200, r2Movie)
						case tc.name == "weak_title" && movie, (tc.name == "alias_failure" || tc.name == "alias_invalid") && dated:
							return r2Response(200, `{"results":[{"id":42,"title":"ZZZ","release_date":"2024-01-01"}]}`)
						case !movie && (tc.name == "tv_details_404" || tc.name == "details_recovery"):
							return r2Response(200, r2TV)
						case dated && slices.Contains([]string{"wrong_year", "movie_details_404", "details_mismatch", "details_recovery", "imdb_title_recovery", "poster_error"}, tc.name),
							tc.name == "partial_success" && movie:
							return r2Response(200, r2Movie)
						default:
							return r2Response(200, r2Empty)
						}
					case "/3/movie/42", "/3/movie/43":
						switch tc.name {
						case "wrong_year", "language_error_year_rejected":
							return r2Response(200, strings.ReplaceAll(fixtureMovieDetails, "2024", "1999"))
						case "movie_details_404":
							return r2Response(404, "")
						case "details_mismatch", "details_recovery":
							return r2Response(200, strings.ReplaceAll(fixtureMovieDetails, `"id":42`, `"id":99`))
						case "language_details_recovery":
							return r2Response(200, strings.ReplaceAll(fixtureMovieDetails, `"id":42`, `"id":43`))
						case "poster_error":
							return r2Response(200, strings.TrimSuffix(fixtureMovieDetails, "}")+`,"poster_path":"/fixture.jpg"}`)
						default:
							return r2Response(200, fixtureMovieDetails)
						}
					case "/3/tv/73":
						if tc.name == "tv_details_404" {
							return r2Response(404, "")
						}
						return r2Response(200, r2TVDetails)
					case "/3/movie/42/alternative_titles":
						if tc.name == "alias_failure" {
							return r2Response(404, "")
						}
						if tc.name == "alias_invalid" {
							return r2Response(200, `{}`)
						}
						return r2Response(200, `{"titles":[]}`)
					case "/t/p/w500/fixture.jpg":
						return r2Response(500, "")
					default:
						t.Errorf("unexpected path %s", endpoint)
						return r2Response(404, "")
					}
				})
				got, err := c.FetchFromParsed(t.Context(), parsed, path)
				result := "NO_MATCH"
				if err != nil {
					result = "TMDB_ERROR"
				}
				if got != nil {
					result = fmt.Sprintf("%s:%d", got.MediaType, got.TMDBID)
				}
				metrics := c.RequestMetrics()
				find, aliases, repeats, posters := 0, 0, 0, 0
				previous := ""
				for _, call := range *calls {
					if call == previous {
						repeats++
					}
					previous = call
					switch {
					case strings.HasPrefix(call, "/find/"):
						find++
					case strings.Contains(call, "/alternative_titles"):
						aliases++
					case strings.HasPrefix(call, "/t/p/"):
						posters++
					}
				}
				t.Logf("R4 %s outcome=%s search=%d details=%d find=%d alias=%d retries=%d poster=%d err=%v", tc.name, result, metrics.SearchCalls, metrics.DetailsCalls, find, aliases, repeats, posters, err)
				if result != tc.want || got != nil && err != nil {
					t.Errorf("outcome %s, want %s", result, tc.want)
				}
				if metrics.SearchCalls != int64(tc.search) || metrics.DetailsCalls != int64(tc.details) || find != tc.find || aliases != tc.alt {
					t.Errorf("HTTP budget: %q", *calls)
				}
				if strings.HasSuffix(tc.name, "_404") || tc.name == "alias_failure" {
					if !errors.Is(err, ErrNotFound) {
						t.Errorf("lost ErrNotFound: %v", err)
					}
				}
				if tc.name == "details_mismatch" && !errors.Is(err, errDetailsIdentity) {
					t.Errorf("lost R3 identity error: %v", err)
				}
				if tc.name == "network" && !errors.Is(err, errors.ErrUnsupported) {
					t.Errorf("lost transport error: %v", err)
				}
				if slices.Contains([]string{"malformed", "partial_json", "missing_results"}, tc.name) {
					fixed = true
					info, err := c.FetchByCleanTitle(t.Context(), "Signal", "2024", MediaTypeMovie)
					if err != nil || info == nil || info.TMDBID != 42 {
						t.Errorf("independent lookup after recovery: %+v, %v", info, err)
					}
				}
			})
		})
	}
}

func TestR4AutomaticCancellation(t *testing.T) {
	for _, mode := range []string{"before_search", "rate_limit", "retry_backoff", "between_languages", "before_details", "failed_endpoint"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				c, calls := r2Fixture(func(r *http.Request) (*http.Response, error) {
					switch mode {
					case "between_languages", "before_details":
						body := r2Empty
						if mode == "before_details" {
							body = r2Movie
						}
						resp, _ := r2Response(200, body)
						resp.Body = r2CancelOnClose{resp.Body, cancel}
						return resp, nil
					case "failed_endpoint":
						cancel()
						return r2Response(404, "")
					default:
						return r2Response(500, "")
					}
				})
				want := 1
				switch mode {
				case "before_search":
					cancel()
					want = 0
				case "rate_limit":
					c.rateLimiter = rate.NewLimiter(rate.Every(time.Second), 1)
					c.rateLimiter.Allow()
					time.AfterFunc(time.Millisecond, cancel)
					want = 0
				case "retry_backoff":
					time.AfterFunc(time.Millisecond, cancel)
				}
				parsed := r2Parsed(2024)
				if mode == "between_languages" {
					parsed.CleanTitle, parsed.TitleLang = "Сигнал", TitleLangCyrillic
				}
				info, err := c.FetchFromParsed(ctx, parsed, "Signal.mkv")
				t.Logf("R4 cancel/%s search=%d outcome=%v", mode, len(*calls), err)
				if info != nil || !errors.Is(err, context.Canceled) || len(*calls) != want {
					t.Errorf("got %v, %v, calls %q; want canceled and %d calls", info, err, *calls, want)
				}
			})
		})
	}
}
