package tmdb

import (
	"context"
	"strings"

	"movielist-app/internal/utils"
)

type searchAttemptsKey struct{}
type attemptIdentity struct {
	query      string
	queryYear  int
	targetYear int
	mediaType  MediaType
	locales    string
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
	if utils.HasCyrillic(query) {
		locales = "uk-UA,ru-RU"
	}
	key := attemptIdentity{
		query:      strings.ToLower(strings.TrimSpace(query)),
		queryYear:  queryYear,
		targetYear: targetYear,
		mediaType:  mediaType,
		locales:    locales,
	}
	if seen[key] {
		return false
	}
	seen[key] = true
	return true
}
