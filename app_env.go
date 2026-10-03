package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/joho/godotenv"
)

type EnvDocument struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

func readEnvDocument(path string) (EnvDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return EnvDocument{}, fmt.Errorf("Не вдалося прочитати .env: %w", err)
	}
	if !utf8.Valid(data) {
		return EnvDocument{}, fmt.Errorf("Файл .env має бути у UTF-8")
	}
	return EnvDocument{Path: path, Content: strings.TrimPrefix(string(data), "\ufeff"), Revision: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}

func (a *App) GetEnvConfig() (EnvDocument, error) {
	a.configFileMutex.Lock()
	defer a.configFileMutex.Unlock()
	if err := a.ctx.Err(); err != nil {
		return EnvDocument{}, err
	}
	return readEnvDocument(a.cfg.EnvPath)
}

func (a *App) SaveEnvConfig(content, revision string) (string, error) {
	a.configFileMutex.Lock()
	defer a.configFileMutex.Unlock()
	if err := a.ctx.Err(); err != nil {
		return "", err
	}
	if len(content) > 1024*1024 || !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
		return "", fmt.Errorf("Некоректний вміст .env")
	}
	content = strings.TrimPrefix(strings.ReplaceAll(content, "\r\n", "\n"), "\ufeff")
	if _, err := godotenv.Unmarshal(content); err != nil {
		return "", fmt.Errorf("Некоректний синтаксис .env. Перевірте лапки та назви параметрів")
	}
	current, err := readEnvDocument(a.cfg.EnvPath)
	if err != nil {
		return "", err
	}
	if current.Revision != revision {
		return "", fmt.Errorf("Файл змінено поза програмою. Автозбереження зупинено, щоб не перезаписати зміни")
	}
	f, err := os.CreateTemp(filepath.Dir(a.cfg.EnvPath), ".env-*")
	if err != nil {
		return "", fmt.Errorf("Не вдалося створити файл для збереження: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if info, err := os.Stat(a.cfg.EnvPath); err == nil {
		if err := f.Chmod(info.Mode().Perm()); err != nil {
			return "", err
		}
	}
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := a.ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), a.cfg.EnvPath); err != nil {
		return "", fmt.Errorf("Не вдалося зберегти .env: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content))), nil
}
