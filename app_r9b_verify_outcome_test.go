package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

// R9-B: явний результат AI→TMDB-верифікації.
// mergeGeminiWithTMDB(ForType) і rescueEmptyGeminiWithFolder повертають
// (storage.Movie, error):
//
//	TmdbID > 0, err == nil     → FOUND
//	TmdbID == 0, err == nil    → UNVERIFIED
//	err != nil (не Canceled)   → TMDB_ERROR
//	errors.Is(err, Canceled)   → CANCELED

// r9bRecorder пише wire TMDB у порядку запитів (без api_key) і делегує handler.
type r9bRecorder struct {
	mu    sync.Mutex
	calls []string
}

type r9bResp struct {
	status int
	body   string
	err    error
}

func (r *r9bRecorder) roundTrip(handler func(*http.Request) r9bResp) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		q.Del("api_key")
		r.mu.Lock()
		r.calls = append(r.calls, req.Method+" "+req.URL.Path+"?"+q.Encode())
		r.mu.Unlock()
		resp := handler(req)
		if resp.err != nil {
			return nil, resp.err
		}
		return &http.Response{
			StatusCode: resp.status,
			Body:       io.NopCloser(strings.NewReader(resp.body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
}

func (r *r9bRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *r9bRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func r9bMergeApp(t *testing.T, handler func(*http.Request) r9bResp) (*App, *r9bRecorder, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{MediaFolderPath: root, TMDBAPIKey: "fixture", PostersDir: t.TempDir()}
	client := tmdb.NewClient(cfg)
	t.Cleanup(client.Close)
	rec := &r9bRecorder{}
	client.SetTransport(rec.roundTrip(handler))
	a := NewApp()
	a.ctx = context.Background()
	a.cfg = cfg
	a.tmdbClient = client
	a.eventEmitter = func(context.Context, string, ...interface{}) {}
	return a, rec, root
}

func r9bRec(title string, year int, mediaType string, confidence float64) ai.RecognizedTitle {
	rec := ai.RecognizedTitle{ENTitle: title, MediaType: mediaType, Status: "resolved", Confidence: confidence}
	if year > 0 {
		rec.Year = &year
	}
	return rec
}

func r9bOK(body string) r9bResp { return r9bResp{status: 200, body: body} }

func r9bOutage() r9bResp { return r9bResp{status: 500, body: "fixture outage"} }

// Каскад merge для EN-гіпотези "Alpha Origin" (рік 2024, movie, non-strict):
// exact movie+tv (en-US, без року) → FetchByCleanTitle movie (movie+рік, movie, tv opposite)
// → TV retry (tv+рік, tv, movie opposite) → details обраного кандидата.
func TestR9BMergeVerificationOutcomes(t *testing.T) {
	const title = "Alpha Origin"
	movieResult := func(id int, release string) string {
		return fmt.Sprintf(`{"results":[{"id":%d,"title":%q,"original_title":%q,"release_date":%q}]}`, id, title, title, release)
	}
	movieDetails := func(id int, origTitle string) string {
		return fmt.Sprintf(`{"id":%d,"title":"Українська назва","original_title":%q,"release_date":"2024-01-01","overview":"Український опис фільму.","genres":[{"name":"Drama"}]}`, id, origTitle)
	}
	tvDetails := func(id int, origName string) string {
		return fmt.Sprintf(`{"id":%d,"name":"Серіал","original_name":%q,"first_air_date":"2024-03-05","overview":"Український опис серіалу.","genres":[{"name":"Drama"}]}`, id, origName)
	}
	empty := `{"results":[]}`

	t.Run("01_found", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			switch p := r.URL.Path; {
			case p == "/3/search/movie":
				return r9bOK(movieResult(101, "2024-01-01"))
			case p == "/3/search/tv":
				return r9bOK(empty)
			case p == "/3/movie/101":
				return r9bOK(movieDetails(101, title))
			default:
				return r9bResp{status: 404, body: "{}"}
			}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if err != nil || movie.TmdbID != 101 {
			t.Fatalf("FOUND: movie=%+v err=%v, want TmdbID=101 err=nil", movie, err)
		}
		if movie.MediaType != "movie" || movie.TitleEN != title || movie.RecognitionSource != "gemini" ||
			movie.RecognitionConfidence != 0.98 || movie.VerificationScore < 0.85 || movie.NeedsReview {
			t.Fatalf("FOUND fields: %+v", movie)
		}
		t.Logf("R9B 01_found calls=%d wire=%v", rec.count(), rec.snapshot())
	})

	t.Run("02_unverified_empty_title", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp { return r9bOutage() })
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec("", 2024, "movie", 0.98))
		if err != nil || movie.TmdbID != 0 {
			t.Fatalf("UNVERIFIED: movie=%+v err=%v, want TmdbID=0 err=nil", movie, err)
		}
		if rec.count() != 0 {
			t.Fatalf("UNVERIFIED empty title must not call TMDB, calls=%v", rec.snapshot())
		}
	})

	t.Run("03_unverified_low_confidence", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp { return r9bOutage() })
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.30))
		if err != nil || movie.TmdbID != 0 {
			t.Fatalf("UNVERIFIED: movie=%+v err=%v, want TmdbID=0 err=nil", movie, err)
		}
		if rec.count() != 0 {
			t.Fatalf("low confidence must not call TMDB, calls=%v", rec.snapshot())
		}
	})

	t.Run("04_unverified_year_conflict", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			switch p := r.URL.Path; {
			case p == "/3/search/movie":
				return r9bOK(movieResult(101, "2024-01-01"))
			case p == "/3/movie/101":
				return r9bOK(movieDetails(101, title))
			default:
				return r9bOK(empty)
			}
		})
		// AI-рік 1999 проти файла 2024 і TMDB 2024 — гіпотеза відхиляється без помилки.
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 1999, "movie", 0.98))
		if err != nil || movie.TmdbID != 0 {
			t.Fatalf("UNVERIFIED year: movie=%+v err=%v, want TmdbID=0 err=nil", movie, err)
		}
		t.Logf("R9B 04_unverified_year calls=%d", rec.count())
	})

	t.Run("05_unverified_type_conflict", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			switch p := r.URL.Path; {
			case p == "/3/search/movie":
				return r9bOK(movieResult(300, "2024-01-01"))
			case p == "/3/movie/300":
				// JW("Alpha Origin","Alpha Origins")=0.9846 < 0.99, тип movie ≠ tv → відхилення.
				return r9bOK(movieDetails(300, "Alpha Origins"))
			default:
				return r9bOK(empty)
			}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "tv", 0.98))
		if err != nil || movie.TmdbID != 0 {
			t.Fatalf("UNVERIFIED type: movie=%+v err=%v, want TmdbID=0 err=nil", movie, err)
		}
		t.Logf("R9B 05_unverified_type calls=%d", rec.count())
	})

	t.Run("06_unverified_clean_no_match", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if err != nil || movie.TmdbID != 0 {
			t.Fatalf("UNVERIFIED no_match: movie=%+v err=%v, want TmdbID=0 err=nil", movie, err)
		}
		// exact movie+tv, FetchByCleanTitle movie (3), TV retry (3) — без details.
		if got := rec.count(); got != 8 {
			t.Fatalf("clean miss wire calls=%d, want 8: %v", got, rec.snapshot())
		}
		for _, c := range rec.snapshot() {
			if !strings.Contains(c, "/3/search/") {
				t.Fatalf("clean miss must not load details, wire=%v", rec.snapshot())
			}
		}
		t.Logf("R9B 06_unverified_clean_no_match calls=%d", rec.count())
	})

	t.Run("07_tmdb_error_exact_search", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				return r9bOutage()
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if err == nil || errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("TMDB_ERROR exact: movie=%+v err=%v, want err!=nil not canceled", movie, err)
		}
		// Помилка exact зупиняє fallback: лише 3 retry одного URL.
		if got := rec.count(); got != 3 {
			t.Fatalf("exact error wire calls=%d, want 3: %v", got, rec.snapshot())
		}
		t.Logf("R9B 07_tmdb_error_exact calls=%d err=%v", rec.count(), err)
	})

	t.Run("08_tmdb_error_fetch_by_clean_title", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			q := r.URL.Query()
			// exact (без року) чистий, а перша спроба FetchByCleanTitle (movie+рік) падає.
			if strings.HasPrefix(r.URL.Path, "/3/search/") && q.Get("year") == "2024" {
				return r9bOutage()
			}
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if err == nil || errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("TMDB_ERROR fetch: movie=%+v err=%v, want err!=nil not canceled", movie, err)
		}
		if rec.count() <= 3 {
			t.Fatalf("fallback must run after clean exact miss, calls=%v", rec.snapshot())
		}
		t.Logf("R9B 08_tmdb_error_fetch calls=%d err=%v", rec.count(), err)
	})

	t.Run("09_tmdb_error_tv_fallback", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			q := r.URL.Query()
			// Падає лише перша TV-спроба з роком (TV retry після чистого movie-промаху).
			if r.URL.Path == "/3/search/tv" && q.Get("first_air_date_year") == "2024" {
				return r9bOutage()
			}
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if err == nil || errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("TMDB_ERROR tv: movie=%+v err=%v, want err!=nil not canceled", movie, err)
		}
		tvRetry := false
		for _, c := range rec.snapshot() {
			if strings.Contains(c, "/3/search/tv") && strings.Contains(c, "first_air_date_year=2024") {
				tvRetry = true
			}
		}
		if !tvRetry {
			t.Fatalf("TV fallback attempt must be in wire: %v", rec.snapshot())
		}
		t.Logf("R9B 09_tmdb_error_tv_fallback calls=%d err=%v", rec.count(), err)
	})

	t.Run("10_found_after_early_error", func(t *testing.T) {
		var n atomic.Int32
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			call := n.Add(1)
			q := r.URL.Query()
			switch p := r.URL.Path; {
			case p == "/3/search/movie" && q.Get("year") == "2024":
				return r9bOutage() // рання спроба FetchByCleanTitle (movie+рік) падає, 3 retry
			case p == "/3/search/tv" && call > 5:
				// протилежний тип у каскаді movie (після exact і movie+рік) знаходить TV-кандидата
				return r9bOK(fmt.Sprintf(`{"results":[{"id":200,"name":%q,"original_name":%q,"first_air_date":"2024-03-05"}]}`, title, title))
			case p == "/3/tv/200":
				return r9bOK(tvDetails(200, title))
			case strings.HasPrefix(p, "/3/search/"):
				return r9bOK(empty)
			default:
				return r9bResp{status: 404, body: "{}"}
			}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		// Пізній підтверджений збіг має пріоритет над ранньою помилкою: FOUND, err == nil.
		if err != nil || movie.TmdbID != 200 || movie.MediaType != "tv" {
			t.Fatalf("FOUND after error: movie=%+v err=%v, want TmdbID=200 tv err=nil", movie, err)
		}
		// wire: exact movie+tv (2), movie+рік 500×3, movie без року, tv opposite → деталі tv:200
		if got := rec.count(); got != 8 {
			t.Fatalf("FOUND after error wire calls=%d, want 8: %v", got, rec.snapshot())
		}
		t.Logf("R9B 10_found_after_early_error calls=%d wire=%v", rec.count(), rec.snapshot())
	})

	t.Run("11_tmdb_error_all_attempts_outage", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			q := r.URL.Query()
			// exact чистий, далі всі спроби з роком падають.
			if strings.HasPrefix(r.URL.Path, "/3/search/") && (q.Get("year") == "2024" || q.Get("first_air_date_year") == "2024") {
				return r9bOutage()
			}
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if err == nil || errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("TMDB_ERROR outage: movie=%+v err=%v, want err!=nil not canceled", movie, err)
		}
		if rec.count() <= 5 {
			t.Fatalf("outage must cover all allowed attempts, calls=%v", rec.snapshot())
		}
		t.Logf("R9B 11_tmdb_error_all_outage calls=%d err=%v", rec.count(), err)
	})

	t.Run("12_tmdb_error_invalid_json", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				return r9bOK(`{not valid json`)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.mergeGeminiWithTMDB(context.Background(), filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if err == nil || errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("TMDB_ERROR json: movie=%+v err=%v, want err!=nil not canceled", movie, err)
		}
		if got := rec.count(); got != 3 {
			t.Fatalf("invalid json wire calls=%d, want 3 retries: %v", got, rec.snapshot())
		}
		t.Logf("R9B 12_tmdb_error_invalid_json calls=%d err=%v", rec.count(), err)
	})

	t.Run("13_canceled_before_first_request", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp { return r9bOutage() })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		movie, err := a.mergeGeminiWithTMDB(ctx, filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if !errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("CANCELED: movie=%+v err=%v, want context.Canceled", movie, err)
		}
		if rec.count() != 0 {
			t.Fatalf("cancel before first request must not call TMDB, calls=%v", rec.snapshot())
		}
	})

	t.Run("14_canceled_between_attempts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var once sync.Once
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				once.Do(cancel) // скасування після першого exact-запиту
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.mergeGeminiWithTMDB(ctx, filepath.Join(root, "Test.2024.mkv"), r9bRec(title, 2024, "movie", 0.98))
		if !errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("CANCELED between attempts: movie=%+v err=%v, want context.Canceled", movie, err)
		}
		if rec.count() > 2 {
			t.Fatalf("no further TMDB requests after cancel, calls=%v", rec.snapshot())
		}
		t.Logf("R9B 14_canceled_between_attempts calls=%d", rec.count())
	})
}

