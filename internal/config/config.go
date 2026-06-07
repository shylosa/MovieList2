package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// Config зберігає всі налаштування програми
type Config struct {
	AppVersion         string
	GithubName         string
	GithubURL          string
	GithubPageURL      string
	MediaFolderPath    string
	ExcludeFolders     []string
	GeminiAPIKey       string
	GeminiModels       []string
	TMDBAPIKey         string
	DBPath             string
	HTMLPath           string
	PostersDir         string
	GoogleSheetURL     string
	SheetWorksheetName string
	GrokAPIKey         string
	GitHubPagesBranch  string
}

// Load зчитує .env та заповнює структуру Config
func Load() *Config {
	// 1. Отримуємо точний шлях до нашого .exe файлу
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		// Шукаємо .env поруч з .exe
		envPath := filepath.Join(exeDir, ".env")

		// Спробуємо завантажити його звідти
		if err := godotenv.Load(envPath); err != nil {
			// Якщо не вийшло (наприклад, при wails dev), пробуємо стандартний fallback
			_ = godotenv.Load(".env")
		} else {
			slog.Info("env_loaded", slog.String("path", envPath))
		}
	} else {
		_ = godotenv.Load(".env")
	}

	// Парсимо список виключень
	excludeRaw := getEnvOrDefault("EXCLUDE_FOLDERS", "")
	var excludeList []string
	if excludeRaw != "" {
		for _, item := range strings.Split(excludeRaw, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				excludeList = append(excludeList, trimmed)
			}
		}
	}

	modelsRaw := getEnvOrDefault("GEMINI_MODELS", "gemini-2.5-flash,gemini-2.0-flash,gemini-2.5-flash-lite")
	var modelsList []string
	for _, m := range strings.Split(modelsRaw, ",") {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			modelsList = append(modelsList, trimmed)
		}
	}

	// Єдине джерело істини — змінні оточення або дефолти. Жодної магії в коді!
	return &Config{
		AppVersion:         getEnvOrDefault("APP_VERSION", "2.0"),
		GithubName:         getEnvOrDefault("GITHUB_NAME", "shylosa"),
		GithubURL:          getEnvOrDefault("GITHUB_URL", "https://github.com/shylosa/MovieList2"),
		GithubPageURL:      getEnvOrDefault("GITHUB_PAGE_URL", "https://shylosa.github.io/MovieList2"),
		MediaFolderPath:    getEnvOrDefault("MEDIA_FOLDER_PATH", ""),
		ExcludeFolders:     excludeList,
		GeminiAPIKey:       os.Getenv("GEMINI_API_KEY"),
		GeminiModels:       modelsList,
		TMDBAPIKey:         getEnvOrDefault("TMDB_API_KEY", ""),
		DBPath:             getEnvOrDefault("DB_PATH", "movies.db"),
		HTMLPath:           getEnvOrDefault("HTML_PATH", "local_index.html"),
		PostersDir:         getEnvOrDefault("POSTERS_DIR", "posters"),
		GoogleSheetURL:      getEnvOrDefault("GOOGLE_SHEET_URL", ""),
		SheetWorksheetName:  getEnvOrDefault("GOOGLE_SHEET_WORKSHEET_NAME", "base"),
		GrokAPIKey:          os.Getenv("GROK_API_KEY"),
		GitHubPagesBranch:   getEnvOrDefault("GITHUB_PAGES_BRANCH", "main"),
	}
}

// getEnvRequired дістає значення або "панікує", якщо критичний ключ відсутній
func getEnvRequired(key string) string {
	val := os.Getenv(key)
	if val == "" {
		slog.Error("missing_required_env", slog.String("key", key))
		panic("missing required env: " + key)
	}
	return val
}

// getEnvOrDefault дістає значення або повертає передане дефолтне
func getEnvOrDefault(key, fallback string) string {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val
}
