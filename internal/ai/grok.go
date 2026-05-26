package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const grokModel = "grok-3-mini"
const grokAPIURL = "https://api.x.ai/v1/chat/completions"

type grokRequest struct {
	Model    string        `json:"model"`
	Messages []grokMessage `json:"messages"`
}

type grokMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type grokResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// callGrok sends a prompt to the Grok API and returns raw content string.
// The prompt must already instruct the model to respond in JSON.
func (c *Client) callGrok(ctx context.Context, prompt string) (string, error) {
	if c.cfg.GrokAPIKey == "" {
		return "", fmt.Errorf("grok: API key not configured")
	}

	payload, err := json.Marshal(grokRequest{
		Model:    grokModel,
		Messages: []grokMessage{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, grokAPIURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.GrokAPIKey)

	resp, err := c.grokHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		body, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Error.Message != "" {
			return "", fmt.Errorf("grok: status %d — %s (code: %s)",
				resp.StatusCode, apiErr.Error.Message, apiErr.Error.Code)
		}
		return "", fmt.Errorf("grok: unexpected status %d", resp.StatusCode)
	}

	var result grokResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("grok: empty response")
	}

	return result.Choices[0].Message.Content, nil
}
