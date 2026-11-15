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
	EnvPath            string
	LogLevel           string
	GithubName         string
	GithubURL          string
	GithubPageURL      string
	MediaFolderPath    string
	MediaFolders       []string
	MediaSources       []string
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
	GrokModel          string
	GroqAPIKey         string
	GroqModel          string
	GitHubPagesBranch  string
}

// Load зчитує .env та заповнює структуру Config
func Load() *Config {
	envPath, _ := filepath.Abs(".env")
	// 1. Отримуємо точний шлях до нашого .exe файлу
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		// Шукаємо .env поруч з .exe
		candidate := filepath.Join(exeDir, ".env")

		// Спробуємо завантажити його звідти
		if err := godotenv.Load(candidate); err != nil {
			// Якщо не вийшло (наприклад, при wails dev), пробуємо стандартний fallback
			_ = godotenv.Load(".env")
			if _, err := os.Stat(envPath); os.IsNotExist(err) {
				envPath = candidate
			}
		} else {
			envPath = candidate
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

	modelsRaw := getEnvOrDefault("GEMINI_MODELS", "gemini-2.5-flash,gemini-flash-lite-latest")
	var modelsList []string
	for _, m := range strings.Split(modelsRaw, ",") {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			modelsList = append(modelsList, trimmed)
		}
	}

	// Налаштування середовища; версія збірки зберігається окремо.
	groqKey := os.Getenv("GROQ_API_KEY")
	// Compatibility for the Groq credential previously stored under GROK_API_KEY.
	if groqKey == "" && strings.HasPrefix(os.Getenv("GROK_API_KEY"), "gsk_") {
		groqKey = os.Getenv("GROK_API_KEY")
	}
	return &Config{
		EnvPath:            envPath,
		LogLevel:           getEnvOrDefault("LOG_LEVEL", "info"),
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
		GoogleSheetURL:     getEnvOrDefault("GOOGLE_SHEET_URL", ""),
		SheetWorksheetName: getEnvOrDefault("GOOGLE_SHEET_WORKSHEET_NAME", "base"),
		GrokAPIKey:         os.Getenv("GROK_API_KEY"),
		GrokModel:          getEnvOrDefault("GROK_MODEL", "grok-3-mini"),
		GroqAPIKey:         groqKey,
		GroqModel:          getEnvOrDefault("GROQ_MODEL", "openai/gpt-oss-120b"),
		GitHubPagesBranch:  getEnvOrDefault("GITHUB_PAGES_BRANCH", "main"),
	}
}

// getEnvOrDefault дістає значення або повертає передане дефолтне
func getEnvOrDefault(key, fallback string) string {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val
}
