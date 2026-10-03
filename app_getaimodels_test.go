package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
)

type rewriteTransport struct {
	base   http.RoundTripper
	scheme string
	host   string
}

func TestGrokCatalogExplainsInvalidKeyWithoutExposingResponse(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()
	app.cfg = &config.Config{GrokAPIKey: "private-key", GrokModel: "grok-3-mini"}
	app.discoveredGrokModels = []string{"grok-3-mini"}
	app.aiModelsHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":"Incorrect API key provided: private-key"}`))}, nil
	})}
	catalog, err := app.GetGrokModelCatalog()
	if err == nil || !strings.Contains(err.Error(), "GROK_API_KEY") || !strings.Contains(err.Error(), "перезапустіть") {
		t.Fatalf("missing actionable error: %v", err)
	}
	if strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("remote response exposed: %v", err)
	}
	if len(catalog.Current) != 1 || catalog.Current[0] != "grok-3-mini" || len(app.discoveredGrokModels) != 1 {
		t.Fatal("failed catalog changed model selections")
	}
}

func TestModelSelectionsLocalAndCatalogFailurePreservesSelection(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx, app.db = ctx, db
	app.cfg = &config.Config{GeminiAPIKey: "fake", GrokAPIKey: "secret", GrokModel: "grok-3-mini", GeminiModels: []string{"gemini-2.5-flash", "gemini-2.5-pro"}}
	var calls atomic.Int32
	app.aiModelsHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Path != "/v1/language-models" {
			t.Errorf("path=%s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"id":"grok-3-mini","input_modalities":["text"],"output_modalities":["text"]},{"id":"grok-text","aliases":["grok-text-latest"],"input_modalities":["text","image"],"output_modalities":["text"]},{"id":"grok-image","input_modalities":["text"],"output_modalities":["image"]},{"id":"grok-reasoning-only","input_modalities":["text"],"output_modalities":["text"],"capabilities":{"reasoning_effort":["low","high"]}}]}`)), Header: make(http.Header)}, nil
	})}
	if len(app.GetModelSelections()["gemini"].Current) != 2 || calls.Load() != 0 {
		t.Fatal("opening settings called API")
	}
	if err := app.SetAIModels([]string{"gemini-2.5-pro", "gemini-2.5-flash"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("save called API")
	}
	catalog, err := app.GetGrokModelCatalog()
	if err != nil || len(catalog.Available) != 3 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	if err := app.SetProviderModels("grok", []string{"grok-text-latest", "grok-3-mini"}); err != nil {
		t.Fatal(err)
	}
	if err := app.SetProviderModels("grok", []string{"grok-image"}); err == nil {
		t.Fatal("accepted image generator")
	}
	if err := app.SetProviderModels("grok", nil); err == nil {
		t.Fatal("accepted empty cascade")
	}
	app.aiModelsHTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("secret")), Header: make(http.Header)}, nil
	})
	_, err = app.GetGrokModelCatalog()
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error=%v", err)
	}
	if got := app.configuredGrokModels(); len(got) != 2 || got[0] != "grok-text-latest" {
		t.Fatalf("selection lost: %v", got)
	}
	// An unavailable configured model can still be retained or reordered.
	app.discoveredGrokModels = nil
	if err := app.SetProviderModels("grok", []string{"grok-3-mini", "grok-text-latest"}); err != nil {
		t.Fatal(err)
	}
	app2 := NewApp()
	app2.ctx, app2.db, app2.cfg = ctx, db, app.cfg
	if got := app2.GetModelSelections()["grok"].Current; len(got) != 2 || got[0] != "grok-3-mini" {
		t.Fatalf("restart selection=%v", got)
	}
}

func TestAIModelCatalogSeparatesAndPersistsSelection(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-2.5-pro","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-embedding-001","supportedGenerationMethods":["embedContent"]}]}`)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	db, err := storage.New(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx, app.db = context.Background(), db
	app.cfg = &config.Config{GeminiAPIKey: "fake", GeminiModels: []string{"gemini-2.5-flash"}}
	app.aiModelsHTTPClient = &http.Client{Transport: &rewriteTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host}}
	catalog, err := app.GetAIModelCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Current) != 1 || catalog.Current[0] != "gemini-2.5-flash" || len(catalog.Available) != 2 {
		t.Fatalf("catalog=%+v", catalog)
	}
	if err := app.SetAIModels([]string{"gemini-2.5-pro"}); err != nil {
		t.Fatal(err)
	}
	if got := app.configuredGeminiModels(); len(got) != 1 || got[0] != "gemini-2.5-pro" {
		t.Fatalf("persisted=%v", got)
	}
}