func TestR9BRescueVerificationOutcomes(t *testing.T) {
	empty := `{"results":[]}`

	t.Run("16_rescue_unverified_no_suitable_folder", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp { return r9bOutage() })
		movie, err := a.rescueEmptyGeminiWithFolder(context.Background(), filepath.Join(root, "Test.2024.mkv"))
		if err != nil || movie.TmdbID != 0 {
			t.Fatalf("UNVERIFIED no folder: movie=%+v err=%v, want TmdbID=0 err=nil", movie, err)
		}
		if rec.count() != 0 {
			t.Fatalf("no parent title must not call TMDB, calls=%v", rec.snapshot())
		}
	})

	t.Run("17_rescue_unverified_clean_miss", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.rescueEmptyGeminiWithFolder(context.Background(), filepath.Join(root, "Alpha Folder", "Alpha Folder 1.2024.WEB-DLRip.mkv"))
		if err != nil || movie.TmdbID != 0 {
			t.Fatalf("UNVERIFIED rescue miss: movie=%+v err=%v, want TmdbID=0 err=nil", movie, err)
		}
		// movie fetch (3) + TV retry (3) для одного кандидата папки.
		if got := rec.count(); got != 6 {
			t.Fatalf("rescue clean miss calls=%d, want 6: %v", got, rec.snapshot())
		}
		t.Logf("R9B 17_rescue_unverified_clean_miss calls=%d", rec.count())
	})

	t.Run("18_rescue_tmdb_error", func(t *testing.T) {
		var a *App
		var rec *r9bRecorder
		var root string
		a, rec, root = r9bMergeApp(t, func(r *http.Request) r9bResp {
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				// перший URL падає (3 retry), решта чисті — достатньо для TMDB_ERROR
				if rec.count() <= 3 {
					return r9bOutage()
				}
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.rescueEmptyGeminiWithFolder(context.Background(), filepath.Join(root, "Alpha Folder", "Alpha Folder 1.2024.WEB-DLRip.mkv"))
		if err == nil || errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("TMDB_ERROR rescue: movie=%+v err=%v, want err!=nil not canceled", movie, err)
		}
		t.Logf("R9B 18_rescue_tmdb_error calls=%d err=%v wire=%v", rec.count(), err, rec.snapshot())
	})

	t.Run("19_rescue_found_after_early_error", func(t *testing.T) {
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			q := r.URL.Query()
			query := q.Get("query")
			switch {
			case strings.Contains(query, "Скарпетта") && q.Get("language") == "uk-UA":
				return r9bOutage() // перший кандидат (кирилиця) падає
			case r.URL.Path == "/3/search/tv" && query == "Scarpetta":
				return r9bOK(`{"results":[{"id":98765,"name":"Scarpetta","original_name":"Scarpetta","first_air_date":"2025-03-05","media_type":"tv"}]}`)
			case r.URL.Path == "/3/tv/98765":
				return r9bOK(`{"id":98765,"name":"Скарпетта","original_name":"Scarpetta","first_air_date":"2025-03-05","overview":"Український опис серіалу."}`)
			case strings.HasPrefix(r.URL.Path, "/3/search/"):
				return r9bOK(empty)
			default:
				return r9bResp{status: 404, body: "{}"}
			}
		})
		movie, err := a.rescueEmptyGeminiWithFolder(context.Background(), filepath.Join(root, "Скарпетта", "Скарпетта 8.WEB-DLRip.mkv"))
		// Пізніший підтверджений кандидат (латиниця Sk→Sc) — FOUND незважаючи на ранню помилку.
		if err != nil || movie.TmdbID != 98765 || movie.MediaType != "tv" {
			t.Fatalf("FOUND rescue: movie=%+v err=%v, want TmdbID=98765 tv err=nil", movie, err)
		}
		t.Logf("R9B 19_rescue_found_after_early_error calls=%d wire=%v", rec.count(), rec.snapshot())
	})

	t.Run("20_rescue_canceled_during_stop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, rec, root := r9bMergeApp(t, func(r *http.Request) r9bResp {
			if strings.HasPrefix(r.URL.Path, "/3/search/") {
				cancel() // STOP під час першого rescue-запиту
				return r9bOK(empty)
			}
			return r9bResp{status: 404, body: "{}"}
		})
		movie, err := a.rescueEmptyGeminiWithFolder(ctx, filepath.Join(root, "Alpha Folder", "Alpha Folder 1.2024.WEB-DLRip.mkv"))
		if !errors.Is(err, context.Canceled) || movie.TmdbID != 0 {
			t.Fatalf("CANCELED rescue: movie=%+v err=%v, want context.Canceled", movie, err)
		}
		t.Logf("R9B 20_rescue_canceled calls=%d wire=%v", rec.count(), rec.snapshot())
	})
}

