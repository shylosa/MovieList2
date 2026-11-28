package ai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"movielist-app/internal/config"

	"golang.org/x/time/rate"
)

func TestR5ReserveCorrelation(t *testing.T) {
	for _, provider := range []string{"groq", "grok"} {
		t.Run(provider, func(t *testing.T) {
			ctx := t.Context()
			client := NewClient(&config.Config{GeminiAPIKey: "fixture", GeminiModels: []string{"gemini-fixture-flash"}})
			client.limiter = rate.NewLimiter(rate.Inf, 1)
			client.grokLimiter = rate.NewLimiter(rate.Inf, 1)
			client.httpClient = &http.Client{Transport: groqTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 404, Header: http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"error":{"code":404,"message":"missing","status":"NOT_FOUND"}}`)),
				}, nil
			})}
			model := "openai/gpt-oss-20b"
			chatCalls := 0
			chat := &http.Client{Transport: groqTransport(func(r *http.Request) (*http.Response, error) {
				chatCalls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), model) {
					t.Fatalf("wrong reserve model on attempt %d", chatCalls)
				}
				payload := `[
					{"request_id":"recognition-2","id":0,"en_title":"C","confidence":0.7,"status":"resolved","reason":"C reason"},
					{"request_id":"recognition-0","id":2,"en_title":"A","confidence":0.8,"status":"resolved","reason":"A reason"}]`
				if chatCalls == 2 {
					if !strings.Contains(string(body), "recognition-1") || strings.Contains(string(body), "recognition-0") ||
						strings.Contains(string(body), "recognition-2") {
						t.Fatal("retry did not submit only B")
					}
					payload = `[
						{"id":1,"en_title":"Wrong legacy","confidence":0.7},
						{"request_id":"recognition-1","id":0,"en_title":"B","confidence":0.9,"status":"resolved","reason":"B reason"}]`
				}
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, strings.ReplaceAll(payload, "\n", " ")))),
				}, nil
			})}
			if provider == "groq" {
				client.cfg.GroqAPIKey = "fixture"
				client.groqHTTPClient = chat
				ctx = WithGroqModelSelection(ctx, []string{model})
			} else {
				model = "grok-3-mini"
				client.cfg.GrokAPIKey, client.cfg.GrokModel = "fixture", model
				client.grokHTTPClient = chat
			}
			inputs := []FileRecognitionContext{{ID: 0}, {ID: 1}, {ID: 2}}
			results, err := client.RecognizeBulk(ctx, inputs)
			if err != nil || len(results) != len(inputs) {
				t.Fatalf("results=%v, err=%v", results, err)
			}
			metrics := client.CallMetrics()
			titles := make([]string, len(inputs))
			for i, title := range []string{"A", "B", "C"} {
				rec := results[i]
				if rec == nil || rec.ENTitle != title || rec.RequestID != fmt.Sprintf("recognition-%d", i) ||
					rec.Provider != provider || rec.Model != model || rec.Status != "resolved" || rec.Reason != title+" reason" ||
					rec.Confidence != []float64{0.8, 0.9, 0.7}[i] {
					t.Errorf("slot %d: %+v", i, rec)
				}
				if rec != nil {
					titles[i] = rec.ENTitle
				}
			}
			t.Logf("inference=%+v hypotheses=%v", metrics, titles)
			if metrics.GeminiGenerate != 1 || metrics.RecognitionRetry != 1 || chatCalls != 2 ||
				provider == "groq" && (metrics.Groq != 2 || metrics.Grok != 0) ||
				provider == "grok" && (metrics.Grok != 2 || metrics.Groq != 0) {
				t.Errorf("unexpected cascade: %+v chat=%d", metrics, chatCalls)
			}
		})
	}
}

func TestR5CorrelationDoesNotPublishAfterDecodedResponseCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := NewClient(&config.Config{})
	results, err := client.retryMissingRecognitions(ctx, []FileRecognitionContext{{RequestID: "a"}}, []RecognizedTitle{{RequestID: "a", ENTitle: "A"}})
	if err != context.Canceled || results != nil || client.CallMetrics() != (CallMetrics{}) {
		t.Fatalf("results=%v err=%v inference=%+v", results, err, client.CallMetrics())
	}
}

func TestR5LegacyRequiresUniqueInputID(t *testing.T) {
	client := NewClient(&config.Config{})
	results, err := client.retryMissingRecognitions(t.Context(), []FileRecognitionContext{
		{ID: 0, RequestID: "a"}, {ID: 0, RequestID: "b"}, {ID: 1, RequestID: "c"},
	}, []RecognizedTitle{{ID: 0, ENTitle: "Ambiguous"}, {ID: 1, ENTitle: "C"}})
	if err != nil || len(results) != 3 || results[0] != nil || results[1] != nil || results[2] == nil ||
		results[2].ENTitle != "C" || client.CallMetrics() != (CallMetrics{}) {
		t.Fatalf("results=%v err=%v inference=%+v", results, err, client.CallMetrics())
	}
}
