package config

import (
	"reflect"
	"testing"
)

var configEnvKeys = []string{
	"APP_VERSION", "GITHUB_NAME", "GITHUB_URL", "GITHUB_PAGE_URL", "MEDIA_FOLDER_PATH",
	"EXCLUDE_FOLDERS", "GEMINI_API_KEY", "GEMINI_MODELS", "TMDB_API_KEY", "DB_PATH",
	"HTML_PATH", "POSTERS_DIR", "GOOGLE_SHEET_URL", "GOOGLE_SHEET_WORKSHEET_NAME",
	"GROK_API_KEY", "GROK_MODEL", "GITHUB_PAGES_BRANCH",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range configEnvKeys {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)
	cfg := Load()
	if cfg.AppVersion != "2.6.0" || cfg.DBPath != "movies.db" || cfg.HTMLPath != "local_index.html" || cfg.PostersDir != "posters" {
		t.Fatalf("unexpected path defaults: %+v", cfg)
	}
	if cfg.SheetWorksheetName != "base" || cfg.GrokModel != "grok-3-mini" || cfg.GitHubPagesBranch != "main" {
		t.Fatalf("unexpected service defaults: %+v", cfg)
	}
	wantModels := []string{"gemini-2.5-flash", "gemini-flash-lite-latest"}
	if !reflect.DeepEqual(cfg.GeminiModels, wantModels) {
		t.Fatalf("GeminiModels = %v; want %v", cfg.GeminiModels, wantModels)
	}
}

func TestLoadEnvironmentOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_VERSION", "9.9")
	t.Setenv("EXCLUDE_FOLDERS", " cache, trailers ,, samples ")
	t.Setenv("GEMINI_MODELS", " model-a, model-b ")
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GROK_API_KEY", "test-grok")
	t.Setenv("GITHUB_PAGES_BRANCH", "pages")
	cfg := Load()
	if cfg.AppVersion != "9.9" || cfg.GeminiAPIKey != "test-gemini" || cfg.GrokAPIKey != "test-grok" || cfg.GitHubPagesBranch != "pages" {
		t.Fatalf("environment overrides not applied: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.ExcludeFolders, []string{"cache", "trailers", "samples"}) {
		t.Fatalf("ExcludeFolders = %v", cfg.ExcludeFolders)
	}
	if !reflect.DeepEqual(cfg.GeminiModels, []string{"model-a", "model-b"}) {
		t.Fatalf("GeminiModels = %v", cfg.GeminiModels)
	}
}
