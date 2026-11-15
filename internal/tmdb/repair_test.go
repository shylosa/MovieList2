package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"golang.org/x/time/rate"
)

func TestRefreshDetailsOnlyStoredEntityAndFreshData(t *testing.T) {
	for _, kind := range []MediaType{MediaTypeMovie, MediaTypeTV} {
		t.Run(string(kind), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != fmt.Sprintf("/3/%s/42", kind) {
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
				}
				if r.URL.Query().Get("language") != "uk-UA" {
					t.Error("expected Ukrainian first")
				}
				if r.URL.Query().Get("append_to_response") != "credits" {
					t.Error("cast not refreshed")
				}
				fmt.Fprintf(w, `{"id":42,"title":"Український фільм %d","name":"Український серіал %d","overview":"Це український опис.","genres":[{"name":"Drama"}],"credits":{"cast":[{"name":"Tom Hardy"}]},"vote_count":10,"vote_average":7}`, calls, calls)
			}))
			defer server.Close()
			client := &Client{client: &http.Client{Transport: &candidateDetailsTransport{server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 10)}
			client.detailsCache.Store(fmt.Sprintf("%s:42", kind), &MovieInfo{TMDBID: 42, MediaType: kind, TitleUA: "Stale"})
			first, err := client.RefreshDetails(context.Background(), kind, 42)
			if err != nil {
				t.Fatal(err)
			}
			second, err := client.RefreshDetails(context.Background(), kind, 42)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || first.TitleUA == second.TitleUA || second.TMDBID != 42 || second.MediaType != kind || second.Cast != "Tom Hardy" || second.Genres != "Драма" {
				t.Fatalf("not a fresh metadata request: %+v %+v (%d calls)", first, second, calls)
			}
			if !HasLocalizedGenres(second.Genres) {
				t.Fatal("ambiguous Ukrainian genre rejected")
			}
		})
	}
}

func TestRefreshDetailsLanguageCascadeAndValidation(t *testing.T) {
	languages := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := r.URL.Query().Get("language")
		languages = append(languages, lang)
		fmt.Fprint(w, `{"id":42,"title":"English","original_title":"Original","overview":"English plot"}`)
	}))
	defer server.Close()
	client := &Client{client: &http.Client{Transport: &candidateDetailsTransport{server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 10)}
	if _, err := client.RefreshDetails(context.Background(), MediaTypeMovie, 42); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(languages, []string{"uk-UA", "ru-RU", "en-US"}) {
		t.Fatalf("cascade: %v", languages)
	}
	for _, identity := range []struct {
		kind MediaType
		id   int
	}{{MediaTypeMovie, 0}, {"unknown", 42}} {
		if _, err := client.RefreshDetails(context.Background(), identity.kind, identity.id); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.RefreshDetails(ctx, MediaTypeMovie, 42); err == nil {
		t.Fatal("cancellation ignored")
	}
	if len(languages) != 3 {
		t.Fatal("invalid/cancelled repair made requests")
	}
}
