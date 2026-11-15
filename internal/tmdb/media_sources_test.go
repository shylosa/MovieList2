package tmdb

import (
	"path/filepath"
	"testing"
)

func TestAdditionalSourceIsParentRescueBoundary(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	c := &Client{}
	c.SetMediaRoots(first, []string{first, second})
	file := filepath.Join(second, "unknown.mkv")
	if root := c.mediaRootForFile(file); root != second || !isScanRootParent(file, root) {
		t.Fatalf("additional root treated as title: %s", root)
	}
	file = filepath.Join(second, "Actual title", "episode.mkv")
	if isScanRootParent(file, c.mediaRootForFile(file)) {
		t.Fatal("real parent title was suppressed")
	}
}
