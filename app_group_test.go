package main

import (
	"context"
	"path/filepath"
	"testing"

	"movielist-app/internal/tmdb"
)

func TestDetectGroupedTVAdjacentStandaloneEpisodes(t *testing.T) {
	dir := filepath.Join("shows", "Bjuro legend")
	seven := filepath.Join(dir, "Bjuro legend.07.1080p.WEB.mkv")
	eight := filepath.Join(dir, "Bjuro legend.08.1080p.WEB.mkv")
	movie := filepath.Join("films", "Movie.2024.1080p.x264.mkv")
	got := detectGroupedTV(context.Background(), []string{seven, eight, movie})
	for _, path := range []string{seven, eight} {
		if decision, ok := got[path]; !ok || decision.reason != "adjacent_episode_numbers" {
			t.Fatalf("%s did not get TV preference: %#v", path, got)
		}
	}
	if _, ok := got[movie]; ok {
		t.Fatalf("year/resolution/codec detected as episodes: %#v", got)
	}
}

func TestDetectGroupedTVSingleWeakNumberIsNotTV(t *testing.T) {
	tests := []string{
		filepath.Join("shows", "Bjuro legend.08.HDTVRip.GeneralFilm.avi"),
		filepath.Join("films", "Movie.Part.2.2024.mkv"),
		filepath.Join("films", "Superkopy.80.2026.WEB-DL.1080p.ELEKTRI4KA.mkv"),
	}
	for _, path := range tests {
		if got := detectGroupedTV(context.Background(), []string{path}); len(got) != 0 {
			t.Fatalf("single weak number in %q provided TV preference: %#v", path, got)
		}
	}
	parsed := tmdb.ParseFilename(tests[2])
	if parsed.MediaType != tmdb.MediaTypeMovie || parsed.CleanTitle != "Superkopy 80" || parsed.Year != 2026 {
		t.Fatalf("Superkopy parse = %#v", parsed)
	}
}

func TestDetectGroupedTVNonAdjacentAndTechnicalNumbers(t *testing.T) {
	dir := filepath.Join("media", "mixed")
	paths := []string{
		filepath.Join(dir, "Show.08.WEB.mkv"),
		filepath.Join(dir, "Show.80.WEB.mkv"),
		filepath.Join(dir, "Movie.2024.1080p.x264.5.1.mkv"),
		filepath.Join(dir, "Movie.2025.2160p.x265.7.1.mkv"),
	}
	if got := detectGroupedTV(context.Background(), paths); len(got) != 0 {
		t.Fatalf("non-adjacent or technical numbers provided TV preference: %#v", got)
	}
}

func TestDetectGroupedTVStrongMarkers(t *testing.T) {
	for _, name := range []string{"Show.S01E01.mkv", "Show Season 1.mkv", "Show Episode 3.mkv", "Шоу Сезон 1.mkv", "Шоу Серія 3.mkv"} {
		path := filepath.Join("shows", "Show", name)
		got := detectGroupedTV(context.Background(), []string{path})
		if decision, ok := got[path]; !ok || decision.reason != "strong_episode_marker" {
			t.Errorf("strong marker %q not detected: %#v", name, got)
		}
	}
}

func TestDetectGroupedTVDoesNotMarkUnrelatedFilesInMixedFolder(t *testing.T) {
	dir := filepath.Join("media", "mixed")
	eight := filepath.Join(dir, "Series.08.WEB.mkv")
	nine := filepath.Join(dir, "Series.09.WEB.mkv")
	movie := filepath.Join(dir, "Feature.80.2026.mkv")
	got := detectGroupedTV(context.Background(), []string{eight, nine, movie})
	if _, ok := got[eight]; !ok {
		t.Fatalf("episode 08 missing: %#v", got)
	}
	if _, ok := got[nine]; !ok {
		t.Fatalf("episode 09 missing: %#v", got)
	}
	if _, ok := got[movie]; ok {
		t.Fatalf("unrelated movie inherited TV preference: %#v", got)
	}
}

func TestMeaningfulSeriesParent(t *testing.T) {
	for _, parent := range []string{"", ".", "Фильмы", "Фільми", "Movies", "Video", "Media"} {
		if isMeaningfulSeriesParent(parent) {
			t.Errorf("service parent %q accepted", parent)
		}
	}
	if !isMeaningfulSeriesParent("Bjuro legend") {
		t.Error("real release parent rejected")
	}
}

func TestDetectGroupedTVCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := detectGroupedTV(ctx, []string{"show/Show.01.mkv", "show/Show.02.mkv"}); len(got) != 0 {
		t.Fatalf("cancelled detection returned %#v", got)
	}
}

func TestGroupedTVConflictSurvivesTMDBConversion(t *testing.T) {
	a := NewApp()
	results := make(chan scanResult, 1)
	results <- scanResult{
		fname:        "Superkopy.80.2026.mkv",
		info:         &tmdb.MovieInfo{TMDBID: 1002109, TitleEN: "Super Troopers 80", MediaType: tmdb.MediaTypeMovie},
		needsGemini:  false,
		needsReview:  true,
		reviewReason: groupedTVReviewReason,
	}
	close(results)
	movies, _, _ := a.processScanResults(context.Background(), results)
	if len(movies) != 1 || !movies[0].NeedsReview || movies[0].ReviewReason != groupedTVReviewReason {
		t.Fatalf("group conflict review lost: %+v", movies)
	}
}
