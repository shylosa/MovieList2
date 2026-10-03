package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/scanner"
	"movielist-app/internal/storage"
)

func TestExcludedFoldersPersistAndAffectScan(t *testing.T) {
	a, dir := newTestAppDB(t, nil)
	a.cfg.MediaFolderPath = dir
	for _, name := range []string{"Keep", "Skip"} {
		path := filepath.Join(dir, name)
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "film.mkv"), []byte("video"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SetExcludedFolders([]string{" Skip ", "skip", ""}); err != nil {
		t.Fatal(err)
	}
	state := a.GetScanFolders()
	state.Excluded[0] = "Keep"
	if a.cfg.ExcludeFolders[0] != "Skip" {
		t.Fatal("getter leaked mutable config")
	}
	a.cfg = &config.Config{MediaFolderPath: dir}
	a.restoreExcludedFolders()
	if !reflect.DeepEqual(a.cfg.ExcludeFolders, []string{"Skip"}) {
		t.Fatalf("restored: %v", a.cfg.ExcludeFolders)
	}
	files, err := scanner.NewScanner(a.cfg).GetDiskFiles(a.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != filepath.Join(dir, "Keep", "film.mkv") {
		t.Fatalf("files: %v", files)
	}
	if err := a.SetExcludedFolders(nil); err != nil {
		t.Fatal(err)
	}
	a.cfg.ExcludeFolders = []string{"env-default"}
	a.restoreExcludedFolders()
	if len(a.cfg.ExcludeFolders) != 0 {
		t.Fatal("empty saved selection did not override defaults")
	}
}

func TestExcludedFoldersRejectInvalidAndActiveChanges(t *testing.T) {
	a, _ := newTestAppDB(t, nil)
	if err := a.SetExcludedFolders([]string{"Original"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../other", ".", "..", "parent/child", "bad\x00name"} {
		if err := a.SetExcludedFolders([]string{name}); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	a.isScanning = true
	if err := a.SetExcludedFolders([]string{"Changed"}); err == nil {
		t.Fatal("accepted change during scan")
	}
	a.isScanning = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.ctx = ctx
	if err := a.SetExcludedFolders([]string{"Changed"}); err == nil {
		t.Fatal("accepted cancelled change")
	}
	a.ctx = context.Background()
	a.restoreExcludedFolders()
	if !reflect.DeepEqual(a.GetScanFolders().Excluded, []string{"Original"}) {
		t.Fatal("failed request changed saved settings")
	}
}

func TestScanFoldersPersistRemoveAndProtectChanges(t *testing.T) {
	a, dir := newTestAppDB(t, nil)
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, root := range []string{first, second} {
		if err := os.Mkdir(root, 0755); err != nil {
			t.Fatal(err)
		}
	}
	a.cfg.MediaFolderPath = first
	if err := a.SetScanFolders([]string{first, second, second}); err != nil {
		t.Fatal(err)
	}
	if len(a.GetScanFolders().Folders) != 2 {
		t.Fatal("duplicate root")
	}
	key := a.getFileIdentifier(filepath.Join(second, "film.mkv"))
	if err := a.db.SaveMoviesBatch(a.ctx, []storage.Movie{{Filename: key}}); err != nil {
		t.Fatal(err)
	}
	movies, err := a.GetMovies()
	if err != nil || len(movies) != 1 || movies[0].FilePath != filepath.Join(second, "film.mkv") || movies[0].FileLabel != "film.mkv" {
		t.Fatalf("display mapping: %v %v", movies, err)
	}
	a.cfg.MediaFolders = nil
	a.restoreScanFolders()
	if len(a.cfg.ScanRoots()) != 2 {
		t.Fatal("sources not restored")
	}
	a.isScanning = true
	if err := a.SetScanFolders(nil); err == nil {
		t.Fatal("changed sources during scan")
	}
	a.isScanning = false
	if err := a.SetScanFolders([]string{dir, second}); err == nil {
		t.Fatal("accepted overlapping sources")
	}
	if err := a.SetScanFolders([]string{first, filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("accepted missing source")
	}
	if err := a.SetScanFolders([]string{second}); err != nil {
		t.Fatal(err)
	}
	if a.getFileIdentifier(filepath.Join(second, "film.mkv")) != key {
		t.Fatal("removal changed existing key")
	}
	if err := a.SetScanFolders(nil); err != nil {
		t.Fatal(err)
	}
	a.cfg.MediaFolders = nil
	a.restoreScanFolders()
	if len(a.cfg.ScanRoots()) != 0 {
		t.Fatal("empty source list reverted to default")
	}
	if _, err := scanner.NewScanner(a.cfg).GetDiskFiles(a.ctx); err == nil {
		t.Fatal("empty sources must stop before cleanup")
	}
}
