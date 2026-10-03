package main

import (
	"context"
	"movielist-app/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvEditorAtomicSaveValidationAndConflict(t *testing.T) {
	a := NewApp()
	a.ctx = context.Background()
	a.cfg = &config.Config{EnvPath: filepath.Join(t.TempDir(), ".env"), GrokModel: "active-model"}
	t.Setenv("GROK_MODEL", "active-model")
	doc, err := a.GetEnvConfig()
	if err != nil || doc.Content != "" {
		t.Fatalf("missing file: %v", err)
	}
	content := "# Keep comment\r\nGROK_MODEL=new-model\r\nAPI_KEY=private-value\r\n"
	revision, err := a.SaveEnvConfig(content, doc.Revision)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(a.cfg.EnvPath)
	if string(data) != strings.ReplaceAll(content, "\r\n", "\n") {
		t.Fatal("text or comments changed")
	}
	if a.cfg.GrokModel != "active-model" || os.Getenv("GROK_MODEL") != "active-model" {
		t.Fatal("save changed live configuration")
	}
	if _, err := a.SaveEnvConfig("API_KEY=\"private-invalid", revision); err == nil || strings.Contains(err.Error(), "private-invalid") {
		t.Fatal("invalid syntax accepted or secret exposed")
	}
	unchanged, _ := os.ReadFile(a.cfg.EnvPath)
	if string(unchanged) != string(data) {
		t.Fatal("invalid input replaced file")
	}
	// A second save must replace the existing file atomically on Windows too.
	revision, err = a.SaveEnvConfig("# replacement\nGROK_MODEL=third\n", revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.cfg.EnvPath, []byte("EXTERNAL=yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveEnvConfig("EXTERNAL=no\n", revision); err == nil {
		t.Fatal("external change overwritten")
	}
	data, _ = os.ReadFile(a.cfg.EnvPath)
	if string(data) != "EXTERNAL=yes\n" {
		t.Fatal("external file lost")
	}
	entries, _ := os.ReadDir(filepath.Dir(a.cfg.EnvPath))
	if len(entries) != 1 {
		t.Fatal("temporary file left behind")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.ctx = ctx
	if _, err := a.SaveEnvConfig("EXTERNAL=no\n", revision); err == nil {
		t.Fatal("cancelled save accepted")
	}
}
