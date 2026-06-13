package utils

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reSeason  = regexp.MustCompile(`(?i)\bS\d{2}\b|\bSeason\s*\d+\b|\bсезон\b`)
	reEpisode = regexp.MustCompile(`(?i)\bs\d{2}e\d{2}\b|\b\d+x\d+\b`)
)

// DisplayFileLabel returns a human-friendly file label for UI/export.
// relativePath — значення movies.filename; mediaType — "movie" | "tv" | "".
func DisplayFileLabel(relativePath, mediaType string) string {
	cleanPath := filepath.ToSlash(relativePath)

	effectiveType := mediaType
	if effectiveType == "" {
		if reSeason.MatchString(cleanPath) || reEpisode.MatchString(cleanPath) {
			effectiveType = "tv"
		}
	}

	if effectiveType != "tv" {
		return filepath.Base(cleanPath)
	}

	parts := strings.Split(cleanPath, "/")
	if len(parts) >= 2 {
		return parts[0] // каталог серіалу (перша папка відносно media folder path)
	}
	return filepath.Base(cleanPath) // fallback
}
