package graph

import (
	"fmt"
	"strconv"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/helpers"
)

// Pagination 保存解析后的分页参数。
type Pagination struct {
	Page     int
	PageSize int
	Offset   int
}

// 分页的默认值与上限，与 REST handler 保持一致。
const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPage         = 1000
	maxPageSize     = 100
)

// parsePagination 解析并校验分页参数，缺省时取默认值：page=1、pageSize=20，pageSize 上限 100。
//
// 越界取值一律报错而非截断，这样客户端传 pageSize: 1000 时能明确知道请求未被采纳。
// 此外，截断的做法在 resolver 直接读取参数而不调用本函数的地方等于没有上限，
// searchPoems 曾因此可以请求任意多的记录。
func parsePagination(page, pageSize *int) (Pagination, error) {
	p := defaultPage
	if page != nil {
		if *page < 1 {
			return Pagination{}, fmt.Errorf("page must be at least 1, got %d", *page)
		}
		if *page > maxPage {
			return Pagination{}, fmt.Errorf("page must be at most %d, got %d", maxPage, *page)
		}
		p = *page
	}

	ps := defaultPageSize
	if pageSize != nil {
		if *pageSize < 1 || *pageSize > maxPageSize {
			return Pagination{}, fmt.Errorf("pageSize must be between 1 and %d, got %d", maxPageSize, *pageSize)
		}
		ps = *pageSize
	}

	return Pagination{
		Page:     p,
		PageSize: ps,
		Offset:   (p - 1) * ps,
	}, nil
}

// parseOptionalID 把可选的字符串 ID 解析为 *int64。
func parseOptionalID(id *string) (*int64, error) {
	return helpers.ParseOptionalInt64(id)
}

// parseLang 把可选的 Lang 指针转换为 Lang 取值，为 nil 时返回默认语言。
func parseLang(lang *database.Lang) database.Lang {
	return helpers.ParseLangPointer(lang)
}

// buildPoemConnection 根据诗词切片与分页信息构造 PoemConnection。
func buildPoemConnection(poems []database.Poem, pag Pagination, totalCount int) *database.PoemConnection {
	edges := make([]database.PoemEdge, len(poems))
	for i, poem := range poems {
		edges[i] = database.PoemEdge{
			Node:   poem,
			Cursor: strconv.Itoa(pag.Offset + i),
		}
	}

	hasNextPage := pag.Offset+len(poems) < totalCount
	hasPreviousPage := pag.Page > 1

	var startCursor, endCursor *string
	if len(edges) > 0 {
		start := edges[0].Cursor
		end := edges[len(edges)-1].Cursor
		startCursor = &start
		endCursor = &end
	}

	return &database.PoemConnection{
		Edges: edges,
		PageInfo: database.PageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: hasPreviousPage,
			StartCursor:     startCursor,
			EndCursor:       endCursor,
		},
		TotalCount: totalCount,
	}
}

// buildAuthorConnection 根据作者切片与分页信息构造 AuthorConnection。
func buildAuthorConnection(authors []database.AuthorWithStats, pag Pagination, totalCount int) *database.AuthorConnection {
	edges := make([]database.AuthorEdge, len(authors))
	for i, author := range authors {
		edges[i] = database.AuthorEdge{
			Node:   author,
			Cursor: strconv.Itoa(pag.Offset + i),
		}
	}

	hasNextPage := pag.Offset+len(authors) < totalCount
	hasPreviousPage := pag.Page > 1

	var startCursor, endCursor *string
	if len(edges) > 0 {
		start := edges[0].Cursor
		end := edges[len(edges)-1].Cursor
		startCursor = &start
		endCursor = &end
	}

	return &database.AuthorConnection{
		Edges: edges,
		PageInfo: database.PageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: hasPreviousPage,
			StartCursor:     startCursor,
			EndCursor:       endCursor,
		},
		TotalCount: totalCount,
	}
}
