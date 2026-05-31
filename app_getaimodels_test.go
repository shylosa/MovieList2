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
		io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash"},{"name":"models/other"}]}`)
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
