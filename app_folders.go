package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"movielist-app/internal/config"
)

// ScanFolders describes the active sources and excluded paths or legacy names.
type ScanFolders struct {
	Root     string   `json:"root"`
	Folders  []string `json:"folders"`
	Excluded []string `json:"excluded"`
}

type savedScanFolders struct {
	Root    string   `json:"root"`
	Folders []string `json:"folders"`
	Sources []string `json:"sources"`
}

func (a *App) GetScanFolders() ScanFolders {
	a.scanMutex.Lock()
	defer a.scanMutex.Unlock()
	return ScanFolders{Root: a.cfg.MediaFolderPath, Folders: append([]string{}, a.cfg.ScanRoots()...), Excluded: append([]string{}, a.cfg.ExcludeFolders...)}
}

func (a *App) SetScanFolders(folders []string) error {
	a.scanMutex.Lock()
	defer a.scanMutex.Unlock()
	if a.isScanning {
		return fmt.Errorf("Зміна папок недоступна під час сканування або виправлення")
	}
	clean := make([]string, 0, len(folders))
	for _, folder := range folders {
		if err := a.ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(folder) == "" {
			return fmt.Errorf("Порожній шлях папки")
		}
		path, err := filepath.Abs(folder)
		if err != nil {
			return err
		}
		known := false
		for _, old := range a.cfg.ScanRoots() {
			if strings.EqualFold(filepath.Clean(old), path) {
				known = true
				break
			}
		}
		if !known {
			info, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("Папка недоступна: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("Виберіть папку: %s", path)
			}
		}
		duplicate := false
		for _, old := range clean {
			if strings.EqualFold(old, path) {
				duplicate = true
				break
			}
			if config.PathWithin(old, path) || config.PathWithin(path, old) {
				return fmt.Errorf("Папки для сканування не можуть бути вкладеними одна в одну")
			}
		}
		if !duplicate {
			clean = append(clean, path)
		}
	}
	anchor := a.cfg.MediaFolderPath
	if anchor != "" {
		absolute, err := filepath.Abs(anchor)
		if err != nil {
			return err
		}
		anchor = absolute
	}
	if anchor == "" && len(clean) > 0 {
		anchor = clean[0]
	}
	sources := append([]string{}, a.cfg.MediaSources...)
	for _, root := range clean {
		found := false
		for _, old := range sources {
			if strings.EqualFold(old, root) {
				found = true
				break
			}
		}
		if !found {
			sources = append(sources, root)
		}
	}
	data, err := json.Marshal(savedScanFolders{Root: anchor, Folders: clean, Sources: sources})
	if err != nil {
		return err
	}
	if err := a.db.SetState(a.ctx, "scan_folders", string(data)); err != nil {
		return err
	}
	a.cfg.MediaFolders = clean
	a.cfg.MediaSources = sources
	a.cfg.MediaFolderPath = anchor
	if a.tmdbClient != nil {
		a.tmdbClient.SetMediaRoots(anchor, append(append([]string{}, clean...), sources...))
	}
	return nil
}

func (a *App) SelectScanFolder() error {
	path, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{Title: "Додати папку для сканування"})
	if err != nil || path == "" {
		return err
	}
	return a.SetScanFolders(append(a.GetScanFolders().Folders, path))
}

func (a *App) SelectExcludedFolder() error {
	path, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{Title: "Додати виключену папку"})
	if err != nil || path == "" {
		return err
	}
	return a.SetExcludedFolders(append(a.GetScanFolders().Excluded, path))
}

func (a *App) restoreScanFolders() {
	data := a.db.GetState(a.ctx, "scan_folders")
	if data == "" {
		return
	}
	var state savedScanFolders
	if json.Unmarshal([]byte(data), &state) == nil && state.Folders != nil {
		a.cfg.MediaFolderPath = state.Root
		a.cfg.MediaFolders = state.Folders
		a.cfg.MediaSources = state.Sources
	}
}

func (a *App) SetExcludedFolders(folders []string) error {
	a.scanMutex.Lock()
	defer a.scanMutex.Unlock()
	if a.isScanning {
		return fmt.Errorf("Зміна виключень недоступна під час сканування або виправлення")
	}
	clean := make([]string, 0, len(folders))
	seen := make(map[string]bool)
	for _, folder := range folders {
		if err := a.ctx.Err(); err != nil {
			return err
		}
		folder = strings.TrimSpace(folder)
		if folder == "" {
			continue
		}
		if strings.ContainsRune(folder, '\x00') {
			return fmt.Errorf("Некоректна назва папки")
		}
		if filepath.IsAbs(folder) {
			folder = filepath.Clean(folder)
		} else if folder == "." || folder == ".." || strings.ContainsAny(folder, "/\\:\x00") {
			return fmt.Errorf("Вкажіть назву папки першого рівня без шляху: %q", folder)
		}
		key := strings.ToLower(folder)
		if !seen[key] {
			clean = append(clean, folder)
			seen[key] = true
		}
	}
	data, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	if err := a.db.SetState(a.ctx, "excluded_folders", string(data)); err != nil {
		return err
	}
	a.cfg.ExcludeFolders = clean
	return nil
}

func (a *App) restoreExcludedFolders() {
	data := a.db.GetState(a.ctx, "excluded_folders")
	if data == "" {
		return
	}
	var folders []string
	if json.Unmarshal([]byte(data), &folders) == nil {
		a.cfg.ExcludeFolders = folders
	}
}
