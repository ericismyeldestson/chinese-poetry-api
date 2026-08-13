package graph

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/99designs/gqlgen/graphql"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// A GraphQL child resolver receives the operation context, not a context
// returned by its parent resolver. This request-scoped registry carries the
// language selected at each root response path across that boundary without
// putting transport state on persistent database models or relying on unstable
// IDs. Paths (including aliases) are used because gqlgen may copy value-backed
// edges while marshaling, so object-pointer identity is not stable.
type languageStateKey struct{}

type requestLanguageState struct {
	mu           sync.RWMutex
	paths        map[string]database.Lang
	authorCounts map[string]int
	dynastyStats map[string]entityCounts
	typeCounts   map[string]int
}

type entityCounts struct {
	poems   int
	authors int
}

// WithLanguageState installs an empty per-operation language registry. The
// GraphQL server must call this from operation middleware before resolvers run.
func WithLanguageState(ctx context.Context) context.Context {
	if _, ok := ctx.Value(languageStateKey{}).(*requestLanguageState); ok {
		return ctx
	}
	return context.WithValue(ctx, languageStateKey{}, &requestLanguageState{
		paths:        make(map[string]database.Lang),
		authorCounts: make(map[string]int),
		dynastyStats: make(map[string]entityCounts),
		typeCounts:   make(map[string]int),
	})
}

func entityKey(lang database.Lang, id int64) string {
	return string(lang) + ":" + strconv.FormatInt(id, 10)
}

func cacheAuthorCount(ctx context.Context, lang database.Lang, id int64, count int) {
	if state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState); ok {
		state.mu.Lock()
		state.authorCounts[entityKey(lang, id)] = count
		state.mu.Unlock()
	}
}

func authorCount(ctx context.Context, lang database.Lang, id int64) (int, bool) {
	state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState)
	if !ok {
		return 0, false
	}
	state.mu.RLock()
	count, ok := state.authorCounts[entityKey(lang, id)]
	state.mu.RUnlock()
	return count, ok
}

func cacheDynastyStats(ctx context.Context, lang database.Lang, id int64, poems, authors int) {
	if state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState); ok {
		state.mu.Lock()
		state.dynastyStats[entityKey(lang, id)] = entityCounts{poems: poems, authors: authors}
		state.mu.Unlock()
	}
}

func dynastyStats(ctx context.Context, lang database.Lang, id int64) (entityCounts, bool) {
	state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState)
	if !ok {
		return entityCounts{}, false
	}
	state.mu.RLock()
	counts, ok := state.dynastyStats[entityKey(lang, id)]
	state.mu.RUnlock()
	return counts, ok
}

func cacheTypeCount(ctx context.Context, lang database.Lang, id int64, count int) {
	if state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState); ok {
		state.mu.Lock()
		state.typeCounts[entityKey(lang, id)] = count
		state.mu.Unlock()
	}
}

func typeCount(ctx context.Context, lang database.Lang, id int64) (int, bool) {
	state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState)
	if !ok {
		return 0, false
	}
	state.mu.RLock()
	count, ok := state.typeCounts[entityKey(lang, id)]
	state.mu.RUnlock()
	return count, ok
}

func registerAuthorLanguage(ctx context.Context, author *database.Author, lang database.Lang) {
	if author == nil {
		return
	}
	registerEntityLanguage(ctx, lang)
}

func registerEntityLanguage(ctx context.Context, lang database.Lang) {
	state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState)
	if !ok {
		return
	}
	path := graphql.GetPath(ctx).String()
	if path == "" {
		return
	}
	state.mu.Lock()
	state.paths[path] = lang
	state.mu.Unlock()
}

func registerPoemLanguage(ctx context.Context, poem *database.Poem, lang database.Lang) {
	if poem != nil {
		registerEntityLanguage(ctx, lang)
		registerAuthorLanguage(ctx, poem.Author, lang)
	}
}

func registerPoemConnectionLanguage(ctx context.Context, connection *database.PoemConnection, lang database.Lang) {
	if connection == nil {
		return
	}
	registerEntityLanguage(ctx, lang)
	for i := range connection.Edges {
		registerPoemLanguage(ctx, &connection.Edges[i].Node, lang)
	}
}

func registerAuthorConnectionLanguage(ctx context.Context, connection *database.AuthorConnection, lang database.Lang) {
	if connection == nil {
		return
	}
	registerEntityLanguage(ctx, lang)
	for i := range connection.Edges {
		registerAuthorLanguage(ctx, &connection.Edges[i].Node.Author, lang)
	}
}

func authorLanguage(ctx context.Context, _ *database.Author) (database.Lang, error) {
	return languageForPath(ctx)
}

func languageForPath(ctx context.Context) (database.Lang, error) {
	state, ok := ctx.Value(languageStateKey{}).(*requestLanguageState)
	if !ok {
		return "", fmt.Errorf("GraphQL language context is unavailable")
	}
	currentPath := graphql.GetPath(ctx).String()
	state.mu.RLock()
	var (
		lang       database.Lang
		bestLength int
	)
	for parentPath, candidate := range state.paths {
		if currentPath != parentPath &&
			!strings.HasPrefix(currentPath, parentPath+".") &&
			!strings.HasPrefix(currentPath, parentPath+"[") {
			continue
		}
		if len(parentPath) > bestLength {
			lang = candidate
			bestLength = len(parentPath)
		}
	}
	state.mu.RUnlock()
	if bestLength == 0 {
		return "", fmt.Errorf("GraphQL language context is unavailable")
	}
	return lang, nil
}
