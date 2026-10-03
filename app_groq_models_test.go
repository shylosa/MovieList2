package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/joho/godotenv"
	"movielist-app/internal/config"
)

func TestGroqCatalogUsesModelsDataAndKeepsSelectionOnFailure(t *testing.T) {
	a := NewApp()
	a.ctx = context.Background()
	a.cfg = &config.Config{GroqAPIKey: "gsk_fake", GroqModel: "openai/gpt-oss-120b"}
	a.aiModelsHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.groq.com/openai/v1/models" || r.Header.Get("Authorization") != "Bearer gsk_fake" || !strings.HasPrefix(r.Header.Get("User-Agent"), "MovieList/") {
			t.Fatal("incorrect catalog request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"openai/gpt-oss-120b","active":true},{"id":"openai/gpt-oss-20b","active":true},{"id":"whisper-large-v3","active":true},{"id":"llama-old","active":false}]}`))}, nil
	})}
	if got := a.GetModelSelections()["groq"]; len(got.Current) != 1 || !got.Configured {
		t.Fatal("local Groq selections missing")
	}
	catalog, err := a.GetGroqModelCatalog()
	if err != nil || len(catalog.Available) != 2 || catalog.Available[0] != "openai/gpt-oss-120b" {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	a.aiModelsHTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("gsk_fake"))}, nil
	})
	catalog, err = a.GetGroqModelCatalog()
	if err == nil || strings.Contains(err.Error(), "gsk_fake") || len(catalog.Current) != 1 || len(a.discoveredGroqModels) != 2 {
		t.Fatalf("unsafe failure=%v catalog=%v", err, catalog)
	}
}

func TestLiveGroqModelCatalog(t *testing.T) {
	if os.Getenv("MOVIELIST_LIVE_GROQ") != "1" {
		t.Skip("opt-in read-only Groq catalog")
	}
	env, err := godotenv.Read(".env")
	if err != nil {
		t.Fatal("cannot read local config")
	}
	key := env["GROQ_API_KEY"]
	if key == "" {
		t.Fatal("GROQ_API_KEY not configured")
	}
	a := NewApp()
	a.ctx = context.Background()
	a.cfg = &config.Config{GroqAPIKey: key, GroqModel: "openai/gpt-oss-120b"}
	catalog, err := a.GetGroqModelCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Available) == 0 {
		t.Fatal("no text models available")
	}
	t.Logf("available text models: %v", catalog.Available)
}