// r9bQueueHarness — App із тимчасовою SQLite, fake TMDB transport і локальним AI endpoint.
func r9bQueueHarness(t *testing.T, existing []storage.Movie, tmdbHandler func(*http.Request) r9bResp, pending map[int]string) (*App, string, *r9bRecorder, *atomic.Int32) {
	t.Helper()
	a, dir := newTestAppDB(t, existing)
	a.cfg.MediaFolderPath = dir
	a.cfg.PostersDir = t.TempDir()
	a.cfg.TMDBAPIKey = "fixture"
	a.cfg.GeminiAPIKey = "fixture"
	a.cfg.GeminiModels = []string{"gemini-2.5-flash"}
	rec := &r9bRecorder{}
	a.tmdbClient = tmdb.NewClient(a.cfg)
	t.Cleanup(func() { a.tmdbClient.Close() })
	a.tmdbClient.SetTransport(rec.roundTrip(tmdbHandler))
	a.aiClient = ai.NewClient(a.cfg)
	aiCalls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		aiCalls.Add(1)
		var rows []map[string]any
		// Відповідаємо лише за ті recognition-N, що є у prompt і ще не закриті.
		for slot, title := range pending {
			if strings.Contains(string(body), fmt.Sprintf("recognition-%d", slot)) {
				rows = append(rows, map[string]any{
					"id": slot, "request_id": fmt.Sprintf("recognition-%d", slot),
					"original_file": fmt.Sprintf("F%02d.2024.mkv", slot),
					"en_title":      title, "year": 2024, "media_type": "movie", "status": "resolved", "confidence": 0.98,
				})
				delete(pending, slot)
			}
		}
		reply, _ := json.Marshal(rows)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, reply)
	}))
	t.Cleanup(server.Close)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)
	return a, dir, rec, aiCalls
}

