package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func repairTestApp(t *testing.T, movie storage.Movie) *App {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMoviesBatch(context.Background(), []storage.Movie{movie}); err != nil {
		t.Fatal(err)
	}
	return &App{ctx: context.Background(), db: db}
}

func TestRepairMetadata(t *testing.T) {
	for _, mediaType := range []string{"movie", "tv"} {
		for _, scenario := range []struct {
			name, storedTitle, storedPlot, remoteTitle, remotePlot string
			wantTitle, wantPlot                                    string
			translate                                              bool
			failAI                                                 bool
		}{
			{"complete", "Український фільм", "Це український опис.", "Новий український фільм", "Новий український опис.", "Новий український фільм", "Новий український опис.", false, false},
			{"english title", "English", "Це український опис.", "English", "Це український опис.", "Український фільм", "Це український опис.", true, false},
			{"english plot", "Український фільм", "English plot", "Український фільм", "English plot", "Український фільм", "Перекладений український опис.", true, false},
			{"missing plot", "Український фільм", "", "Український фільм", "English plot", "Український фільм", "Перекладений український опис.", true, false},
			{"stored UA preferred over fallback", "Український фільм", "Це український опис.", "English", "Russian plot ы", "Український фільм", "Це український опис.", false, false},
			{"empty remote preserves existing", "Український фільм", "Це український опис.", "", "", "Український фільм", "Це український опис.", false, false},
			{"AI failure preserves values", "English", "Useful English plot", "English", "New English plot", "English", "Useful English plot", true, true},
			{"no source plot", "Український фільм", "", "Український фільм", "", "Український фільм", "", false, false},
		} {
			t.Run(mediaType+"/"+scenario.name, func(t *testing.T) {
				before := storage.Movie{Filename: "Folder/film.mkv", TmdbID: 42, MediaType: mediaType, TitleUA: scenario.storedTitle, Plot: scenario.storedPlot, Year: "2000", Cast: "Old Actor", RecognitionSource: "manual_id", RecognitionConfidence: .9, VerificationScore: .8, NeedsReview: true, ReviewReason: "test"}
				a := repairTestApp(t, before)
				a.repairDetailsFetcher = func(ctx context.Context, kind tmdb.MediaType, id int) (*tmdb.MovieInfo, error) {
					if id != 42 || string(kind) != mediaType {
						t.Fatal("identity changed")
					}
					return &tmdb.MovieInfo{TMDBID: id, MediaType: kind, TitleUA: scenario.remoteTitle, TitleEN: "Original", Plot: scenario.remotePlot, Year: "2001", Cast: "New Actor", Genres: "Бойовик і пригоди", VoteAverage: 8, VoteCount: 100}, nil
				}
				calls := 0
				a.repairTranslator = func(ctx context.Context, items []ai.BulkTranslateItem) ([]ai.BulkTranslateItem, error) {
					calls++
					if len(items) != 1 {
						t.Fatal("expected one batched translation")
					}
					item := items[0]
					if scenario.remoteTitle == "Український фільм" && item.Title != "" {
						t.Fatal("trusted title sent to AI")
					}
					if scenario.remotePlot == "Це український опис." && item.Plot != "" {
						t.Fatal("trusted plot sent to AI")
					}
					if scenario.failAI {
						return nil, errors.New("AI unavailable")
					}
					return []ai.BulkTranslateItem{{Filename: item.Filename, Title: "Український фільм", Plot: "Перекладений український опис."}}, nil
				}
				result, err := a.RepairMetadata(before.Filename)
				if err != nil {
					t.Fatal(err)
				}
				if (calls > 0) != scenario.translate {
					t.Fatalf("translation calls: %d", calls)
				}
				got, err := a.db.GetMovieByFilename(a.ctx, before.Filename)
				if err != nil {
					t.Fatal(err)
				}
				if got.TitleUA != scenario.wantTitle || got.Plot != scenario.wantPlot {
					t.Fatalf("unexpected localization: %+v", got)
				}
				if got.TmdbID != 42 || got.MediaType != mediaType || got.RecognitionSource != before.RecognitionSource || got.VerificationScore != before.VerificationScore || got.NeedsReview != before.NeedsReview || got.ReviewReason != before.ReviewReason {
					t.Fatalf("recognition changed: %+v", got)
				}
				if got.Year != "2001" || got.Cast != "New Actor" || got.VoteCount != 100 || result.Movie.FileLabel != "Folder" {
					t.Fatalf("metadata not refreshed: %+v", result)
				}
				if scenario.failAI && result.Warning == "" {
					t.Fatal("missing partial failure feedback")
				}
			})
		}
	}
}

