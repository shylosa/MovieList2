package ai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"movielist-app/internal/config"
	"movielist-app/internal/utils"
)

var groqLiveBudgetRE = regexp.MustCompile(`(?i)(limit|requested)\s*:?\s*(\d+)`)

// Explicit opt-in: real inference consumes the configured Groq account quota.
// Only synthetic filenames are sent; no library data or database is accessed.
func liveGroqKey(t *testing.T) string {
	t.Helper()
	if os.Getenv("MOVIELIST_LIVE_GROQ_INFERENCE") != "1" {
		t.Skip("opt-in Groq inference")
	}
	env, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local configuration")
	}
	key := env["GROQ_API_KEY"]
	if key == "" {
		t.Fatal("GROQ_API_KEY not configured")
	}
	return key
}

func TestLiveGroqRecognition(t *testing.T) {
	key := liveGroqKey(t)
	models := strings.Split(os.Getenv("MOVIELIST_LIVE_GROQ_MODELS"), ",")
	if len(models) == 1 && models[0] == "" {
		models = []string{"openai/gpt-oss-120b"}
	}
	cases := []struct {
		filename, title, mediaType string
		year                       int
	}{
		{"The.Matrix.1999.1080p.mkv", "The Matrix", "movie", 1999},
		{"Shrek.2001.1080p.mkv", "Shrek", "movie", 2001},
		{"Breaking.Bad.2008.S01E01.mkv", "Breaking Bad", "tv", 2008},
		{"Третий.лишний.2012.mkv", "Ted", "movie", 2012},
		{"zz_unidentified_item_948273.mkv", "", "movie", 0},
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			client := NewClient(&config.Config{GroqAPIKey: key, GroqModel: model})
			client.groqHTTPClient.Transport = groqTransport(func(req *http.Request) (*http.Response, error) {
				resp, err := http.DefaultTransport.RoundTrip(req)
				if err == nil && resp.StatusCode == http.StatusTooManyRequests {
					body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
					resp.Body.Close()
					resp.Body = io.NopCloser(bytes.NewReader(body))
					text := strings.ToLower(string(body))
					category := "rate_limited"
					if strings.Contains(text, "request too large") {
						category = "request_too_large"
					}
					if strings.Contains(text, "tokens per day") || strings.Contains(text, "requests per day") {
						category = "daily_limit"
					}
					t.Logf("HTTP 429 category=%s retry_after=%s token_limit=%s token_remaining=%s", category, resp.Header.Get("Retry-After"), resp.Header.Get("X-Ratelimit-Limit-Tokens"), resp.Header.Get("X-Ratelimit-Remaining-Tokens"))
					for _, match := range groqLiveBudgetRE.FindAllStringSubmatch(text, -1) {
						t.Logf("provider_token_budget %s=%s", match[1], match[2])
					}
				}
				return resp, err
			})
			client.quotaLocked.Store(true) // Exercise the public Gemini-quota fallback path.
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			ctx = WithGroqModelSelection(ctx, []string{model})
			inputs := make([]FileRecognitionContext, len(cases))
			for i, item := range cases {
				inputs[i] = FileRecognitionContextFromPath(item.filename)
				inputs[i].ID = i + 1
			}
			result, err := client.RecognizeBulk(ctx, inputs)
			if err != nil {
				t.Fatal(err)
			}
			if len(result) != len(cases) {
				t.Fatalf("received %d results, expected %d", len(result), len(cases))
			}
			for i, item := range cases {
				got := result[i]
				year := 0
				if got.Year != nil {
					year = *got.Year
				}
				t.Logf("file=%s title=%q type=%s year=%d status=%s confidence=%.2f provider=%s model=%s", item.filename, got.ENTitle, got.MediaType, year, got.Status, got.Confidence, got.Provider, got.Model)
				if got.Provider != "groq" || got.Model != model || got.OriginalFile != item.filename || got.RequestID != "recognition-"+strconv.Itoa(i) {
					t.Errorf("response correlation/provenance mismatch for item %d", i)
				}
				if got.ENTitle != item.title {
					t.Errorf("item %d title=%q, expected %q", i, got.ENTitle, item.title)
				}
				if item.title != "" && (got.Year == nil || *got.Year != item.year || got.MediaType != item.mediaType) {
					t.Errorf("incorrect year/type for item %d", i)
				}
				if item.title != "" && got.Confidence < 0.55 {
					t.Errorf("item %d lacks confidence required by the application", i)
				}
				if item.title == "" && (got.Status != "unresolved" || got.Confidence != 0) {
					t.Error("unknown input must remain unresolved")
				}
			}
			t.Logf("Groq requests=%d", client.CallMetrics().Groq)
		})
	}
}

func TestLiveGroqMetadataTranslation(t *testing.T) {
	key := liveGroqKey(t)
	model := "openai/gpt-oss-120b"
	client := NewClient(&config.Config{GroqAPIKey: key, GroqModel: model})
	client.quotaLocked.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	items, err := client.TranslateMetadata(WithGroqModelSelection(ctx, []string{model}), []BulkTranslateItem{{
		Filename: "The.Matrix.1999.1080p.mkv", OriginalTitle: "The Matrix", Title: "The Matrix",
		Plot: "A computer hacker discovers that the world he knows is a simulation.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Filename != "The.Matrix.1999.1080p.mkv" || items[0].Title != "Матриця" || !utils.IsGoodUkrainian(items[0].Plot) {
		t.Fatalf("unexpected localization: %+v", items)
	}
	t.Logf("title=%q plot=%q Groq requests=%d", items[0].Title, items[0].Plot, client.CallMetrics().Groq)
}
