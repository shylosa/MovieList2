package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/config"
	"movielist-app/internal/storage"
)

func TestStoredLocalizationSkipsCompletedFallbackAndRetriesFailure(t *testing.T) {
	for _, response := range []string{"original", "missing", "invalid", "http_failure"} {
		t.Run(response, func(t *testing.T) {
			movie := storage.Movie{Filename: "original.mkv", TmdbID: 1, MediaType: "movie", TitleUA: "Original", TitleEN: "Original"}
			a, dir := newTestAppDB(t, []storage.Movie{movie})
			calls := 0
			previous := http.DefaultTransport
			http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				text := `[]`
				if response == "original" {
					text = `[{"filename":"original.mkv","title":"Original","plot":""}]`
				}
				if response == "invalid" {
					text = `[{"filename":"original.mkv","title":"Unrelated title","plot":""}]`
				}
				body, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]string{"text": text}}}}}})
				status := http.StatusOK
				if response == "http_failure" {
					status = http.StatusBadRequest
					body = []byte(`{"error":{"code":400,"message":"test failure","status":"INVALID_ARGUMENT"}}`)
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			cfg := &config.Config{GeminiAPIKey: "test-key", GeminiModels: []string{"gemini-2.5-flash"}}
			a.aiClient = ai.NewClient(cfg)
			a.localizeStoredMovies(context.Background(), nil)
			if calls != 1 {
				t.Fatalf("first attempt made %d requests", calls)
			}
			if err := a.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := storage.New(filepath.Join(dir, "movies.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a.db = db
			a.aiClient = ai.NewClient(cfg)
			a.localizeStoredMovies(context.Background(), nil)
			want := 2
			if response == "original" {
				want = 1
			}
			if calls != want {
				t.Fatalf("requests after restart = %d; want %d", calls, want)
			}
			if response == "original" {
				// Explicit localization bypasses the automatic-scan completion filter.
				a.aiClient = ai.NewClient(cfg)
				a.processTranslationQueue(context.Background(), []string{movie.Filename}, a.aiClient, nil)
				if calls != 2 {
					t.Fatal("explicit localization did not retry")
				}
			}
		})
	}
}

func TestLocalizationCompletionRequiresUsableResult(t *testing.T) {
	for _, tc := range []struct {
		name, title, plot, returnedTitle, returnedPlot string
		want                                           bool
	}{
		{"original", "Original", "", "Original", "", true},
		{"translated", "Original", "English plot", "Офіційна назва", "Український опис.", true},
		{"missing_title", "Original", "", "", "", false},
		{"rejected_title", "Original", "", "Unrelated", "", false},
		{"missing_plot", "Original", "English plot", "Original", "", false},
		{"foreign_plot", "Original", "English plot", "Original", "English plot", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			movie := storage.Movie{TmdbID: 1, TitleUA: tc.title, TitleEN: "Original", Plot: tc.plot}
			result := ai.BulkTranslateItem{Title: tc.returnedTitle, Plot: tc.returnedPlot}
			update, _ := prepareLocalizationUpdate(movie, result)
			if got := localizationCompleted(movie, result, update); got != tc.want {
				t.Fatalf("completed = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestLocalizationRejectsForeignTitlesButKeepsVerifiedIdentity(t *testing.T) {
	base := storage.Movie{Filename: "fish.mkv", TmdbID: 1757519, MediaType: "movie", TitleUA: "Healthy as a Fish", TitleEN: "Sano come un pesce"}
	for _, title := range []string{"Здоровый как рыба", "Unrelated English Title", "<think>reasoning</think>Здоровий як риба", ""} {
		update, _ := prepareLocalizationUpdate(base, ai.BulkTranslateItem{Title: title, Plot: "Український опис фільму."})
		if update.TitleChanged || update.Movie.TitleUA != base.TitleUA || !update.PlotChanged || update.Movie.TmdbID != base.TmdbID || update.Movie.MediaType != base.MediaType {
			t.Fatalf("unsafe localization accepted or valid plot lost for %q: %+v", title, update)
		}
	}
	for _, title := range []string{"Здоровий, як риба", base.TitleEN} {
		update, _ := prepareLocalizationUpdate(base, ai.BulkTranslateItem{Title: title})
		if !update.TitleChanged || update.Movie.TitleUA != title || update.Movie.TmdbID != base.TmdbID {
			t.Fatalf("valid Ukrainian/original title rejected: %+v", update)
		}
	}
	base.TitleUA = "Здоровий, як риба"
	update, _ := prepareLocalizationUpdate(base, ai.BulkTranslateItem{Title: base.TitleEN})
	if update.TitleChanged || update.Movie.TitleUA != base.TitleUA {
		t.Fatal("trusted Ukrainian title downgraded to original")
	}
}

func TestLocalizationSuccessReportedOnlyAfterCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "failure"}[fail], func(t *testing.T) {
			a, _ := newTestAppDB(t, []storage.Movie{{Filename: "fish.mkv", TmdbID: 1757519, MediaType: "movie", TitleUA: "Healthy as a Fish", TitleEN: "Sano come un pesce"}})
			var messages []string
			a.eventEmitter = func(_ context.Context, event string, args ...interface{}) {
				if event != "log-message" {
					return
				}
				messages = append(messages, args[0].(string))
				stored, err := a.db.GetMovieByFilename(context.Background(), "fish.mkv")
				if err != nil || stored == nil || stored.TitleUA != "Здоровий, як риба" {
					t.Errorf("success emitted before commit: movie=%+v err=%v", stored, err)
				}
			}
			ctx := context.Background()
			if fail {
				// Force a database error with a live context, independently of cancellation.
				if err := a.db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			count, err := a.saveLocalizationUpdates(ctx, []localizationUpdate{{Movie: storage.Movie{Filename: "fish.mkv", TmdbID: 1757519, MediaType: "movie", TitleUA: "Здоровий, як риба", TitleEN: "Sano come un pesce"}, TitleChanged: true}})
			if fail {
				if err == nil || count != 0 || len(messages) != 0 {
					t.Fatalf("failed commit reported success: count=%d err=%v messages=%v", count, err, messages)
				}
			} else if err != nil || count != 1 || len(messages) != 1 || !strings.Contains(messages[0], "Назву локалізовано") {
				t.Fatalf("committed update not reported: count=%d err=%v messages=%v", count, err, messages)
			}
		})
	}
}

func TestOriginalTitleAndTranslatedPlotMessage(t *testing.T) {
	message := localizationSavedMessage(localizationUpdate{Movie: storage.Movie{TitleUA: "Sano come un pesce", TitleEN: "Sano come un pesce"}, PlotChanged: true})
	if !strings.Contains(message, "Опис перекладено") || !strings.Contains(message, "оригінальну назву збережено") || strings.Contains(message, "Назву локалізовано") {
		t.Fatalf("plot-only update described as title translation: %s", message)
	}
}
