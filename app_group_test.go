package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDetectGroupedTVStandaloneEpisodes(t *testing.T) {
	dir := filepath.Join("shows", "Bjuro legend")
	paths := []string{filepath.Join(dir, "Bjuro legend.07.WEB.mkv"), filepath.Join(dir, "Bjuro legend.08.WEB.mkv"), filepath.Join("films", "Movie.2024.1080p.x264.mkv")}
	got := detectGroupedTV(context.Background(), paths)
	if !got[dir] {
		t.Fatalf("series folder not detected: %#v", got)
	}
	if got[filepath.Join("films")] {
		t.Fatalf("year/resolution/codec detected as episodes: %#v", got)
	}
}

func TestDetectGroupedTVSingleReleaseEpisode(t *testing.T) {
	dir := filepath.Join("shows", "Bjuro legend.HDTVRip.GeneralFilm")
	got := detectGroupedTV(context.Background(), []string{filepath.Join(dir, "Bjuro legend.08.HDTVRip.GeneralFilm.avi")})
	if !got[dir] {
		t.Fatalf("standalone episode did not provide TV preference: %#v", got)
	}
}

func TestDetectGroupedTVCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := detectGroupedTV(ctx, []string{"show/Show.01.mkv", "show/Show.02.mkv"}); len(got) != 0 {
		t.Fatalf("cancelled detection returned %#v", got)
	}
}
