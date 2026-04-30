package scanner

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"movielist-app/internal/config"
)

type Scanner struct {
	cfg *config.Config
}

func NewScanner(cfg *config.Config) *Scanner {
	return &Scanner{cfg: cfg}
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".mov": true,
}

func (s *Scanner) GetDiskFiles() ([]string, error) {
	var results []string

	root := s.cfg.MediaFolderPath
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, err
	}

	excludeMap := make(map[string]bool)
	for _, folder := range s.cfg.ExcludeFolders {
		excludeMap[strings.ToLower(folder)] = true
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		name := entry.Name()
		fullPath := filepath.Join(root, name)

		if excludeMap[strings.ToLower(name)] {
			continue
		}

		if !entry.IsDir() {
			ext := strings.ToLower(filepath.Ext(name))
			if videoExts[ext] {
				results = append(results, fullPath)
			}
		} else {
			firstVideo := s.getFirstVideoInDir(fullPath)
			if firstVideo != "" {
				results = append(results, firstVideo)
			}
		}
	}

	log.Printf("✅ Всього знайдено файлів: %d", len(results))
	return results, nil
}

func (s *Scanner) getFirstVideoInDir(dirPath string) string {
	var largestVideo string
	var maxSize int64

	filepath.WalkDir(dirPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			ext := strings.ToLower(filepath.Ext(path))
			if videoExts[ext] {
				info, err := d.Info()
				if err == nil && info.Size() > maxSize {
					maxSize = info.Size()
					largestVideo = path
				}
			}
		}
		return nil
	})

	return largestVideo
}
