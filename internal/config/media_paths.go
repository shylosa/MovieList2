package config

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

func (c *Config) ScanRoots() []string {
	if c.MediaFolders != nil {
		return c.MediaFolders
	}
	if c.MediaFolderPath == "" {
		return nil
	}
	return []string{c.MediaFolderPath}
}

func PathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (c *Config) RootForPath(path string) string {
	roots := append(append([]string{}, c.ScanRoots()...), c.MediaSources...)
	for _, root := range roots {
		if PathWithin(root, path) {
			return root
		}
	}
	return c.MediaFolderPath
}

func sourcePrefix(root string) string {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return fmt.Sprintf("source:%x/", sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root)))))
}

// Existing keys remain relative to the original root. Additional sources use
// a stable namespace followed by the complete relative media path.
func (c *Config) MediaIdentifier(path string) string {
	if c.MediaFolderPath != "" && PathWithin(c.MediaFolderPath, path) {
		rel, _ := filepath.Rel(c.MediaFolderPath, path)
		return filepath.ToSlash(rel)
	}
	for _, root := range c.ScanRoots() {
		if PathWithin(root, path) {
			rel, _ := filepath.Rel(root, path)
			return sourcePrefix(root) + filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

func (c *Config) ResolveMediaPath(identifier string) string {
	roots := append(append([]string{}, c.ScanRoots()...), c.MediaSources...)
	for _, root := range roots {
		prefix := sourcePrefix(root)
		if strings.HasPrefix(identifier, prefix) {
			return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(identifier, prefix)))
		}
	}
	return filepath.Join(c.MediaFolderPath, filepath.FromSlash(identifier))
}
