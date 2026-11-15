package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"movielist-app/internal/utils"
	"movielist-app/internal/version"
)

const GroqModelsURL = "https://api.groq.com/openai/v1/models"
const groqChatURL = "https://api.groq.com/openai/v1/chat/completions"

type groqChatRequest struct {
	grokRequest
	MaxCompletionTokens int `json:"max_completion_tokens"`
}

// The models endpoint also returns audio, moderation and tool-using systems.
// Allow chat model families; never send a recognition prompt to those other APIs.
func IsGroqTextModel(name string) bool {
	lower := strings.ToLower(name)
	for _, excluded := range []string{"whisper", "guard", "safeguard", "orpheus", "compound", "preview", "tts"} {
		if strings.Contains(lower, excluded) {
			return false
		}
	}
	for _, prefix := range []string{"openai/gpt-oss-", "llama-", "meta-llama/llama-", "qwen/", "qwen-", "gemma", "mistral", "moonshotai/", "minimaxai/"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func (c *Client) callGroq(ctx context.Context, prompt, model string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.cfg.GroqAPIKey == "" {
		return "", fmt.Errorf("groq: API key not configured")
	}
	// Reserve traffic retains the conservative shared 30 RPM, burst=1 limiter.
	if c.grokLimiter != nil {
		if err := c.grokLimiter.Wait(ctx); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Bound generated text for catalog batches instead of using the model's
	// full default output allowance.
	payload := groqChatRequest{grokRequest: grokRequest{Model: model, Messages: []grokMessage{{Role: "user", Content: prompt}}, Temperature: 0.1}, MaxCompletionTokens: 4096}
	// Groq's GPT-OSS models reject reasoning_effort=none. Use their lowest
	// supported effort; reasoning is separate from message.content.
	if strings.HasPrefix(model, "openai/gpt-oss-") {
		payload.ReasoningEffort = "low"
	}
	if strings.HasPrefix(model, "qwen/") || strings.HasPrefix(model, "qwen-") {
		payload.ReasoningEffort = "none"
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqChatURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.GroqAPIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MovieList/"+version.Current)
	client := c.groqHTTPClient
	if client == nil {
		return "", fmt.Errorf("groq: client unavailable")
	}
	c.groqCalls.Add(1)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("groq: network request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq: HTTP %d", resp.StatusCode)
	}
	var result grokResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("groq: invalid response")
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("groq: empty response")
	}
	return result.Choices[0].Message.Content, nil
}

func (c *Client) groqRecognizeFallback(ctx context.Context, prompt string) ([]RecognizedTitle, error) {
	// Gemini receives its field contract through ResponseSchema. Groq uses the
	// same task prompt but needs that contract explicitly in its chat request.
	prompt = groqRecognitionPrompt(prompt)
	var lastErr error
	for _, model := range c.groqModelsForContext(ctx) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := c.callGroq(ctx, prompt, model)
		var result []RecognizedTitle
		if err == nil {
			result, err = parseGroqRecognizeResponse(raw)
		}
		if err != nil {
			lastErr = err
			continue
		}
		for i := range result {
			result[i].Provider = "groq"
			result[i].Model = model
		}
		utils.LoggerWithTrace(ctx).Info("groq_recognize_success", slog.String("model", model), slog.Int("results_count", len(result)))
		return result, nil
	}
	return nil, fmt.Errorf("groq recognize failed: %w", lastErr)
}

func groqRecognitionPrompt(prompt string) string {
	return prompt + `
OUTPUT CONTRACT: Return a raw JSON array, one object per input, with ALL fields:
id (integer copied from input), request_id (copied unchanged), original_file (copied unchanged),
en_title (string), original_title (string), year (integer or null), possible_years (integer array),
media_type (movie or tv), country (string), director_or_creator (string),
status (resolved, ambiguous, or unresolved), reason (short string), confidence (number 0.0 through 1.0).
confidence is REQUIRED: report your actual certainty in identifying the work, not certainty in reading the filename.
For unknown works use en_title="", status="unresolved", confidence=0. Do not invent a work or fill in facts when uncertain.
Use empty strings/arrays for unknown optional details. Never return a TMDB ID. No markdown or explanation.`
}

func parseGroqRecognizeResponse(raw string) ([]RecognizedTitle, error) {
	result, err := parseRecognizeResponse(raw)
	if err != nil {
		return nil, err
	}
	var fields []struct {
		Confidence *float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(cleanJSON(raw)), &fields); err != nil {
		return nil, fmt.Errorf("groq: invalid recognition fields")
	}
	for _, item := range fields {
		if item.Confidence == nil || *item.Confidence < 0 || *item.Confidence > 1 {
			return nil, fmt.Errorf("groq: missing or invalid recognition confidence")
		}
	}
	return result, nil
}

func (c *Client) groqTranslateFallback(ctx context.Context, prompt string) ([]BulkTranslateItem, error) {
	var lastErr error
	for _, model := range c.groqModelsForContext(ctx) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := c.callGroq(ctx, prompt, model)
		var result []BulkTranslateItem
		if err == nil {
			err = json.Unmarshal([]byte(cleanJSON(raw)), &result)
		}
		if err != nil {
			lastErr = err
			continue
		}
		utils.LoggerWithTrace(ctx).Info("groq_translate_success", slog.String("model", model), slog.Int("results_count", len(result)))
		return result, nil
	}
	return nil, fmt.Errorf("groq translate failed: %w", lastErr)
}
