package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
)

func TestGeneratePosterSourcesAndStableIDs(t *testing.T) {
	movie := storage.Movie{
		Filename:        "Enemy/Enemy.2013.mkv",
		TitleEN:         "Enemy",
		PosterURL:       "https://image.tmdb.org/t/p/w500/remote.jpg",
		LocalPosterPath: `posters\local.jpg`,
		VoteAverage:     7.4,
		VoteCount:       120,
	}
	for _, tc := range []struct {
		name     string
		mobile   bool
		want     string
		dontWant string
	}{
		{name: "local", want: "posters/local.jpg", dontWant: movie.PosterURL},
		{name: "mobile", mobile: true, want: movie.PosterURL, dontWant: "posters/local.jpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.html")
			if err := Generate(&config.Config{AppVersion: "test", HTMLPath: path}, []storage.Movie{movie}, tc.mobile); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			html := string(content)
			if !strings.Contains(html, tc.want) || strings.Contains(html, tc.dontWant) {
				t.Fatalf("poster source mismatch: want %q and not %q", tc.want, tc.dontWant)
			}
			for _, id := range []string{`id="noResults"`, `id="filteredCount"`, `id="filteredNum"`} {
				if !strings.Contains(html, id) {
					t.Fatalf("missing stable HTML %s", id)
				}
			}
			if !strings.Contains(html, "★ 7.4") || !strings.Contains(html, "120 оцінок TMDB") {
				t.Fatal("TMDB rating missing from showcase")
			}
			if !strings.Contains(html, ".movie-list.list-mode .rating { justify-self: start; }") || !strings.Contains(html, "width: max-content") {
				t.Fatal("TMDB rating must remain compact in list mode")
			}
		})
	}
}
