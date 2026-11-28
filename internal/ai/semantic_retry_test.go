package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"movielist-app/internal/config"
)

func TestR10ASemanticRetryUsesOneReserveModel(t *testing.T) {
	for _, provider := range []string{"groq", "grok"} {
		for _, fail := range []bool{false, true} {
			mode := "/success"
			if fail {
				mode = "/http_error"
			}
			t.Run(provider+mode, func(t *testing.T) {
				client := NewClient(&config.Config{GeminiAPIKey: "fixture"})
				client.quotaLocked.Store(true)
				client.grokLimiter = rate.NewLimiter(rate.Inf, 1)
				ctx := t.Context()
				const previous, next = "previous-model", "next-model"
				calls := 0
				chat := &http.Client{Transport: groqTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					body, err := io.ReadAll(r.Body)
					if err != nil || !strings.Contains(string(body), `"model":"next-model"`) ||
						!strings.Contains(string(body), "previous_rejection") {
						t.Error("semantic retry must send the selected model and rejection hint")
					}
					status := http.StatusOK
					response := `{"choices":[{"message":{"content":"[{\"request_id\":\"r5\",\"id\":8,\"en_title\":\"Title\",\"confidence\":0.9}]"}}]}`
					if fail {
						status, response = http.StatusServiceUnavailable, "{}"
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response))}, nil
				})}
				if provider == "groq" {
					client.cfg.GroqAPIKey = "fixture"
					client.groqHTTPClient = chat
					ctx = WithGroqModelSelection(ctx, []string{previous, next, "third-model"})
				} else {
					client.cfg.GrokAPIKey = "fixture"
					client.grokHTTPClient = chat
					ctx = WithModelSelection(ctx, nil, []string{previous, next, "third-model"})
				}
				results, err := client.RetryRecognition(ctx, []FileRecognitionContext{
					{ID: 5, RequestID: "r5", PreviousModel: previous, PreviousRejection: "year_mismatch"},
					{ID: 8, RequestID: "r8", PreviousModel: previous, PreviousRejection: "low_confidence"},
				})
				if calls != 1 || (err != nil) != fail || client.CallMetrics().RecognitionRetry != 0 {
					t.Fatalf("calls=%d, err=%v, metrics=%+v", calls, err, client.CallMetrics())
				}
				if !fail && (len(results) != 2 || results[0] == nil || results[1] != nil ||
					results[0].Provider != provider || results[0].Model != next || results[0].ID != 5) {
					t.Errorf("unexpected correlated result: %+v", results)
				}
			})
		}
	}
}

func TestR10ASemanticRetryUnavailableOrCanceled(t *testing.T) {
	client := NewClient(&config.Config{})
	inputs := []FileRecognitionContext{{ID: 5, RequestID: "r5", PreviousRejection: "year_mismatch"}}
	if got, err := client.RetryRecognition(t.Context(), inputs); got != nil || err != nil {
		t.Fatalf("unavailable model: results=%v, err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := client.RetryRecognition(ctx, inputs); got != nil || err != context.Canceled {
		t.Fatalf("canceled: results=%v, err=%v", got, err)
	}
	if metrics := client.CallMetrics(); metrics != (CallMetrics{}) {
		t.Errorf("unexpected inference: %+v", metrics)
	}
}
