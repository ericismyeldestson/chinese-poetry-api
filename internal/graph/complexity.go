package graph

import (
	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/graph/generated"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/graph/model"
)

const (
	defaultGraphQLPageSize  = 20
	nestedAuthorPoemsIOCost = 20
)

// ComplexityRoot prices collection fields by the number of rows they may
// resolve. gqlgen's default cost is based only on selected field depth, which
// would make pageSize:100 cost the same as pageSize:1 and leave N+1 resolver
// work effectively unbounded by the configured complexity limit.
func ComplexityRoot() generated.ComplexityRoot {
	var root generated.ComplexityRoot

	pageCost := func(childComplexity int, pageSize *int) int {
		size := defaultGraphQLPageSize
		if pageSize != nil {
			size = *pageSize
		}
		if size < 1 {
			size = 1
		}
		if size > maxPageSize {
			size = maxPageSize
		}
		return 1 + size*childComplexity
	}

	root.Query.Poems = func(childComplexity int, _ *database.Lang, _ *int, pageSize *int, _ *string, _ *string, _ *string) int {
		return pageCost(childComplexity, pageSize)
	}
	root.Query.SearchPoems = func(childComplexity int, _ string, _ *database.Lang, _ *model.SearchType, _ *int, pageSize *int) int {
		return pageCost(childComplexity, pageSize)
	}
	root.Query.Authors = func(childComplexity int, _ *database.Lang, _ *int, pageSize *int, _ *string) int {
		return pageCost(childComplexity, pageSize)
	}
	root.Author.Poems = func(childComplexity int, _ *int, pageSize *int) int {
		// This nested resolver performs an independent count/page query pair. A
		// large fixed I/O charge prevents it from being fanned out beneath an
		// authors collection within the default complexity budget; direct clients
		// can use root poems(authorId:) without this fan-out risk.
		return nestedAuthorPoemsIOCost + pageCost(childComplexity, pageSize)
	}

	return root
}