func TestAIModelCatalogSeparatesCurrentAndAvailableAndPersistsSelection(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-2.5-pro","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-embedding-001","supportedGenerationMethods":["embedContent"]}]}`)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	db, err := storage.New(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx, app.db = ctx, db
	app.cfg = &config.Config{GeminiAPIKey: "fake", GeminiModels: []string{"gemini-2.5-flash"}}
	app.aiModelsHTTPClient = &http.Client{Transport: &rewriteTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host}}
	catalog, err := app.GetAIModelCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Current) != 1 || catalog.Current[0] != "gemini-2.5-flash" || len(catalog.Available) != 2 {
		t.Fatalf("catalog=%+v", catalog)
	}
	if err := app.SetAIModels([]string{"gemini-2.5-pro"}); err != nil {
		t.Fatal(err)
	}
	if got := app.configuredGeminiModels(); len(got) != 1 || got[0] != "gemini-2.5-pro" {
		t.Fatalf("persisted=%v", got)
	}
}

func (r *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = r.scheme
	newReq.URL.Host = r.host
	return r.base.RoundTrip(newReq)
}

func TestGetAIModels_ReturnsGeminiModels(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]},{"name":"models/other","supportedGenerationMethods":["generateContent"]}]}`)
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// safe to ignore: httptest.Server always provides a valid URL.
	u, _ := url.Parse(srv.URL)

	app := NewApp()
	app.cfg = &config.Config{GeminiAPIKey: "fake-key"}
	app.aiModelsHTTPClient = &http.Client{Transport: &rewriteTransport{base: http.DefaultTransport, scheme: u.Scheme, host: u.Host}}

	names, err := app.GetAIModels()
	if err != nil {
		t.Fatalf("GetAIModels error: %v", err)
	}

	found := false
	for _, n := range names {
		if n == "gemini-2.5-flash" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected gemini model in names: %v", names)
	}
}

func TestGeminiModelDiscoveryFilteringAndConfiguredOrder(t *testing.T) {
	discovered := map[string]bool{"gemini-flash-lite-latest": true, "gemini-2.5-flash": true}
	got := selectConfiguredGeminiModels([]string{"missing", "gemini-2.5-flash", "gemini-flash-lite-latest"}, discovered)
	want := []string{"gemini-2.5-flash", "gemini-flash-lite-latest"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("selected models = %v; want %v", got, want)
	}
	for _, tc := range []struct {
		name    string
		methods []string
		want    bool
	}{
		{"gemini-2.5-flash", []string{"generateContent"}, true},
		{"gemini-2.5-pro", []string{"generateContent"}, true},
		{"gemini-3.1-pro-preview", []string{"generateContent"}, false},
		{"gemini-2.5-flash-preview-tts", []string{"generateContent"}, false},
		{"gemini-embedding-001", []string{"embedContent"}, false},
		{"gemini-2.5-flash", []string{"countTokens"}, false},
	} {
		if got := isUsableGeminiModel(tc.name, tc.methods); got != tc.want {
			t.Errorf("isUsableGeminiModel(%q) = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestGetAIModels_GrokOnlyMode(t *testing.T) {
	app := NewApp()
	app.cfg = &config.Config{
		GeminiAPIKey: "", // Gemini відсутній
		GrokAPIKey:   "test-grok-key",
	}

	names, err := app.GetAIModels()
	if err != nil {
		t.Fatalf("expected no error in Grok-only mode, got: %v", err)
	}
	if len(names) != 1 || names[0] != "grok-3-mini" {
		t.Errorf("expected [grok-3-mini], got: %v", names)
	}
}

func TestGetAIModels_NoKeysConfigured(t *testing.T) {
	app := NewApp()
	app.cfg = &config.Config{
		GeminiAPIKey: "",
		GrokAPIKey:   "",
	}

	_, err := app.GetAIModels()
	if err == nil {
		t.Fatal("expected error when no AI keys configured, got nil")
	}
}

func TestGetAIModelsDiscoveryFailureUsesConfiguredFallback(t *testing.T) {
	app := NewApp()
	app.cfg = &config.Config{GeminiAPIKey: "fake", GeminiModels: []string{"gemini-2.5-pro", "gemini-2.5-flash"}}
	app.aiModelsHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})}
	names, err := app.GetAIModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "gemini-2.5-pro" || names[1] != "gemini-2.5-flash" {
		t.Fatalf("fallback models = %v", names)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
