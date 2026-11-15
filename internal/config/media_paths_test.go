package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMultipleSourceKeysAndResolution(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	c := &Config{MediaFolderPath: first, MediaFolders: []string{first, second}}
	c.MediaSources = []string{first, second}
	paths := []string{filepath.Join(first, "Series", "episode.mkv"), filepath.Join(second, "Series", "episode.mkv")}
	keys := []string{c.MediaIdentifier(paths[0]), c.MediaIdentifier(paths[1])}
	if keys[0] != "Series/episode.mkv" || keys[0] == keys[1] || !strings.HasPrefix(keys[1], "source:") {
		t.Fatalf("keys: %v", keys)
	}
	for i, key := range keys {
		if c.ResolveMediaPath(key) != paths[i] {
			t.Fatalf("resolve %s", key)
		}
	}
	c.MediaFolders = []string{second}
	if c.MediaIdentifier(paths[1]) != keys[1] {
		t.Fatal("removing first source changed remaining keys")
	}
	if c.RootForPath(paths[1]) != second {
		t.Fatal("wrong recognition root")
	}
	c.MediaFolders = []string{}
	if c.ResolveMediaPath(keys[1]) != paths[1] {
		t.Fatal("removed source lost path before catalog cleanup")
	}
	if PathWithin(first, second) || PathWithin(first, filepath.Join(base, "first-other")) {
		t.Fatal("accepted sibling path")
	}
}
