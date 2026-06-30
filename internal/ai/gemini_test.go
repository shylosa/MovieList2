package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"movielist-app/internal/config"

	"golang.org/x/time/rate"
)

// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Транспорт для перехоплення запитів SDK і перенаправлення на локальний мок
type mockTransport struct {
	serverURL string
}

func TestGetModelsFiltersTTS(t *testing.T) {
	client := NewClient(&config.Config{})
	client.SetModels([]string{
		"gemini-2.5-flash-preview-tts",
		"gemini-2.5-pro-preview-tts",
		"gemini-2.5-flash",
		"gemini-embedding-exp",
	})

	models := client.getModels()
	if len(models) != 1 || models[0] != "gemini-2.5-flash" {
		t.Fatalf("expected only gemini-2.5-flash, got %#v", models)
	}
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// safe to ignore: tests pass a valid httptest server URL.
	clonedURL, _ := url.Parse(m.serverURL)
	reqClone := req.Clone(req.Context())
	reqClone.URL.Scheme = clonedURL.Scheme
	reqClone.URL.Host = clonedURL.Host
	return http.DefaultTransport.RoundTrip(reqClone)
}

// Цей тест перевіряє РЕГРЕСІЮ: чи перемикається каскад на іншу модель
// при отриманні помилки від першої.
func TestGeminiCascadeFallback(t *testing.T) {
	// 1. Створюємо мок-сервер, який імітує API Google
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		if reqCount == 1 {
			// Перша модель падає (наприклад, перевищено ліміти 429)
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": {"message": "Quota exceeded"}}`))
			return
		}

		// Друга модель відповідає успішно
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Валідна відповідь GenAI SDK
		w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{"text": "[{\"original_file\":\"test.mkv\",\"en_title\":\"Success Movie\",\"media_type\":\"movie\"}]"}]
				}
			}]
		}`))
	}))
	defer server.Close()

	// 2. Налаштовуємо клієнта
	cfg := &config.Config{
		GeminiAPIKey: "fake-key",
	}
	client := &Client{
		cfg:          cfg,
		limiter:      rate.NewLimiter(rate.Every(1*time.Millisecond), 1),
		activeModels: []string{"gemini-fail-flash-model", "gemini-success-flash-model"},
		// 🔴 ВИПРАВЛЕННЯ: Використовуємо наш кастомний транспорт
		httpClient: &http.Client{Transport: &mockTransport{serverURL: server.URL}},
	}
	client.quotaLocked.Store(false)

	// 3. Запускаємо запит
	ctx := context.Background()
	results, err := client.RecognizeBulk(ctx, []FileRecognitionContext{
		{OriginalFile: "test.mkv", CleanTitle: "test", MediaType: "movie"},
	})

	// 4. Перевірки
	if err != nil {
		t.Fatalf("Регресія: Каскад впав з помилкою замість перемикання: %v", err)
	}

	if len(results) == 0 || results[0].ENTitle != "Success Movie" {
		t.Errorf("Отримано неправильний результат від резервної моделі")
	}

	if reqCount != 2 {
		t.Errorf("Очікувалось 2 HTTP запити (1 фейл, 1 успіх), але було %d", reqCount)
	}
}

func TestTranslateBulk_GeminiQuotaLockedFallsBackToGrok(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "[{\"filename\":\"test.mkv\",\"title\":\"Офіційна назва\",\"plot\":\"Опис\"}]"
				}
			}]
		}`))
	}))
	defer server.Close()

	client := &Client{
		cfg:            &config.Config{GrokAPIKey: "test-key"},
		grokHTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: &grokTestTransport{serverURL: server.URL}},
	}
	client.quotaLocked.Store(true)

	results, err := client.TranslateBulk(context.Background(), []BulkTranslateItem{{Filename: "test.mkv", Title: "", OriginalTitle: "Original", Plot: "Plot"}})
	if err != nil {
		t.Fatalf("expected TranslateBulk to fall back to Grok, got: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Filename != "test.mkv" || results[0].Title != "Офіційна назва" {
		t.Errorf("unexpected translated result: %#v", results[0])
	}
}

func TestTranslateBulk_Gemini429FallsBackToGrok(t *testing.T) {
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		if reqCount <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"Quota exceeded"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "[{\"filename\":\"test.mkv\",\"title\":\"Офіційна назва\",\"plot\":\"Опис\"}]"
				}
			}]
		}`))
	}))
	defer server.Close()

	client := &Client{
		cfg: &config.Config{
			GeminiAPIKey: "fake-key",
			GrokAPIKey:   "test-key",
		},
		limiter:      rate.NewLimiter(rate.Every(1*time.Millisecond), 1),
		activeModels: []string{"gemini-2.5-flash", "gemini-2.5-pro"},
		httpClient:   &http.Client{Transport: &mockTransport{serverURL: server.URL}},
		grokHTTPClient: &http.Client{Timeout: 5 * time.Second,
			Transport: &grokTestTransport{serverURL: server.URL}},
	}
	client.quotaLocked.Store(false)

	results, err := client.TranslateBulk(context.Background(), []BulkTranslateItem{{Filename: "test.mkv", Title: "", OriginalTitle: "Original", Plot: "Plot"}})
	if err != nil {
		t.Fatalf("expected TranslateBulk to fall back to Grok after 429, got: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Filename != "test.mkv" || results[0].Title != "Офіційна назва" {
		t.Errorf("unexpected translated result: %#v", results[0])
	}
	if reqCount < 3 {
		t.Errorf("expected at least 3 HTTP requests (2 Gemini + 1 Grok), got %d", reqCount)
	}
}

func TestRecognizeBulk_GeminiQuotaLockedFallsBackToGrok(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "[{\"id\":1,\"original_file\":\"Enemy.2013.mkv\",\"en_title\":\"Enemy\",\"media_type\":\"movie\",\"confidence\":0.95}]"
				}
			}]
		}`))
	}))
	defer server.Close()

	client := &Client{
		cfg:            &config.Config{GrokAPIKey: "test-key", GrokModel: "grok-3-mini"},
		grokHTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: &grokTestTransport{serverURL: server.URL}},
	}
	client.quotaLocked.Store(true)

	contexts := []FileRecognitionContext{{ID: 1, OriginalFile: "Enemy.2013.mkv", CleanTitle: "Enemy", MediaType: "movie"}}
	results, err := client.RecognizeBulk(context.Background(), contexts)
	if err != nil {
		t.Fatalf("expected RecognizeBulk to fall back to Grok when quota locked, got: %v", err)
	}
	if len(results) != 1 || results[0].ENTitle != "Enemy" {
		t.Errorf("unexpected result: %+v", results)
	}
}

func TestRecognizeBulk_QuotaLockedNoGrok(t *testing.T) {
	client := &Client{
		cfg: &config.Config{GrokAPIKey: ""},
	}
	client.quotaLocked.Store(true)

	_, err := client.RecognizeBulk(context.Background(), []FileRecognitionContext{
		{ID: 1, OriginalFile: "test.mkv", CleanTitle: "test", MediaType: "movie"},
	})
	if err == nil {
		t.Fatal("expected error when quota locked and no Grok key, got nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected 'not configured' in error message, got: %v", err)
	}
}

func TestResetQuotaLock(t *testing.T) {
	c := &Client{cfg: &config.Config{}}
	c.quotaLocked.Store(true)
	c.ResetQuotaLock()

	if c.quotaLocked.Load() {
		t.Error("expected quota lock to be reset to false after ResetQuotaLock()")
	}
}
