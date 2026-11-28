package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func TestR3AppTMDBIdentity(t *testing.T) {
	for _, operation := range []string{"confirm", "repair", "manual_id"} {
		for _, responseID := range []int{12345, 12346} {
			t.Run(fmt.Sprintf("%s/id=%d", operation, responseID), func(t *testing.T) {
				const expectedID = 12345
				kind := "movie"
				fields := `"title":"Український фільм","original_title":"Film"`
				if operation == "repair" {
					kind = "tv"
					fields = `"name":"Український серіал","original_name":"Series"`
				}
				before := storage.Movie{
					Filename: "folder/fixture.mkv", TmdbID: expectedID, MediaType: kind,
					TitleUA: "Попередня назва", Plot: "Український опис.",
					RecognitionSource: "manual_title", NeedsReview: true, ReviewReason: "fixture",
				}
				a := repairTestApp(t, before)
				a.ctx = t.Context()
				wantBefore, err := a.db.GetMovieByFilename(a.ctx, before.Filename)
				if err != nil {
					t.Fatal(err)
				}
				var details, posters atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == fmt.Sprintf("/3/%s/%d", kind, expectedID):
						details.Add(1)
						poster := ""
						if responseID != expectedID {
							poster = `,"poster_path":"/foreign.jpg"`
						}
						fmt.Fprintf(w, `{"id":%d,%s,"overview":"Новий український опис."%s}`, responseID, fields, poster)
					case strings.HasPrefix(r.URL.Path, "/t/p/"):
						posters.Add(1)
						fmt.Fprint(w, "fixture poster")
					default:
						t.Errorf("unexpected endpoint %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				u, err := url.Parse(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				a.tmdbClient = tmdb.NewClient(&config.Config{TMDBAPIKey: "fixture", PostersDir: t.TempDir()})
				a.tmdbClient.SetTransport(&mockTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host})
				switch operation {
				case "confirm":
					err = a.ConfirmTMDBCandidate(CandidateConfirmRequest{before.Filename, expectedID, kind})
				case "repair":
					_, err = a.RepairMetadata(before.Filename)
				case "manual_id":
					err = a.updateMovie(a.ctx, before.Filename, "12345")
				}
				a.wg.Wait()
				stored, readErr := a.db.GetMovieByFilename(a.ctx, before.Filename)
				if readErr != nil || stored == nil {
					t.Fatalf("stored movie: %+v, %v", stored, readErr)
				}
				t.Logf("R3_APP stored=%s:%d changed=%t err=%v details=%d posters=%d",
					stored.MediaType, stored.TmdbID, !reflect.DeepEqual(stored, wantBefore), err, details.Load(), posters.Load())
				if responseID == expectedID {
					if err != nil || stored.TmdbID != expectedID || stored.MediaType != kind || reflect.DeepEqual(stored, wantBefore) {
						t.Errorf("valid details did not update the movie: %+v, %v", stored, err)
					}
				} else if err == nil || !reflect.DeepEqual(stored, wantBefore) {
					t.Errorf("foreign details affected SQLite: %+v, %v", stored, err)
				}
				if details.Load() != 1 || posters.Load() != 0 {
					t.Errorf("details HTTP=%d, poster HTTP=%d; want 1 and 0", details.Load(), posters.Load())
				}
			})
		}
	}
}