func r9bQueueTMDB(t *testing.T, titles map[string]int, failQuery string, onDetails func(string)) func(*http.Request) r9bResp {
	return func(r *http.Request) r9bResp {
		p, q := r.URL.Path, r.URL.Query()
		switch {
		case p == "/3/search/movie" && q.Get("query") == failQuery:
			return r9bOutage()
		case p == "/3/search/movie":
			if id, ok := titles[q.Get("query")]; ok {
				return r9bOK(fmt.Sprintf(`{"results":[{"id":%d,"title":%q,"release_date":"2024-01-01"}]}`, id, q.Get("query")))
			}
			return r9bOK(`{"results":[]}`)
		case strings.HasPrefix(p, "/3/search/"):
			return r9bOK(`{"results":[]}`)
		case strings.HasPrefix(p, "/3/movie/"):
			if onDetails != nil {
				onDetails(p)
			}
			var id int
			fmt.Sscanf(p, "/3/movie/%d", &id)
			title := ""
			for name, tid := range titles {
				if tid == id {
					title = name
				}
			}
			return r9bOK(fmt.Sprintf(`{"id":%d,"title":"Українська назва","original_title":%q,"release_date":"2024-01-01","overview":"Український опис."}`, id, title))
		default:
			return r9bResp{status: 404, body: "{}"}
		}
	}
}

