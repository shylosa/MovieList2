package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

// Opt-in integration checks use actual TMDB data but never save recognition
// results or modify the library. Posters go to a temporary directory.
func TestChecklistLiveTMDB(t *testing.T) {
	if os.Getenv("MOVIELIST_LIVE_TMDB") != "1" {
		t.Skip("opt-in live TMDB check")
	}
	cfg := config.Load()
	if cfg.TMDBAPIKey == "" {
		t.Fatal("TMDB key unavailable")
	}
	cfg.PostersDir = t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := tmdb.NewClient(cfg)
	defer c.Close()
	for _, tt := range []struct {
		filename string
		id       int
	}{
		{"Tretyi_lishnyi_2012_BDRip_AVO_[TC]_by_Dalemake.avi", 72105},
		{"Dorozhnoe.prikljuchenie.1.2000.XviD.HDTVRip.avi", 9285},
	} {
		t.Run(tt.filename, func(t *testing.T) {
			got, err := c.FetchFromFilename(ctx, tt.filename)
			if err != nil {
				t.Fatalf("TMDB check failed: %v", err)
			}
			if got == nil || got.TMDBID != tt.id || got.MediaType != tmdb.MediaTypeMovie {
				t.Fatalf("unexpected result: %+v", got)
			}
			t.Logf("verified movie:%d via TMDB, no AI client", got.TMDBID)
		})
	}
	a := NewApp()
	a.cfg, a.tmdbClient = cfg, c
	for _, tt := range []struct {
		title string
		id    int
	}{
		{"Вы нам не подходите", 284725}, {"Вместе до конца", 241882},
	} {
		t.Run(tt.title, func(t *testing.T) {
			got := a.rescueEmptyGeminiWithFolder(ctx, filepath.Join(cfg.MediaFolderPath, tt.title, tt.title+" 1.WEB-DLRip.avi"))
			if got.TmdbID != tt.id || got.MediaType != "tv" {
				t.Fatalf("unexpected rescue: %+v", got)
			}
			t.Logf("verified rescue tv:%d", got.TmdbID)
		})
	}
}

// Explicitly opted-in maintenance calls the same validated Models API as UI.
// Only selected_gemini_models is written; movie records are untouched.
func TestChecklistLiveModelsCleanup(t *testing.T) {
	path := os.Getenv("MOVIELIST_CLEANUP_MODELS_DB")
	if path == "" {
		t.Skip("opt-in local Models API cleanup")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("absolute existing database path required")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	db, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a := NewApp()
	a.ctx, a.db, a.cfg = ctx, db, config.Load()
	current := a.configuredGeminiModels()
	selected := []string{}
	for _, model := range current {
		if strings.TrimPrefix(model, "models/") != "gemini-2.0-flash" {
			selected = append(selected, model)
		}
	}
	if len(selected) == len(current) {
		t.Logf("obsolete model absent; selected=%v", current)
		return
	}
	if len(selected) == 0 {
		selected = []string{"gemini-2.5-flash", "gemini-flash-lite-latest"}
	}
	if err := a.SetAIModels(selected); err != nil {
		t.Fatalf("Models API cleanup failed: %v", err)
	}
	var persisted []string
	if err := json.Unmarshal([]byte(db.GetState(ctx, "selected_gemini_models")), &persisted); err != nil {
		t.Fatal(err)
	}
	for _, model := range persisted {
		if strings.TrimPrefix(model, "models/") == "gemini-2.0-flash" {
			t.Fatal("obsolete model persisted")
		}
	}
	t.Logf("verified persisted Models API selection=%v", persisted)
}
