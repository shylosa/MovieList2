package scanner

import (
	"context"
	"movielist-app/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestMultipleRootsAndNestedAbsoluteExclusion(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	for _, name := range []string{"first/Film/keep.mkv", "first/Film/Skip/larger.mkv", "second/other.mkv"} {
		path := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{MediaFolderPath: first, MediaFolders: []string{first, second}, ExcludeFolders: []string{filepath.Join(first, "Film", "Skip")}}
	files, err := NewScanner(cfg).GetDiskFiles(context.Background())
	if err != nil || len(files) != 2 || files[0] != filepath.Join(first, "Film", "keep.mkv") || files[1] != filepath.Join(second, "other.mkv") {
		t.Fatalf("files=%v err=%v", files, err)
	}
	cfg.MediaFolders = append(cfg.MediaFolders, filepath.Join(base, "missing"))
	if files, err := NewScanner(cfg).GetDiskFiles(context.Background()); err == nil || files != nil {
		t.Fatalf("partial scan returned files: %v %v", files, err)
	}
}