func r9bPaths(dir string, n int) []string {
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		paths[i] = filepath.Join(dir, fmt.Sprintf("F%02d.2024.mkv", i))
	}
	return paths
}

func TestR9BQueueOutcomes(t *testing.T) {
	t.Run("15_canceled_before_commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pending := map[int]string{}
		titles := map[string]int{}
		for i := 0; i < 11; i++ {
			title := fmt.Sprintf("Title %02d", i)
			pending[i] = title
			titles[title] = 101 + i
		}
		a, dir, rec, aiCalls := r9bQueueHarness(t, nil, r9bQueueTMDB(t, titles, "", func(p string) {
			if p == "/3/movie/101" {
				cancel() // STOP під час TMDB-верифікації першого файла
			}
		}), pending)
		accepted := a.processGeminiQueue(ctx, r9bPaths(dir, 11), a.aiClient)
		rows, err := a.db.GetAllMovies(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// CANCELED: без placeholder, без commit, без наступної AI-пачки.
		if len(accepted) != 0 || len(rows) != 0 {
			t.Fatalf("CANCELED: accepted=%v rows=%d, want empty", accepted, len(rows))
		}
		if aiCalls.Load() != 1 {
			t.Fatalf("CANCELED: AI calls=%d, want 1 (no second batch)", aiCalls.Load())
		}
		t.Logf("R9B 15_canceled_before_commit aiCalls=%d tmdbCalls=%d rows=%d", aiCalls.Load(), rec.count(), len(rows))
	})

	t.Run("21_confirmed_row_protected_from_tmdb_error", func(t *testing.T) {
		existing := storage.Movie{
			Filename: "F00.2024.mkv", TmdbID: 900, MediaType: "tv", TitleEN: "Confirmed",
			TitleUA: "Підтверджена назва", Plot: "Підтверджений опис.", RecognitionSource: "manual",
			NeedsReview: true, ReviewReason: "fixture",
		}
		for _, mode := range []string{"tmdb_error", "unverified"} {
			t.Run(mode, func(t *testing.T) {
				pending := map[int]string{0: "Ghost Title"}
				handler := r9bQueueTMDB(t, map[string]int{}, "Ghost Title", nil)
				if mode == "unverified" {
					handler = r9bQueueTMDB(t, map[string]int{}, "", nil)
				}
				a, dir, _, _ := r9bQueueHarness(t, []storage.Movie{existing}, handler, pending)
				accepted := a.processGeminiQueue(context.Background(), r9bPaths(dir, 1), a.aiClient)
				got, err := a.db.GetMovieByFilename(t.Context(), "F00.2024.mkv")
				if err != nil || got == nil {
					t.Fatalf("row missing: %v", err)
				}
				if got.TmdbID != 900 || got.TitleUA != existing.TitleUA || got.Plot != existing.Plot ||
					got.MediaType != "tv" || got.RecognitionSource != "manual" || !got.NeedsReview || got.ReviewReason != "fixture" {
					t.Fatalf("%s: confirmed row changed: %+v", mode, got)
				}
				if len(accepted) != 0 {
					t.Fatalf("%s: accepted=%v, want empty", mode, accepted)
				}
			})
		}
	})

	t.Run("22_sqlite_commit_error", func(t *testing.T) {
		pending := map[int]string{0: "Title 00"}
		titles := map[string]int{"Title 00": 101}
		a, dir, rec, aiCalls := r9bQueueHarness(t, nil, r9bQueueTMDB(t, titles, "", nil), pending)
		// Відмова COMMIT через deferred FK, як у R5 rollback.
		raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = raw.ExecContext(context.Background(), `
			CREATE TABLE r9b_parent(id INTEGER PRIMARY KEY);
			CREATE TABLE r9b_guard(id INTEGER REFERENCES r9b_parent(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER r9b_abort AFTER INSERT ON movies WHEN NEW.filename = 'F00.2024.mkv'
				BEGIN INSERT INTO r9b_guard VALUES (1); END;`)
		if err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		accepted := a.processGeminiQueue(context.Background(), r9bPaths(dir, 1), a.aiClient)
		rows, err := a.db.GetAllMovies(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// Помилка commit: accepted не нараховується, рядка немає — чинна політика R9-B.
		if len(accepted) != 0 || len(rows) != 0 {
			t.Fatalf("commit error: accepted=%v rows=%d, want empty", accepted, len(rows))
		}
		if aiCalls.Load() != 1 {
			t.Fatalf("commit error: AI calls=%d, want 1", aiCalls.Load())
		}
		t.Logf("R9B 22_sqlite_commit_error aiCalls=%d tmdbCalls=%d rows=%d", aiCalls.Load(), rec.count(), len(rows))
	})

	t.Run("23_ai_batch_8plus2_with_tmdb_error", func(t *testing.T) {
		titles := map[string]int{}
		for i := 0; i < 9; i++ {
			titles[fmt.Sprintf("Title %02d", i)] = 101 + i
		}
		// Пачка 10 файлів відповідає лише за 0..7; 8 і 9 приходять singleton-retry,
		// причому гіпотеза 9 веде в TMDB_ERROR (exact search → 500).
		a2, dir2 := newTestAppDB(t, nil)
		a2.cfg.MediaFolderPath = dir2
		a2.cfg.PostersDir = t.TempDir()
		a2.cfg.TMDBAPIKey = "fixture"
		a2.cfg.GeminiAPIKey = "fixture"
		a2.cfg.GeminiModels = []string{"gemini-2.5-flash"}
		rec2 := &r9bRecorder{}
		a2.tmdbClient = tmdb.NewClient(a2.cfg)
		t.Cleanup(func() { a2.tmdbClient.Close() })
		a2.tmdbClient.SetTransport(rec2.roundTrip(r9bQueueTMDB(t, titles, "Fail Title", nil)))
		a2.aiClient = ai.NewClient(a2.cfg)
		aiCalls2 := &atomic.Int32{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			attempt := aiCalls2.Add(1) - 1
			var rows []map[string]any
			answer := func(slot int, title string) {
				rows = append(rows, map[string]any{
					"id": slot, "request_id": fmt.Sprintf("recognition-%d", slot),
					"original_file": fmt.Sprintf("F%02d.2024.mkv", slot),
					"en_title":      title, "year": 2024, "media_type": "movie", "status": "resolved", "confidence": 0.98,
				})
			}
			if attempt == 0 {
				for i := 0; i < 8; i++ { // batch 8 із 10
					answer(i, fmt.Sprintf("Title %02d", i))
				}
			} else if attempt == 1 { // singleton retry для 8 — верифікується
				if strings.Contains(string(body), "recognition-8") {
					answer(8, "Title 08")
				}
			} else if attempt == 2 { // singleton retry для 9 — TMDB_ERROR
				if strings.Contains(string(body), "recognition-9") {
					answer(9, "Fail Title")
				}
			}
			reply, _ := json.Marshal(rows)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, reply)
		}))
		t.Cleanup(server.Close)
		t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)

		accepted := a2.processGeminiQueue(context.Background(), r9bPaths(dir2, 10), a2.aiClient)
		rows, err := a2.db.GetAllMovies(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		stored := map[string]int{}
		for _, m := range rows {
			stored[m.Filename] = m.TmdbID
		}
		if aiCalls2.Load() != 3 {
			t.Fatalf("8+2: AI calls=%d, want 3 (batch + 2 singleton)", aiCalls2.Load())
		}
		if len(accepted) != 9 {
			t.Fatalf("8+2: accepted=%v, want 9 files", accepted)
		}
		for i := 0; i < 9; i++ {
			want := 101 + i
			if stored[fmt.Sprintf("F%02d.2024.mkv", i)] != want {
				t.Errorf("8+2: F%02d stored=%d, want %d", i, stored[fmt.Sprintf("F%02d.2024.mkv", i)], want)
			}
		}
		// F09: TMDB_ERROR → чинний unresolved placeholder із захистом confirmed.
		if stored["F09.2024.mkv"] != 0 {
			t.Fatalf("8+2: F09 stored=%d, want unresolved 0", stored["F09.2024.mkv"])
		}
		for _, m := range rows {
			if m.Filename == "F09.2024.mkv" && (!m.NeedsReview || m.ReviewReason != "unresolved") {
				t.Errorf("8+2: F09 placeholder state wrong: %+v", m)
			}
		}
		t.Logf("R9B 23_ai_batch_8plus2 aiCalls=%d tmdbCalls=%d stored=%v", aiCalls2.Load(), rec2.count(), stored)
	})
}

