package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

// R7 A/B oracle: кожен сценарій проганяється на A (R6, з кешами деталей) і на
// B (R7, без кешів) з однаковими фікстурами. Тест перевіряє лише оракул
// identity (filename, MediaType, TMDB ID) і логує фактичні HTTP-запити —
// їх порівняння дає A/B-таблицю. Цей файл свідомо використовує лише публічний
// API, тому компілюється і на A, і на B.

func r7ABClient(t *testing.T, reply func(req *http.Request, n int) (*http.Response, error)) (*Client, *[]string) {
	t.Helper()
	n := 0
	c, calls := r2Fixture(func(req *http.Request) (*http.Response, error) {
		n++
		return reply(req, n)
	})
	c.postersDir = t.TempDir()
	return c, calls
}

func r7MovieDetailsBody(title string) string {
	return fmt.Sprintf(`{"id":42,"title":%q,"original_title":"Signal","release_date":"2024-01-01","overview":"Опис фільму","poster_path":"/movie.jpg","credits":{"cast":[{"name":"Tom Hardy"}]}}`, title)
}

func r7TVDetailsBody(name string) string {
	return fmt.Sprintf(`{"id":42,"name":%q,"original_name":"Signal TV","first_air_date":"2022-01-01","overview":"Опис серіалу"}`, name)
}

