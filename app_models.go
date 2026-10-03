package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"movielist-app/internal/ai"
)

type ProviderModels struct {
	Current    []string `json:"current"`
	Configured bool     `json:"configured"`
}

// GetModelSelections reads local state only; opening settings never calls a provider.
func (a *App) GetModelSelections() map[string]ProviderModels {
	return map[string]ProviderModels{
		"gemini": {a.configuredGeminiModels(), a.cfg.GeminiAPIKey != ""},
		"grok":   {a.configuredGrokModels(), a.cfg.GrokAPIKey != ""},
		"groq":   {a.configuredGroqModels(), a.cfg.GroqAPIKey != ""},
	}
}

func (a *App) configuredGrokModels() []string {
	if a.db != nil {
		var selected []string
		if raw := a.db.GetState(a.ctx, "selected_grok_models"); raw != "" && json.Unmarshal([]byte(raw), &selected) == nil && len(selected) > 0 {
			return selected
		}
	}
	if a.cfg.GrokModel != "" {
		return []string{a.cfg.GrokModel}
	}
	return []string{"grok-3-mini"}
}

// Capture selections once, without mutating the shared AI client.
func (a *App) modelContext(ctx context.Context) context.Context {
	if a.cfg == nil {
		return ctx
	}
	return ai.WithGroqModelSelection(ai.WithModelSelection(ctx, a.configuredGeminiModels(), a.configuredGrokModels()), a.configuredGroqModels())
}

func (a *App) SetProviderModels(provider string, names []string) error {
	if provider != "gemini" && provider != "grok" && provider != "groq" {
		return fmt.Errorf("unknown AI provider")
	}
	if len(names) == 0 {
		return fmt.Errorf("оберіть хоча б одну модель")
	}
	current := a.GetModelSelections()[provider].Current
	a.modelsMutex.RLock()
	available := append([]string(nil), a.discoveredAIModels...)
	if provider == "grok" {
		available = append([]string(nil), a.discoveredGrokModels...)
	}
	if provider == "groq" {
		available = append([]string(nil), a.discoveredGroqModels...)
	}
	a.modelsMutex.RUnlock()
	allowed := make(map[string]bool)
	for _, name := range append(current, available...) {
		allowed[name] = true
	}
	selected := make([]string, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if !allowed[name] {
			return fmt.Errorf("модель відсутня в отриманому каталозі")
		}
		if !seen[name] {
			selected = append(selected, name)
			seen[name] = true
		}
	}
	if a.db == nil {
		return fmt.Errorf("database unavailable")
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return err
	}
	if err := a.db.SetState(a.ctx, "selected_"+provider+"_models", string(encoded)); err != nil {
		return err
	}
	a.modelsMutex.Lock()
	a.aiModelsCache = nil
	a.modelsMutex.Unlock()
	return nil
}

func (a *App) GetGrokModelCatalog() (AIModelCatalog, error) {
	current := a.configuredGrokModels()
	if a.cfg.GrokAPIKey == "" {
		return AIModelCatalog{Current: current}, fmt.Errorf("GROK_API_KEY не задано")
	}
	value, err, _ := a.modelsGroup.Do("grok_catalog", func() (interface{}, error) {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.x.ai/v1/language-models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+a.cfg.GrokAPIKey)
		client := a.aiModelsHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("не вдалося отримати каталог Grok")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, grokCatalogHTTPError(resp)
		}
		var catalog struct {
			Models []struct {
				ID           string   `json:"id"`
				Aliases      []string `json:"aliases"`
				Input        []string `json:"input_modalities"`
				Output       []string `json:"output_modalities"`
				Capabilities struct {
					ReasoningEffort []string `json:"reasoning_effort"`
				} `json:"capabilities"`
			} `json:"models"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&catalog); err != nil {
			return nil, fmt.Errorf("некоректний каталог Grok")
		}
		names := make(map[string]bool)
		hasText := func(modalities []string) bool {
			for _, m := range modalities {
				if m == "text" {
					return true
				}
			}
			return false
		}
		for _, model := range catalog.Models {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !hasText(model.Input) || !hasText(model.Output) {
				continue
			}
			// Preserve the application's reasoning_effort=none contract. Exclude
			// models explicitly advertising only incompatible effort levels.
			if efforts := model.Capabilities.ReasoningEffort; len(efforts) > 0 {
				compatible := false
				for _, effort := range efforts {
					if effort == "none" {
						compatible = true
					}
				}
				if !compatible {
					continue
				}
			}
			for _, name := range append([]string{model.ID}, model.Aliases...) {
				if strings.HasPrefix(name, "grok-") {
					names[name] = true
				}
			}
		}
		available := make([]string, 0, len(names))
		for name := range names {
			available = append(available, name)
		}
		sort.Strings(available)
		a.modelsMutex.Lock()
		a.discoveredGrokModels = append([]string(nil), available...)
		a.modelsMutex.Unlock()
		return available, nil
	})
	if err != nil {
		return AIModelCatalog{Current: current}, err
	}
	return AIModelCatalog{Current: current, Available: value.([]string)}, nil
}

// Classify the provider response without returning or logging remote content:
// xAI may include the rejected credential in its error message.
func grokCatalogHTTPError(resp *http.Response) error {
	reason := "request_failed"
	message := fmt.Sprintf("каталог Grok: HTTP %d", resp.StatusCode)
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		lower := strings.ToLower(string(body))
		if resp.StatusCode == http.StatusUnauthorized || strings.Contains(lower, "incorrect api key") ||
			strings.Contains(lower, "invalid api key") || strings.Contains(lower, "invalid token") ||
			strings.Contains(lower, "incorrect key") {
			reason = "invalid_api_key"
			message = "xAI не прийняв ключ Grok. Замініть GROK_API_KEY у конфігурації та перезапустіть програму"
		}
	case http.StatusForbidden:
		reason = "access_denied"
		message = "xAI відмовив у доступі до каталогу Grok. Перевірте дозволи API-ключа"
	case http.StatusTooManyRequests:
		reason = "rate_limited"
		message = "xAI обмежив частоту запитів. Спробуйте отримати каталог Grok пізніше"
	}
	slog.Warn("grok_catalog_failed", slog.Int("http_status", resp.StatusCode), slog.String("reason", reason))
	return fmt.Errorf("%s", message)
}
