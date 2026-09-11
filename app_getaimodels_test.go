package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"movielist-app/internal/config"
)

type rewriteTransport struct {
	base   http.RoundTripper
	scheme string
	host   string
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
		{"gemini-2.5-pro", []string{"generateContent"}, false},
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
	if len(names) != 1 || names[0] != "gemini-2.5-flash" {
		t.Fatalf("fallback models = %v", names)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