func TestR9BFixSelectedVerificationOutcome(t *testing.T) {
	const filename = "F00.2024.mkv"
	const title = "Alpha Origin"
	const id = 10101
	confirmed := storage.Movie{
		Filename: filename, TmdbID: 900, MediaType: "movie", TitleEN: "Previous Title",
		Year: "2024", Plot: "Previous English plot.", NeedsReview: true, ReviewReason: "previous_review",
	}
	unresolved := storage.Movie{
		Filename: filename, TitleEN: "Previous Hypothesis", NeedsReview: true, ReviewReason: "unresolved",
	}
	transportErr := errors.New("fixture transport error")
	for _, tc := range []struct {
		name, hint, mediaType string
		failure, wantErr      string
		fix, wantFailed, stop bool
		existing              []storage.Movie
		wantID, writes, http  int
		aiCalls               int32
	}{
		{name: "24a_update_movie_found", wantID: id, writes: 1, aiCalls: 1, http: 3},
		{name: "24b_update_movie_http_500_returns_error", failure: "500", wantErr: "TMDB HTTP 500", aiCalls: 1, http: 3},
		{name: "update_movie_no_match", failure: "no_match", writes: 1, aiCalls: 1, http: 8},
		{name: "update_movie_transport", failure: "transport", wantErr: transportErr.Error(), existing: []storage.Movie{confirmed}, wantID: 900, aiCalls: 1, http: 3},
		{name: "update_movie_invalid_json", failure: "json", wantErr: "invalid TMDB response", existing: []storage.Movie{unresolved}, aiCalls: 1, http: 3},
		{name: "update_movie_details_id_mismatch", failure: "identity", wantErr: "TMDB details identity mismatch", aiCalls: 1, http: 3},
		{name: "update_movie_stop_before_tmdb", failure: "stop_before", stop: true, aiCalls: 1},
		{name: "update_movie_stop_during_tmdb", failure: "stop_during", stop: true, aiCalls: 1, http: 1, existing: []storage.Movie{unresolved}},
		{name: "update_movie_commit_failure", failure: "commit", wantErr: "batch commit failed", aiCalls: 1, http: 3},
		{name: "fix_selected_empty_hint_queue", fix: true, failure: "500", wantFailed: true, existing: []storage.Movie{unresolved}, writes: 1, aiCalls: 1, http: 3},
		{name: "fix_selected_empty_hint_confirmed", fix: true, failure: "500", wantFailed: true, existing: []storage.Movie{confirmed}, wantID: 900, aiCalls: 1, http: 3},
		{name: "fix_selected_explicit_type", fix: true, mediaType: "movie", wantID: id, writes: 1, aiCalls: 1, http: 2},
		{name: "fix_selected_explicit_type_error", fix: true, mediaType: "movie", failure: "500", wantFailed: true, aiCalls: 1, http: 3},
		{name: "fix_selected_stop_during_tmdb", fix: true, mediaType: "movie", failure: "stop_during", stop: true, aiCalls: 1, http: 1},
		{name: "fix_selected_exact_title", fix: true, hint: title, wantID: id, writes: 1, http: 3},
		{name: "fix_selected_tmdb_id", fix: true, hint: "10101", wantID: id, writes: 1, http: 1},
		{name: "fix_selected_imdb_id", fix: true, hint: "tt1234567", wantID: id, writes: 1, http: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stop := func() { cancel() }

			titles := map[string]int{title: id}
			if tc.failure == "no_match" {
				titles = nil
			}
			respond := r9bQueueTMDB(t, titles, "", nil)
			a, dir, rec, aiCalls := r9bQueueHarness(t, tc.existing, func(req *http.Request) r9bResp {
				switch req.URL.Path {
				case "/3/find/tt1234567":
					return r9bOK(`{"movie_results":[{"id":10101,"title":"Alpha Origin","release_date":"2024-01-01"}]}`)
				case "/3/movie/10101":
					if tc.failure == "identity" {
						return r9bOK(`{"id":10102,"title":"Alpha Origin"}`)
					}
				case "/3/search/movie":
					switch tc.failure {
					case "500":
						return r9bOutage()
					case "transport":
						return r9bResp{err: transportErr}
					case "json":
						return r9bOK("{")
					case "stop_during":
						stop()
						return r9bResp{err: req.Context().Err()}
					}
				}
				return respond(req)
			}, map[int]string{0: title})
			a.ctx = ctx
			if tc.fix {
				stop = a.StopScan
			}
			var completion string
			localizations := 0
			a.eventEmitter = func(_ context.Context, name string, args ...interface{}) {
				switch name {
				case "log-message":
					message := args[0].(string)
					if tc.failure == "stop_before" && strings.Contains(message, "Gemini: '") {
						cancel()
					}
					if strings.Contains(message, "Аналіз локалізації") {
						localizations++
					}
				case "scan-finished":
					completion = args[0].(string)
				}
			}

			raw, err := sql.Open("sqlite", filepath.Join(dir, "movies.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			// Рахуємо також upsert, який залишає значення рядка такими самими.
			_, err = raw.ExecContext(t.Context(), `
				CREATE TABLE movie_writes(id INTEGER);
				CREATE TRIGGER count_insert AFTER INSERT ON movies BEGIN INSERT INTO movie_writes VALUES (NEW.tmdb_id); END;
				CREATE TRIGGER count_update AFTER UPDATE ON movies BEGIN INSERT INTO movie_writes VALUES (NEW.tmdb_id); END;`)
			if err != nil {
				t.Fatal(err)
			}
			if tc.failure == "commit" {
				_, err = raw.ExecContext(t.Context(), `
					CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY);
					CREATE TABLE fixture_guard(id INTEGER REFERENCES fixture_parent(id) DEFERRABLE INITIALLY DEFERRED);
					CREATE TRIGGER fixture_abort AFTER INSERT ON movies BEGIN INSERT INTO fixture_guard VALUES (1); END;`)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := a.db.GetAllMovies(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			var updateErr error
			if tc.fix {
				a.FixSelected([]FixRequest{{Filename: filename, Hint: tc.hint, MediaType: tc.mediaType}})
			} else {
				updateErr = a.UpdateMovie(filename, "")
			}

			after, err := a.db.GetAllMovies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var writes int
			if err := raw.QueryRowContext(t.Context(), "SELECT count(*) FROM movie_writes").Scan(&writes); err != nil {
				t.Fatal(err)
			}
			var ids []int
			for _, row := range after {
				ids = append(ids, row.TmdbID)
			}
			recognitions := a.aiClient.CallMetrics().RecognitionBatch
			t.Logf("R11A recognition=%d ai=%d tmdb=%v writes=%d ids=%v unchanged=%t localization=%d canceled=%t error=%t completion=%q",
				recognitions, aiCalls.Load(), rec.snapshot(), writes, ids, slices.Equal(before, after), localizations,
				errors.Is(updateErr, context.Canceled), updateErr != nil, completion)

			if tc.fix {
				want := "Успішно виправлено: 1; не виправлено: 0"
				if tc.wantFailed {
					want = "Успішно виправлено: 0; не виправлено: 1"
				}
				if tc.stop {
					want = "Виправлення перервано користувачем"
				}
				if completion != want {
					t.Errorf("completion = %q, want %q", completion, want)
				}
			} else if tc.stop {
				if !errors.Is(updateErr, context.Canceled) || updateErr != ctx.Err() {
					t.Errorf("error = %v, want ctx.Err()", updateErr)
				}
			} else if tc.wantErr == "" {
				if updateErr != nil {
					t.Errorf("unexpected error: %v", updateErr)
				}
			} else if updateErr == nil || !strings.Contains(updateErr.Error(), tc.wantErr) {
				t.Errorf("error = %v, want %q", updateErr, tc.wantErr)
			}
			if tc.failure == "transport" && !errors.Is(updateErr, transportErr) {
				t.Errorf("transport cause missing from error: %v", updateErr)
			}

			if tc.stop && a.db.GetState(t.Context(), "last_scan_at") != "" {
				t.Error("last_scan_at changed after STOP")
			}
			if writes != tc.writes || rec.count() != tc.http || aiCalls.Load() != tc.aiCalls || recognitions != int64(tc.aiCalls) || localizations != 0 {
				t.Errorf("writes/TMDB/AI/recognition/localization = %d/%d/%d/%d/%d, want %d/%d/%d/%d/0",
					writes, rec.count(), aiCalls.Load(), recognitions, localizations, tc.writes, tc.http, tc.aiCalls, tc.aiCalls)
			}
			if tc.writes == 0 && !slices.Equal(before, after) {
				t.Errorf("SQLite row changed: before=%+v after=%+v", before, after)
			}
			if (tc.writes != 0 || len(tc.existing) != 0) && !slices.Equal(ids, []int{tc.wantID}) {
				t.Errorf("stored TMDB IDs = %v, want [%d]", ids, tc.wantID)
			}
			if tc.wantID == id && tc.aiCalls == 1 && len(after) == 1 && (after[0].MediaType != "movie" || after[0].RecognitionSource != "gemini") {
				t.Errorf("unexpected confirmed identity or source: %+v", after[0])
			}
		})
	}
}
