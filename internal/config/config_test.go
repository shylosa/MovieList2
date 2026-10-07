package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var configEnvKeys = []string{
	"APP_VERSION", "GITHUB_NAME", "GITHUB_URL", "GITHUB_PAGE_URL", "MEDIA_FOLDER_PATH",
	"EXCLUDE_FOLDERS", "GEMINI_API_KEY", "GEMINI_MODELS", "TMDB_API_KEY", "DB_PATH",
	"HTML_PATH", "POSTERS_DIR", "GOOGLE_SHEET_URL", "GOOGLE_SHEET_WORKSHEET_NAME",
	"GROK_API_KEY", "GROK_MODEL", "GITHUB_PAGES_BRANCH",
	"GROQ_API_KEY", "GROQ_MODEL", "LOG_LEVEL",
}

func TestLoadGroqSettingsAndLegacyKeyCompatibility(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GROK_API_KEY", "gsk_legacy")
	if cfg := Load(); cfg.GroqAPIKey != "gsk_legacy" || cfg.GroqModel != "openai/gpt-oss-120b" {
		t.Fatal("Groq legacy key compatibility failed")
	}
	t.Setenv("GROQ_API_KEY", "gsk_current")
	t.Setenv("GROQ_MODEL", "openai/gpt-oss-20b")
	if cfg := Load(); cfg.GroqAPIKey != "gsk_current" || cfg.GroqModel != "openai/gpt-oss-20b" {
		t.Fatal("Groq settings not applied")
	}
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("GROK_API_KEY", "xai-real")
	if cfg := Load(); cfg.GroqAPIKey != "" {
		t.Fatal("xAI credential must not be used for Groq")
	}
}

func TestLoadUsesFallbackEnvAsEditorPath(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("# config for editor\nGROK_MODEL=test-model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Load().EnvPath; got != path {
		t.Fatalf("editor path = %q; want %q", got, path)
	}
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
	if cfg.LogLevel != "info" {
		t.Fatalf("default LogLevel = %q; want info", cfg.LogLevel)
	}
	if cfg.DBPath != "movies.db" || cfg.HTMLPath != "local_index.html" || cfg.PostersDir != "posters" {
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
	t.Setenv("LOG_LEVEL", "debug")
	cfg := Load()
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel override = %q; want debug", cfg.LogLevel)
	}
	if cfg.GeminiAPIKey != "test-gemini" || cfg.GrokAPIKey != "test-grok" || cfg.GitHubPagesBranch != "pages" {
		t.Fatalf("environment overrides not applied: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.ExcludeFolders, []string{"cache", "trailers", "samples"}) {
		t.Fatalf("ExcludeFolders = %v", cfg.ExcludeFolders)
	}
	if !reflect.DeepEqual(cfg.GeminiModels, []string{"model-a", "model-b"}) {
		t.Fatalf("GeminiModels = %v", cfg.GeminiModels)
	}
}
