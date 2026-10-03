package scanner

import (
	"context"
	"fmt"
	"log/slog"
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

func (s *Scanner) GetDiskFiles(ctx context.Context) ([]string, error) {
	var results []string
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	roots := s.cfg.ScanRoots()
	if len(roots) == 0 {
		return nil, fmt.Errorf("Не вибрано жодної папки для сканування")
	}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		files, err := s.scanRoot(ctx, root)
		if err != nil {
			return nil, err
		}
		results = append(results, files...)
	}
	slog.Info("disk_scan_completed", slog.Int("total_files", len(results)))
	return results, nil
}

func (s *Scanner) excluded(path string, matchNames bool) bool {
	for _, folder := range s.cfg.ExcludeFolders {
		if filepath.IsAbs(folder) {
			if config.PathWithin(folder, path) {
				return true
			}
		} else if matchNames && strings.EqualFold(folder, filepath.Base(path)) {
			return true
		}
	}
	return false
}

func (s *Scanner) scanRoot(ctx context.Context, root string) ([]string, error) {
	var results []string
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, err
	}
	if s.excluded(root, false) {
		return results, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		fullPath := filepath.Join(root, name)

		if s.excluded(fullPath, true) {
			continue
		}

		if !entry.IsDir() {
			ext := strings.ToLower(filepath.Ext(name))
			if videoExts[ext] {
				results = append(results, fullPath)
			}
		} else {
			largestVideo, err := s.getLargestVideoInDir(ctx, fullPath)
			if err != nil {
				return nil, fmt.Errorf("scan directory %q: %w", fullPath, err)
			}
			if largestVideo != "" {
				results = append(results, largestVideo)
			}
		}
	}

	return results, nil
}

func (s *Scanner) getLargestVideoInDir(ctx context.Context, dirPath string) (string, error) {
	var largestVideo string
	var maxSize int64

	err := filepath.WalkDir(dirPath, func(path string, d os.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if d.IsDir() && s.excluded(path, false) {
			return filepath.SkipDir
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
	if err != nil {
		slog.Warn("walkdir_error", slog.String("dir", dirPath), slog.Any("error", err))
		return "", err
	}

	return largestVideo, nil
}
