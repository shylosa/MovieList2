package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/time/rate"
)

func TestGetCandidateDetailsShortFilmAndCache(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.RawQuery, "append_to_response") {
			t.Error("candidate details requested appended data")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":385938,"title":"Oculus","original_title":"Oculus","release_date":"2013-01-18","runtime":14,"vote_average":4.2,"vote_count":8,"poster_path":"/oculus.jpg","genres":[{"name":"Horror"}],"overview":"Short."}`))
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &candidateDetailsTransport{server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	first, err := c.GetCandidateDetails(context.Background(), MediaTypeMovie, 385938)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.GetCandidateDetails(context.Background(), MediaTypeMovie, 385938)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ShortFilm || first.Runtime != 14 || first.VoteCount != 8 || second.TMDBID != first.TMDBID {
		t.Fatalf("details: first=%+v second=%+v", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests=%d, want 1", calls.Load())
	}
}

type candidateDetailsTransport struct{ serverURL string }

func (t *candidateDetailsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base, _ := url.Parse(t.serverURL)
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = base.Scheme, base.Host
	return http.DefaultTransport.RoundTrip(clone)
}
