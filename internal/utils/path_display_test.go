package utils

import (
	"testing"
)

func TestDisplayFileLabel(t *testing.T) {
	tests := []struct {
		name         string
		relativePath string
		mediaType    string
		expected     string
	}{
		{
			name:         "Movie in nested directories",
			relativePath: "Movies/2024/Inception.mkv",
			mediaType:    "movie",
			expected:     "Inception.mkv",
		},
		{
			name:         "Movie flat in root",
			relativePath: "Inception.mkv",
			mediaType:    "movie",
			expected:     "Inception.mkv",
		},
		{
			name:         "TV in nested season subdirectory",
			relativePath: "Breaking Bad/Season 1/S01E01.mkv",
			mediaType:    "tv",
			expected:     "Breaking Bad",
		},
		{
			name:         "TV directly inside TV folder",
			relativePath: "Breaking Bad/S01E01.mkv",
			mediaType:    "tv",
			expected:     "Breaking Bad",
		},
		{
			name:         "TV in root",
			relativePath: "The.Show.S01E01.mkv",
			mediaType:    "tv",
			expected:     "The.Show.S01E01.mkv",
		},
		{
			name:         "Unresolved movie (no mediaType)",
			relativePath: "Movies/SomeMovie.mkv",
			mediaType:    "",
			expected:     "SomeMovie.mkv",
		},
		{
			name:         "Unresolved TV (no mediaType) identified by S01E01",
			relativePath: "Stranger Things/S01E01.mkv",
			mediaType:    "",
			expected:     "Stranger Things",
		},
		{
			name:         "Unresolved TV (no mediaType) identified by Season",
			relativePath: "Stranger Things/Season 3/episode.mkv",
			mediaType:    "",
			expected:     "Stranger Things",
		},
		{
			name:         "Unresolved TV in root (no mediaType)",
			relativePath: "Stranger.Things.S01E01.mkv",
			mediaType:    "",
			expected:     "Stranger.Things.S01E01.mkv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := DisplayFileLabel(tt.relativePath, tt.mediaType)
			if actual != tt.expected {
				t.Errorf("DisplayFileLabel(%q, %q) = %q; expected %q", tt.relativePath, tt.mediaType, actual, tt.expected)
			}
		})
	}
}
