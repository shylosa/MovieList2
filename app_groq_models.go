package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"movielist-app/internal/ai"
	"movielist-app/internal/version"
)

func (a *App) configuredGroqModels() []string {
	if a.db != nil {
		var selected []string
		if raw := a.db.GetState(a.ctx, "selected_groq_models"); raw != "" && json.Unmarshal([]byte(raw), &selected) == nil && len(selected) > 0 {
			return selected
		}
	}
	if a.cfg.GroqModel != "" {
		return []string{a.cfg.GroqModel}
	}
	return []string{"openai/gpt-oss-120b"}
}

func (a *App) GetGroqModelCatalog() (AIModelCatalog, error) {
	current := a.configuredGroqModels()
	if a.cfg.GroqAPIKey == "" {
		return AIModelCatalog{Current: current}, fmt.Errorf("GROQ_API_KEY не задано")
	}
	value, err, _ := a.modelsGroup.Do("groq_catalog", func() (interface{}, error) {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ai.GroqModelsURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+a.cfg.GroqAPIKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "MovieList/"+version.Current)
		client := a.aiModelsHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("не вдалося отримати каталог Groq")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return nil, fmt.Errorf("Groq не прийняв ключ. Перевірте GROQ_API_KEY і перезапустіть програму")
			case http.StatusForbidden:
				return nil, fmt.Errorf("Groq відмовив у доступі до каталогу (HTTP 403). Перевірте дозволи ключа та доступ до API")
			default:
				return nil, fmt.Errorf("каталог Groq: HTTP %d", resp.StatusCode)
			}
		}
		var catalog struct {
			Data []struct {
				ID     string `json:"id"`
				Active *bool  `json:"active"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&catalog); err != nil {
			return nil, fmt.Errorf("некоректний каталог Groq")
		}
		names := make(map[string]bool)
		for _, model := range catalog.Data {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if (model.Active == nil || *model.Active) && ai.IsGroqTextModel(model.ID) {
				names[model.ID] = true
			}
		}
		available := make([]string, 0, len(names))
		for name := range names {
			available = append(available, name)
		}
		sort.Strings(available)
		a.modelsMutex.Lock()
		a.discoveredGroqModels = append([]string(nil), available...)
		a.modelsMutex.Unlock()
		return available, nil
	})
	if err != nil {
		return AIModelCatalog{Current: current}, err
	}
	return AIModelCatalog{Current: current, Available: value.([]string)}, nil
}