func TestR7ABScenarios(t *testing.T) {
	ctx := context.Background()

	t.Run("repeat_full_details_same_file", func(t *testing.T) {
		c, calls := r7ABClient(t, func(req *http.Request, n int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				return r2Response(200, r7MovieDetailsBody(fmt.Sprintf("Фільм версія %d", n)))
			case "/t/p/w500/movie.jpg":
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		first, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		if err != nil || first == nil || first.TMDBID != 42 || first.MediaType != MediaTypeMovie {
			t.Fatalf("first: %+v, %v", first, err)
		}
		second, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		if err != nil || second == nil || second.TMDBID != 42 {
			t.Fatalf("second: %+v, %v", second, err)
		}
		m := c.RequestMetrics()
		t.Logf("R7_AB repeat_full_details http=%d details=%d titleA=%q titleB=%q calls=%q", len(*calls), m.DetailsCalls, first.TitleUA, second.TitleUA, *calls)
	})

	t.Run("repeat_details_after_tmdb_change", func(t *testing.T) {
		version := 0
		c, calls := r7ABClient(t, func(req *http.Request, n int) (*http.Response, error) {
			if req.URL.Path == "/3/movie/42" {
				version++
				return r2Response(200, r7MovieDetailsBody(fmt.Sprintf("Фільм версія %d", version)))
			}
			if strings.HasPrefix(req.URL.Path, "/t/p/") {
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		first, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		if err != nil {
			t.Fatal(err)
		}
		version++ // TMDB змінив відповідь між зверненнями
		second, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		if err != nil || second == nil || second.TMDBID != 42 {
			t.Fatalf("second: %+v, %v", second, err)
		}
		t.Logf("R7_AB details_after_change http=%d titleA=%q titleB=%q fresh=%t", len(*calls), first.TitleUA, second.TitleUA, second.TitleUA != first.TitleUA)
	})

	t.Run("movie_and_tv_same_numeric_id", func(t *testing.T) {
		c, calls := r7ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			case "/3/tv/42":
				return r2Response(200, r7TVDetailsBody("Серіал"))
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		movie, err := c.GetDetails(ctx, MediaTypeMovie, 42, "")
		if err != nil || movie == nil || movie.MediaType != MediaTypeMovie || movie.TMDBID != 42 {
			t.Fatalf("movie: %+v, %v", movie, err)
		}
		tv, err := c.GetDetails(ctx, MediaTypeTV, 42, "")
		if err != nil || tv == nil || tv.MediaType != MediaTypeTV || tv.TMDBID != 42 {
			t.Fatalf("tv: %+v, %v", tv, err)
		}
		if movie.TitleUA == tv.TitleUA {
			t.Errorf("movie and tv share title %q", movie.TitleUA)
		}
		t.Logf("R7_AB movie_tv_same_id http=%d movie=%q tv=%q calls=%q", len(*calls), movie.TitleUA, tv.TitleUA, *calls)
	})

	t.Run("two_files_same_identity_separate_posters", func(t *testing.T) {
		c, calls := r7ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				return r2Response(200, r7MovieDetailsBody("Фільм"))
			case "/t/p/w500/movie.jpg":
				return r2Response(200, "poster")
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		a, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		if err != nil {
			t.Fatal(err)
		}
		b, err := c.GetDetails(ctx, MediaTypeMovie, 42, "b.mkv")
		if err != nil {
			t.Fatal(err)
		}
		if a.TMDBID != 42 || b.TMDBID != 42 {
			t.Fatalf("identities: %d, %d", a.TMDBID, b.TMDBID)
		}
		if a.LocalPosterPath == b.LocalPosterPath || a.LocalPosterPath == "" {
			t.Errorf("posters not separated: a=%q b=%q", a.LocalPosterPath, b.LocalPosterPath)
		}
		for _, p := range []string{a.LocalPosterPath, b.LocalPosterPath} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("poster missing on disk: %s: %v", p, err)
			}
		}
		t.Logf("R7_AB two_files_posters http=%d posterA=%q posterB=%q calls=%q", len(*calls), a.LocalPosterPath, b.LocalPosterPath, *calls)
	})

	t.Run("refresh_after_get_details", func(t *testing.T) {
		c, calls := r7ABClient(t, func(req *http.Request, n int) (*http.Response, error) {
			if req.URL.Path == "/3/movie/42" {
				return r2Response(200, r7MovieDetailsBody(fmt.Sprintf("Фільм версія %d", n)))
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		if _, err := c.GetDetails(ctx, MediaTypeMovie, 42, ""); err != nil {
			t.Fatal(err)
		}
		refreshed, err := c.RefreshDetails(ctx, MediaTypeMovie, 42)
		if err != nil || refreshed == nil || refreshed.TMDBID != 42 || refreshed.MediaType != MediaTypeMovie {
			t.Fatalf("refresh: %+v, %v", refreshed, err)
		}
		t.Logf("R7_AB refresh_after_get http=%d calls=%q", len(*calls), *calls)
	})

	t.Run("preview_repeat_and_change", func(t *testing.T) {
		version := 0
		c, calls := r7ABClient(t, func(req *http.Request, n int) (*http.Response, error) {
			if req.URL.Path == "/3/movie/42" {
				version++
				return r2Response(200, fmt.Sprintf(`{"id":42,"title":"Превʼю %d","original_title":"Signal","release_date":"2024-01-01","runtime":14,"vote_average":4.2,"vote_count":%d,"poster_path":"/p.jpg","genres":[{"name":"Horror"}],"overview":"Короткий опис."}`, version, version))
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		first, err := c.GetCandidateDetails(ctx, MediaTypeMovie, 42)
		if err != nil || first == nil || first.TMDBID != 42 {
			t.Fatalf("first preview: %+v, %v", first, err)
		}
		second, err := c.GetCandidateDetails(ctx, MediaTypeMovie, 42)
		if err != nil || second == nil || second.TMDBID != 42 {
			t.Fatalf("second preview: %+v, %v", second, err)
		}
		if !first.ShortFilm || !second.ShortFilm {
			t.Errorf("ShortFilm lost: %+v %+v", first, second)
		}
		t.Logf("R7_AB preview_repeat http=%d titleA=%q titleB=%q fresh=%t calls=%q", len(*calls), first.Title, second.Title, second.Title != first.Title, *calls)
	})

	t.Run("preview_then_confirm", func(t *testing.T) {
		c, calls := r7ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/movie/42":
				if req.URL.Query().Get("append_to_response") != "" {
					return r2Response(200, r7MovieDetailsBody("Фільм"))
				}
				return r2Response(200, `{"id":42,"title":"Превʼю","original_title":"Signal","release_date":"2024-01-01","vote_average":7.5,"vote_count":100}`)
			}
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		})
		preview, err := c.GetCandidateDetails(ctx, MediaTypeMovie, 42)
		if err != nil || preview == nil || preview.TMDBID != 42 {
			t.Fatalf("preview: %+v, %v", preview, err)
		}
		confirmed, err := c.GetDetails(ctx, MediaTypeMovie, 42, "a.mkv")
		if err != nil || confirmed == nil || confirmed.TMDBID != 42 {
			t.Fatalf("confirm: %+v, %v", confirmed, err)
		}
		t.Logf("R7_AB preview_confirm http=%d calls=%q", len(*calls), *calls)
	})

	t.Run("candidate_list_without_preview", func(t *testing.T) {
		c, calls := r7ABClient(t, func(req *http.Request, _ int) (*http.Response, error) {
			switch req.URL.Path {
			case "/3/search/movie":
				return r2Response(200, `{"results":[
					{"id":1,"title":"Alpha","original_title":"Alpha","release_date":"2024-01-01"},
					{"id":2,"title":"Alpha 2","original_title":"Alpha 2","release_date":"2023-01-01"},
					{"id":3,"title":"Alpha 3","original_title":"Alpha 3","release_date":"2022-01-01"},
					{"id":4,"title":"Alpha 4","original_title":"Alpha 4","release_date":"2021-01-01"},
					{"id":5,"title":"Alpha 5","original_title":"Alpha 5","release_date":"2020-01-01"},
					{"id":6,"title":"Alpha 6","original_title":"Alpha 6","release_date":"2019-01-01"}]}`)
			default:
				return nil, fmt.Errorf("unexpected path %s (details must not load)", req.URL.Path)
			}
		})
		candidates, err := c.SearchCandidates(ctx, "Alpha", 2024, "movie")
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) > 5 {
			t.Errorf("candidates=%d, want at most 5", len(candidates))
		}
		for _, call := range *calls {
			if strings.Contains(call, "/movie/") && !strings.Contains(call, "/search/") {
				t.Errorf("candidate list loaded details: %q", call)
			}
		}
		t.Logf("R7_AB candidate_list http=%d candidates=%d calls=%q", len(*calls), len(candidates), *calls)
	})
}
