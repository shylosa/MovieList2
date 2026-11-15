package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"movielist-app/internal/config"
)

type groqTransport func(*http.Request) (*http.Response, error)

func (f groqTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGroqFallbackUsesSnapshotAndSupportedRequest(t *testing.T) {
	var requests []string
	client := &Client{cfg: &config.Config{GroqAPIKey: "gsk_test"}}
	client.groqHTTPClient = &http.Client{Transport: groqTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != groqChatURL || r.Header.Get("Authorization") != "Bearer gsk_test" {
			t.Fatal("incorrect endpoint or authorization")
		}
		var payload groqChatRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.ReasoningEffort != "low" {
			t.Fatalf("unsupported reasoning effort: %s", payload.ReasoningEffort)
		}
		if payload.MaxCompletionTokens != 4096 {
			t.Fatal("output token reservation must be bounded")
		}
		if !strings.Contains(payload.Messages[0].Content, "confidence is REQUIRED") {
			t.Fatal("recognition output contract missing")
		}
		requests = append(requests, payload.Model)
		content := "invalid JSON"
		if len(requests) == 2 {
			content = `[{"en_title":"Enemy","original_file":"Enemy.mkv","confidence":0.9}]`
		}
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	models := []string{"openai/gpt-oss-120b", "openai/gpt-oss-20b"}
	ctx := WithGroqModelSelection(WithModelSelection(context.Background(), nil, nil), models)
	models[0] = "changed"
	result, err := client.grokRecognizeFallback(ctx, "prompt")
	if err != nil || len(result) != 1 || result[0].Provider != "groq" || result[0].Model != "openai/gpt-oss-20b" || requests[0] != "openai/gpt-oss-120b" {
		t.Fatalf("result=%v requests=%v err=%v", result, requests, err)
	}
	if client.CallMetrics().Groq != 2 || client.CallMetrics().Grok != 0 {
		t.Fatal("incorrect provider metrics")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.grokTranslateFallback(cancelled, "prompt"); err != context.Canceled || len(requests) != 2 {
		t.Fatal("cancelled request reached provider")
	}
}

func TestGroqRecognitionRequiresExplicitConfidence(t *testing.T) {
	for _, raw := range []string{`[{"en_title":"The Matrix"}]`, `[{"confidence":null}]`, `[{"confidence":-1}]`, `[{"confidence":1.1}]`} {
		if _, err := parseGroqRecognizeResponse(raw); err == nil {
			t.Fatalf("accepted invalid confidence: %s", raw)
		}
	}
	if items, err := parseGroqRecognizeResponse(`[{"en_title":"","status":"unresolved","confidence":0}]`); err != nil || len(items) != 1 {
		t.Fatal("explicit uncertainty must remain valid")
	}
}

func TestGroqTextFiltering(t *testing.T) {
	for _, name := range []string{"openai/gpt-oss-120b", "openai/gpt-oss-20b", "llama-3.3-70b-versatile"} {
		if !IsGroqTextModel(name) {
			t.Errorf("rejected %s", name)
		}
	}
	for _, name := range []string{"whisper-large-v3", "groq/compound", "openai/gpt-oss-safeguard-20b", "meta-llama/llama-guard-4-12b", "canopylabs/orpheus-v1-english"} {
		if IsGroqTextModel(name) {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestGroqTranslationFallsBackAndKeepsRemoteErrorsPrivate(t *testing.T) {
	client := &Client{cfg: &config.Config{GroqAPIKey: "gsk_private"}}
	calls := 0
	client.groqHTTPClient = &http.Client{Transport: groqTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("gsk_private"))}, nil
		}
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `[{"id":"1","plot":"Опис українською"}]`}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	ctx := WithGroqModelSelection(context.Background(), []string{"openai/gpt-oss-120b", "openai/gpt-oss-20b"})
	items, err := client.grokTranslateFallback(ctx, "prompt")
	if err != nil || calls != 2 || len(items) != 1 || items[0].Plot != "Опис українською" {
		t.Fatalf("items=%v calls=%d err=%v", items, calls, err)
	}
	client.groqHTTPClient.Transport = groqTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("gsk_private"))}, nil
	})
	_, err = client.callGroq(ctx, "prompt", "openai/gpt-oss-120b")
	if err == nil || strings.Contains(err.Error(), "gsk_private") {
		t.Fatal("unsafe provider error")
	}
}
