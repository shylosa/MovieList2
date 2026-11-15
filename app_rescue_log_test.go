package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/tmdb"
)

func TestRescueProductionLocalizedTitles(t *testing.T) {
	for _, tt := range []struct {
		title, original string
		id              int
	}{
		{"Вы нам не подходите", "Not Suitable for Work", 284725},
		{"Вместе до конца", "Ride or Die", 241882},
	} {
		for _, scenario := range []string{"exact", "wrong", "ambiguous", "identity mismatch"} {
			t.Run(tt.title+"/"+scenario, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case strings.HasSuffix(r.URL.Path, "/search/tv") && r.URL.Query().Get("query") == tt.title:
						name := tt.title
						if scenario == "wrong" {
							name = "Совсем другая история"
						}
						fmt.Fprintf(w, `{"results":[{"id":%d,"name":%q,"original_name":%q,"first_air_date":"2025-01-01","original_language":"en"}`, tt.id, name, tt.original)
						if scenario == "ambiguous" {
							fmt.Fprintf(w, `,{"id":%d,"name":%q,"original_name":%q,"first_air_date":"2025-01-01","original_language":"en"}`, tt.id+1, name, tt.original)
						}
						fmt.Fprint(w, `]}`)
					case strings.Contains(r.URL.Path, "/search/"):
						fmt.Fprint(w, `{"results":[]}`)
					case strings.HasSuffix(r.URL.Path, "/alternative_titles"):
						fmt.Fprint(w, `{"results":[]}`)
					default:
						id := tt.id
						if scenario == "identity mismatch" {
							id += 99
						}
						fmt.Fprintf(w, `{"id":%d,"name":"Разом назавжди","original_name":%q,"overview":"Це український опис.","first_air_date":"2025-01-01"}`, id, tt.original)
					}
				}))
				defer server.Close()
				u, _ := url.Parse(server.URL)
				root := t.TempDir()
				a := NewApp()
				a.cfg = &config.Config{MediaFolderPath: root, TMDBAPIKey: "test", PostersDir: t.TempDir()}
				a.tmdbClient = tmdb.NewClient(a.cfg)
				a.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
				got := a.rescueEmptyGeminiWithFolder(context.Background(), filepath.Join(root, tt.title, tt.title+" 1.WEB-DLRip.avi"))
				want := 0
				if scenario == "exact" {
					want = tt.id
				}
				if got.TmdbID != want || (want > 0 && got.MediaType != "tv") {
					t.Fatalf("rescue=%+v want ID=%d", got, want)
				}
			})
		}
	}
}
