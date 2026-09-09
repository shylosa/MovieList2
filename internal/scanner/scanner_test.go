package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"movielist-app/internal/config"
)

func TestGetDiskFilesEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	s := NewScanner(&config.Config{MediaFolderPath: root})

	files, err := s.GetDiskFiles(context.Background())
	if err != nil {
		t.Fatalf("GetDiskFiles() error = %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("GetDiskFiles() returned %d files, want 0", len(files))
	}
}

func TestGetDiskFilesReturnsLargestVideoPerDirectory(t *testing.T) {
	root := t.TempDir()
	releaseDir := filepath.Join(root, "Release")
	if err := os.Mkdir(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(releaseDir, "sample.mkv")
	large := filepath.Join(releaseDir, "movie.mp4")
	if err := os.WriteFile(small, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(large, []byte("larger-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(&config.Config{MediaFolderPath: root})

	files, err := s.GetDiskFiles(context.Background())
	if err != nil {
		t.Fatalf("GetDiskFiles() error = %v", err)
	}
	if len(files) != 1 || files[0] != large {
		t.Fatalf("GetDiskFiles() = %v, want [%s]", files, large)
	}
}

func TestGetDiskFilesHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewScanner(&config.Config{MediaFolderPath: t.TempDir()})

	_, err := s.GetDiskFiles(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetDiskFiles() error = %v, want context.Canceled", err)
	}
}

func TestGetLargestVideoInDirReturnsWalkError(t *testing.T) {
	s := NewScanner(&config.Config{})
	missing := filepath.Join(t.TempDir(), "missing")

	_, err := s.getLargestVideoInDir(context.Background(), missing)
	if err == nil {
		t.Fatal("getLargestVideoInDir() error = nil, want walk error")
	}
}
