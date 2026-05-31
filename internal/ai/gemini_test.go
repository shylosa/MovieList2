package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"movielist-app/internal/config"

	"golang.org/x/time/rate"
)

// 🔴 ХІРУРГІЧНЕ ВТРУЧАННЯ: Транспорт для перехоплення запитів SDK і перенаправлення на локальний мок
type mockTransport struct {
	serverURL string
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
