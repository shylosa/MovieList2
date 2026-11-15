package tmdb

import (
	"context"
	"strings"
)

type searchAttemptsKey struct{}
type attemptIdentity struct {
	SearchCacheKey
	locales string
}

// The recognition pipeline is sequential. This map belongs to one pipeline,
// never to the client or to concurrent files.
func withSearchAttempts(ctx context.Context) context.Context {
	if ctx.Value(searchAttemptsKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, searchAttemptsKey{}, make(map[attemptIdentity]bool))
}

func claimSearchAttempt(ctx context.Context, query string, queryYear, targetYear int, mediaType MediaType) bool {
	seen := ctx.Value(searchAttemptsKey{}).(map[attemptIdentity]bool)
	locales := "en-US"
	if hasCyrillicChars(query) {
		locales = "uk-UA,ru-RU"
	}
	key := attemptIdentity{SearchCacheKey{strings.ToLower(strings.TrimSpace(query)), queryYear, targetYear, mediaType}, locales}
	if seen[key] {
		return false
	}
	seen[key] = true
	return true
}
