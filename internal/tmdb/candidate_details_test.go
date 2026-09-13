package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

func TestPreferredLocalizedTextPriorities(t *testing.T) {
	if got := preferredLocalizedText("Суперкопы 80", "Super Troopers 80", "Суперкопы 80"); got != "Super Troopers 80" {
		t.Fatalf("title source=%q", got)
	}
	if got := preferredLocalizedText("Russian plot with ы", "Російський опис", "English plot"); got != "Російський опис" {
		t.Fatalf("plot source=%q", got)
	}
	if got := preferredLocalizedText("Українська назва", "English", "Русская"); got != "Українська назва" {
		t.Fatalf("ukrainian source=%q", got)
	}
}

func TestGetDetailsConcurrentReadersShareMetadataWaterfall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"title":"Українська назва","original_title":"English Title","release_date":"2024-01-01","overview":"Український опис","genres":[],"credits":{"cast":[]}}`))
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &candidateDetailsTransport{server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 20)}
	const readers = 20
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := c.GetDetails(context.Background(), MediaTypeMovie, 42, "")
			if err == nil && (info == nil || info.TMDBID != 42) {
				err = fmt.Errorf("unexpected details: %+v", info)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("metadata waterfall requests=%d, want 1", got)
	}
}

func TestGetDetailsWaitingReaderHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":43,"title":"Українська назва","original_title":"English","release_date":"2024-01-01","overview":"Український опис","genres":[],"credits":{"cast":[]}}`))
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &candidateDetailsTransport{server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 2)}
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.GetDetails(context.Background(), MediaTypeMovie, 43, "")
		firstDone <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := c.GetDetails(ctx, MediaTypeMovie, 43, "")
		secondDone <- err
	}()
	cancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting reader error=%v, want context.Canceled", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

type candidateDetailsTransport struct{ serverURL string }

func (t *candidateDetailsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base, _ := url.Parse(t.serverURL)
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = base.Scheme, base.Host
	return http.DefaultTransport.RoundTrip(clone)
}
