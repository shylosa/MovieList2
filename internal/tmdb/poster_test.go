package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"movielist-app/internal/config"
)

func TestDownloadPosterAtomicSuccessAndCacheHit(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("complete-image"))
	}))
	defer server.Close()

	postersDir := t.TempDir()
	client := NewClient(&config.Config{PostersDir: postersDir})
	defer client.Close()

	path, err := client.DownloadPoster(context.Background(), server.URL, "movie")
	if err != nil {
		t.Fatalf("DownloadPoster() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete-image" {
		t.Fatalf("poster contents = %q", data)
	}

	cachedPath, err := client.DownloadPoster(context.Background(), server.URL, "movie")
	if err != nil {
		t.Fatalf("cached DownloadPoster() error = %v", err)
	}
	if cachedPath != path {
		t.Fatalf("cached path = %q, want %q", cachedPath, path)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1", got)
	}
}

func TestDownloadPosterRemovesPartialFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	}))
	defer server.Close()

	postersDir := t.TempDir()
	client := NewClient(&config.Config{PostersDir: postersDir})
	defer client.Close()

	_, err := client.DownloadPoster(context.Background(), server.URL, "broken")
	if err == nil {
		t.Fatal("DownloadPoster() error = nil, want truncated response error")
	}
	if _, statErr := os.Stat(filepath.Join(postersDir, "broken.jpg")); !os.IsNotExist(statErr) {
		t.Fatalf("final poster exists after failed download: %v", statErr)
	}
	temps, globErr := filepath.Glob(filepath.Join(postersDir, ".poster-*.tmp"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files remain after failed download: %v", temps)
	}
}

func TestDownloadPosterHonorsCancelledContext(t *testing.T) {
	postersDir := t.TempDir()
	client := NewClient(&config.Config{PostersDir: postersDir})
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.DownloadPoster(ctx, "https://example.invalid/poster.jpg", "cancelled")
	if err == nil {
		t.Fatal("DownloadPoster() error = nil, want cancellation error")
	}
	entries, readErr := os.ReadDir(postersDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("poster directory is not empty after cancellation: %v", entries)
	}
}
