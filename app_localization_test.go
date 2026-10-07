package main

import (
	"context"
	"strings"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
)

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
