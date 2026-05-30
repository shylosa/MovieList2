package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"movielist-app/internal/config"
)

// TestCallGrok_HappyPath verifies that callGrok returns valid content
// when the server responds with a well-formed OpenAI-compatible JSON.
func TestCallGrok_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "[{\"id\":1,\"original_file\":\"Vrag.2013.mkv\",\"en_title\":\"Enemy\",\"media_type\":\"movie\",\"confidence\":0.95}]"
				}
			}]
		}`))
	}))
	defer server.Close()

	client := &Client{
		cfg:            &config.Config{GrokAPIKey: "test-key"},
		grokHTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: &grokTestTransport{serverURL: server.URL}},
	}

	raw, err := client.callGrok(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify the raw content is valid JSON array
	var results []RecognizedTitle
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		t.Fatalf("returned content is not valid JSON: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ENTitle != "Enemy" {
		t.Errorf("expected en_title 'Enemy', got '%s'", results[0].ENTitle)
	}
}

// TestCallGrok_ReasoningEffortNone verifies that the request body contains
// "reasoning_effort":"none" to prevent <think>...</think> blocks from grok-3-mini.
// Since callGrok does NOT strip thinking tokens, the fix relies entirely on
// sending reasoning_effort="none" in the API request.
func TestCallGrok_ReasoningEffortNone(t *testing.T) {
	var capturedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "[{\"id\":1,\"original_file\":\"test.mkv\",\"en_title\":\"Test\",\"media_type\":\"movie\",\"confidence\":0.9}]"
				}
			}]
		}`))
	}))
	defer server.Close()

	client := &Client{
		cfg:            &config.Config{GrokAPIKey: "test-key"},
		grokHTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: &grokTestTransport{serverURL: server.URL}},
	}

	_, err := client.callGrok(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the request body contains reasoning_effort:"none"
	var reqBody grokRequest
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse captured request body: %v", err)
	}
	if reqBody.ReasoningEffort != "none" {
		t.Errorf("expected reasoning_effort='none', got '%s'", reqBody.ReasoningEffort)
	}
}

// TestCallGrok_Non200Error verifies that callGrok correctly parses error
// responses from the Grok API (e.g., HTTP 429 rate limit).
func TestCallGrok_Non200Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded","code":"rate_limit_exceeded"}}`))
	}))
	defer server.Close()

	client := &Client{
		cfg:            &config.Config{GrokAPIKey: "test-key"},
		grokHTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: &grokTestTransport{serverURL: server.URL}},
	}

	_, err := client.callGrok(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected error for HTTP 429, got nil")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("error should contain 'rate limit exceeded', got: %v", err)
	}
}

// TestCallGrok_EmptyAPIKey verifies that callGrok returns an error
// when the Grok API key is not configured (empty string).
func TestCallGrok_EmptyAPIKey(t *testing.T) {
	client := &Client{
		cfg:            &config.Config{GrokAPIKey: ""},
		grokHTTPClient: &http.Client{},
	}
	_, err := client.callGrok(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty API key, got nil")
	}
	if !strings.Contains(err.Error(), "API key not configured") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestCallGrok_CancelledContext verifies that callGrok returns an error
// when the context is already cancelled before the call.
func TestCallGrok_CancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server that hangs — should never be reached if ctx is pre-cancelled
		time.Sleep(5 * time.Second)
	}))
	defer server.Close()

	client := &Client{
		cfg: &config.Config{GrokAPIKey: "test-key"},
		grokHTTPClient: &http.Client{Timeout: 5 * time.Second,
			Transport: &grokTestTransport{serverURL: server.URL}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before the call

	_, err := client.callGrok(ctx, "test")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// grokTestTransport redirects requests to the local test server.
type grokTestTransport struct {
	serverURL string
}

func (t *grokTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the target URL with the test server URL, preserving headers and body
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, t.serverURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultTransport.RoundTrip(newReq)
}
