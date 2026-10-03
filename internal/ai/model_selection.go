package ai

import "context"

type modelSelectionKey struct{}
type modelSelection struct{ gemini, grok, groq []string }

// WithModelSelection captures an immutable cascade for the whole operation,
// including its background localization tasks, while sharing clients and limiters.
func WithModelSelection(ctx context.Context, gemini, grok []string) context.Context {
	return context.WithValue(ctx, modelSelectionKey{}, modelSelection{
		gemini: append([]string(nil), gemini...), grok: append([]string(nil), grok...),
	})
}

func WithGroqModelSelection(ctx context.Context, models []string) context.Context {
	selection, _ := ctx.Value(modelSelectionKey{}).(modelSelection)
	selection.groq = append([]string(nil), models...)
	return context.WithValue(ctx, modelSelectionKey{}, selection)
}

func (c *Client) groqModelsForContext(ctx context.Context) []string {
	if selection, ok := ctx.Value(modelSelectionKey{}).(modelSelection); ok && len(selection.groq) > 0 {
		return selection.groq
	}
	if c.cfg.GroqModel != "" {
		return []string{c.cfg.GroqModel}
	}
	return []string{"openai/gpt-oss-120b"}
}

func (c *Client) modelsForContext(ctx context.Context) []string {
	if selection, ok := ctx.Value(modelSelectionKey{}).(modelSelection); ok && len(selection.gemini) > 0 {
		return filterGeminiModels(selection.gemini)
	}
	return c.getModels()
}

func (c *Client) grokModelsForContext(ctx context.Context) []string {
	if selection, ok := ctx.Value(modelSelectionKey{}).(modelSelection); ok && len(selection.grok) > 0 {
		return selection.grok
	}
	return c.getGrokModels()
}