func TestRepairMetadataFailuresAndConcurrentChange(t *testing.T) {
	for _, scenario := range []string{"missing id", "invalid type", "TMDB failure", "wrong entity", "concurrent identity", "concurrent metadata", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			before := storage.Movie{Filename: "film.mkv", TmdbID: 42, MediaType: "movie", TitleUA: "Український фільм", Plot: "Це український опис."}
			if scenario == "missing id" {
				before.TmdbID = 0
			}
			if scenario == "invalid type" {
				before.MediaType = ""
			}
			a := repairTestApp(t, before)
			calls := 0
			a.repairDetailsFetcher = func(ctx context.Context, kind tmdb.MediaType, id int) (*tmdb.MovieInfo, error) {
				calls++
				if scenario == "TMDB failure" {
					return nil, errors.New("offline")
				}
				if scenario == "concurrent identity" || scenario == "concurrent metadata" {
					current := before
					if scenario == "concurrent identity" {
						current.TmdbID = 99
					} else {
						current.Plot = "Інший український опис."
					}
					if err := a.db.SaveMoviesBatch(ctx, []storage.Movie{current}); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "cancelled" {
					a.repairCancel()
				}
				if scenario == "wrong entity" {
					id++
				}
				return &tmdb.MovieInfo{TMDBID: id, MediaType: kind, TitleUA: "Новий український фільм", Plot: "Новий український опис."}, nil
			}
			if _, err := a.RepairMetadata(before.Filename); err == nil {
				t.Fatal("expected safe failure")
			}
			if (scenario == "missing id" || scenario == "invalid type") && calls != 0 {
				t.Fatal("invalid identity made a network call")
			}
			got, _ := a.db.GetMovieByFilename(a.ctx, before.Filename)
			if scenario == "concurrent identity" {
				before.TmdbID = 99
			}
			if scenario == "concurrent metadata" {
				before.Plot = "Інший український опис."
			}
			if got.TmdbID != before.TmdbID || got.TitleUA != before.TitleUA || got.Plot != before.Plot || got.MediaType != before.MediaType {
				t.Fatalf("failed repair changed record: %+v", got)
			}
			if a.isScanning || a.repairCancel != nil {
				t.Fatal("operation guard not released")
			}
		})
	}
}

func TestRepairTranslationValidation(t *testing.T) {
	movie := storage.Movie{TitleUA: "English", Plot: "Keep existing plot"}
	request := ai.BulkTranslateItem{Filename: "a", Title: "English", OriginalTitle: "Original", Plot: "Source"}
	if applyRepairTranslation(&movie, request, []ai.BulkTranslateItem{{Filename: "a", Title: "Український фільм", Plot: "Русский сюжет ы"}}) {
		t.Fatal("invalid plot accepted")
	}
	if movie.TitleUA != "Український фільм" || movie.Plot != "Keep existing plot" {
		t.Fatalf("partial fields corrupted: %+v", movie)
	}
	if applyRepairTranslation(&movie, request, []ai.BulkTranslateItem{{Filename: "other", Plot: "Український опис"}}) {
		t.Fatal("wrong filename accepted")
	}
	if !applyRepairTranslation(&movie, ai.BulkTranslateItem{Filename: "a", Title: "Original", OriginalTitle: "Original"}, []ai.BulkTranslateItem{{Filename: "a", Title: "Original", Plot: "Invented plot"}}) {
		t.Fatal("original title rejected")
	}
	if movie.TitleUA != "Original" || movie.Plot != "Keep existing plot" {
		t.Fatal("unrequested plot changed")
	}
	for _, text := range []string{"<think>Український опис</think>", "```Український опис```", "English", "Русский текст ы", ""} {
		if validRepairLocalization(text) {
			t.Fatalf("invalid translation accepted: %q", text)
		}
	}
}

func TestRepairDuplicateAndShutdownCancellation(t *testing.T) {
	a := repairTestApp(t, storage.Movie{Filename: "a", TmdbID: 42, MediaType: "tv"})
	started := make(chan struct{})
	a.repairDetailsFetcher = func(ctx context.Context, kind tmdb.MediaType, id int) (*tmdb.MovieInfo, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := a.RepairMetadata("a"); done <- err }()
	<-started
	if _, err := a.RepairMetadata("a"); err == nil {
		t.Fatal("duplicate accepted")
	}
	a.shutdown(context.Background())
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown did not cancel repair: %v", err)
	}
	if _, err := a.RepairMetadata("a"); err == nil {
		t.Fatal("repair started after shutdown")
	}
}
