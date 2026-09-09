package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/time/rate"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRedactAPIKeyErrorHidesSecretAndPreservesCause(t *testing.T) {
	const secret = "super-secret-key"
	original := fmt.Errorf("GET https://example.test/search?api_key=%s&query=movie: %w", secret, context.Canceled)
	redacted := redactAPIKeyError(original)

	if strings.Contains(redacted.Error(), secret) {
		t.Fatalf("redacted error leaked API key: %q", redacted)
	}
	if !strings.Contains(redacted.Error(), "api_key=***MASKED***") {
		t.Fatalf("redacted error does not contain mask: %q", redacted)
	}
	if !errors.Is(redacted, context.Canceled) {
		t.Fatal("redacted error no longer preserves context.Canceled")
	}
}

func TestSearchWithFallbacksStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	c := &Client{
		apiKey:      "test-secret",
		rateLimiter: rate.NewLimiter(rate.Inf, 1),
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			cancel()
			return nil, context.Canceled
		})},
	}

	_, err := c.SearchWithFallbacks(ctx, ParsedFile{
		CleanTitle: "Example Movie",
		Year:       2024,
		MediaType:  MediaTypeMovie,
	}, "Example.Movie.2024.mkv")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchWithFallbacks() error = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("network requests after cancellation = %d, want 1 total request", got)
	}
}

func TestDoRequestCancellationErrorDoesNotLeakAPIKey(t *testing.T) {
	const secret = "request-secret"
	c := &Client{
		rateLimiter: rate.NewLimiter(rate.Inf, 1),
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.Canceled
		})},
	}

	err := c.doRequest(context.Background(), "https://example.test/search?api_key="+secret+"&query=movie", &struct{}{})
	if err == nil {
		t.Fatal("doRequest() error = nil, want cancellation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("doRequest() error leaked API key: %q", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("doRequest() error no longer preserves context.Canceled")
	}
}
