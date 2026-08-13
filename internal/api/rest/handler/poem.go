package handler

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/api/searchquery"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// PoemHandler 处理诗词相关的请求。
type PoemHandler struct {
	repo *database.Repository
}

// NewPoemHandler 创建诗词 handler。
func NewPoemHandler(repo *database.Repository) *PoemHandler {
	return &PoemHandler{
		repo: repo,
	}
}

// ListPoems 分页返回诗词列表。
// 语言：?lang=zh-Hans（默认）或 ?lang=zh-Hant
// 过滤条件与 RandomPoem 一致，可按名称：?author=李白&type=五言绝句&dynasty=唐
// 也可按 ID：?author_id=123&type_id=456&type_id=789&dynasty_id=6
func (h *PoemHandler) ListPoems(c *gin.Context) {
	if !checkQueryParams(c, append([]string{queryLang, queryPage, queryPageSize}, filterQueryKeys...)...) {
		return
	}

	lang, ok := parseLang(c)
	if !ok {
		return
	}
	repo := h.repo.WithLang(lang)

	pagination, ok := ParsePagination(c)
	if !ok {
		return
	}

	filters, ok := parsePoemFilters(c, repo)
	if !ok {
		return
	}

	// 与 GraphQL 的 poems resolver 共用 ListPoemsWithFilter，
	// 保证相同过滤条件下两套 API 返回的内容与顺序完全一致。
	poems, total, err := repo.ListPoemsWithFilter(
		pagination.PageSize, pagination.Offset(),
		filters.dynastyID, filters.authorID, filters.typeIDs,
	)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to retrieve poems")
		return
	}

	data := make([]map[string]any, len(poems))
	for i, poem := range poems {
		data[i] = formatPoem(&poem)
	}

	c.JSON(http.StatusOK, NewPaginationResponse(data, pagination, int64(total)))
}

// searchTypes 列出搜索接口 type 参数的合法取值。
var searchTypes = []string{"all", "title", "content", "author"}

// SearchPoems 按关键词搜索诗词。
func (h *PoemHandler) SearchPoems(c *gin.Context) {
	if !checkQueryParams(c, queryLang, queryPage, queryPageSize, queryQuery, queryType) {
		return
	}

	lang, ok := parseLang(c)
	if !ok {
		return
	}
	repo := h.repo.WithLang(lang)

	query, err := searchquery.Normalize(c.Query(queryQuery))
	if errors.Is(err, searchquery.ErrEmpty) {
		respondError(c, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	searchType := c.DefaultQuery(queryType, "all")
	if !slices.Contains(searchTypes, searchType) {
		respondError(c, http.StatusBadRequest, "unsupported type "+strconv.Quote(searchType)+
			"; supported: "+strings.Join(searchTypes, ", "))
		return
	}
	if searchType != "author" {
		if err := searchquery.ValidateIndexedLength(query); err != nil {
			respondError(c, http.StatusBadRequest, err.Error())
			return
		}
	} else if err := searchquery.ValidateLiteralSubstring(query); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	pagination, ok := ParsePagination(c)
	if !ok {
		return
	}

	poems, total, err := repo.SearchPoems(c.Request.Context(), query, searchType, pagination.Page, pagination.PageSize)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "search failed")
		return
	}

	data := make([]map[string]any, len(poems))
	for i, poem := range poems {
		data[i] = formatPoem(&poem)
	}

	c.JSON(http.StatusOK, NewPaginationResponse(data, pagination, total))
}

// filterQueryKeys 列出 /poems 与 /poems/random 共用的作者、体裁、朝代过滤参数。
// 同时用于拒绝 char 与这些参数一起使用（见 RandomPoem 的说明）。
var filterQueryKeys = []string{queryAuthorID, queryAuthor, queryTypeID, queryType, queryDynastyID, queryDynasty}

// poemFilters 保存解析后的作者、体裁、朝代过滤条件。
// id 为 nil 或 typeIDs 为空表示该字段不过滤。
type poemFilters struct {
	dynastyID *int64
	authorID  *int64
	typeIDs   []int64
}

// parsePoemFilters 解析 filterQueryKeys 中的各项过滤条件，
// 并通过 repo 把按名称传入的形式（如 ?author=李白）解析为 ID。
// 遇到格式错误的 ID 返回 400、名称查不到返回 404，此时直接写响应并返回 false。
//
// 每项条件既可传 ID 也可传名称，两者同时出现时以 ID 为准。
func parsePoemFilters(c *gin.Context, repo *database.Repository) (poemFilters, bool) {
	var filters poemFilters

	// Resolve dynasty first because author identity is (name, dynasty).
	dynastyID, ok := parseInt64Query(c, queryDynastyID)
	if !ok {
		return poemFilters{}, false
	}
	switch {
	case dynastyID != nil:
		filters.dynastyID = dynastyID
	case c.Query(queryDynasty) != "":
		dynasty, err := repo.GetDynastyByName(c.Query(queryDynasty))
		if err != nil {
			respondError(c, http.StatusNotFound, "dynasty not found")
			return poemFilters{}, false
		}
		filters.dynastyID = &dynasty.ID
	}

	// A name-only lookup is accepted only when it is unambiguous. Otherwise the
	// client must add dynasty/dynasty_id or use the exact author_id.
	authorID, ok := parseInt64Query(c, queryAuthorID)
	if !ok {
		return poemFilters{}, false
	}
	switch {
	case authorID != nil:
		filters.authorID = authorID
	case c.Query(queryAuthor) != "":
		author, err := repo.GetAuthorByNameAndDynasty(c.Query(queryAuthor), filters.dynastyID)
		if errors.Is(err, database.ErrAmbiguousAuthor) {
			respondError(c, http.StatusConflict, "ambiguous author name; add dynasty/dynasty_id or use author_id")
			return poemFilters{}, false
		}
		if err != nil {
			respondError(c, http.StatusNotFound, "author not found")
			return poemFilters{}, false
		}
		filters.authorID = &author.ID
	}

	// 体裁过滤：按 ID 或名称，支持多个取值，彼此为 OR 关系
	typeIDStrs := c.QueryArray(queryTypeID)
	typeNames := c.QueryArray(queryType)
	switch {
	case len(typeIDStrs) > 0:
		for _, idStr := range typeIDStrs {
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				respondError(c, http.StatusBadRequest, queryTypeID+" must be an integer, got "+strconv.Quote(idStr))
				return poemFilters{}, false
			}
			filters.typeIDs = append(filters.typeIDs, id)
		}
	case len(typeNames) > 0:
		// 一次查询批量解析体裁名称
		ids, err := repo.GetPoetryTypeIDs(typeNames)
		if err != nil {
			respondError(c, http.StatusNotFound, "poetry type not found")
			return poemFilters{}, false
		}
		filters.typeIDs = ids
	}

	return filters, true
}

// RandomPoem 按可选条件随机返回一首诗词。
// 语言：?lang=zh-Hans（默认）或 ?lang=zh-Hant
// 过滤条件可按名称：?author=李白&type=五言绝句&type=七言绝句&dynasty=唐
// 也可按 ID：?author_id=123&type_id=456&type_id=789&dynasty_id=789
//
// v2 公网接口禁用了原飞花令单字检索 ?char=春：单字模式无法使用 trigram
// 索引，而旧实现每次做 COUNT 与随机 OFFSET 两次线性扫描。该参数保留用于返回
// 明确的 410 Gone，避免旧客户端误把它当成普通随机查询。
func (h *PoemHandler) RandomPoem(c *gin.Context) {
	if !checkQueryParams(c, append([]string{queryLang, queryChar}, filterQueryKeys...)...) {
		return
	}

	lang, ok := parseLang(c)
	if !ok {
		return
	}
	repo := h.repo.WithLang(lang)

	if char := c.Query(queryChar); char != "" {
		// A single-character LIKE pattern cannot use the FTS5 trigram index.
		// The previous implementation performed both COUNT and random OFFSET
		// scans, so this public path is disabled until a dedicated character
		// index exists.
		respondError(c, http.StatusGone, "char search is disabled because single-character queries cannot use the search index")
		return
	}

	filters, ok := parsePoemFilters(c, repo)
	if !ok {
		return
	}

	poem, err := repo.GetRandomPoem(filters.dynastyID, filters.authorID, filters.typeIDs)
	if err != nil {
		respondError(c, http.StatusNotFound, "no poems found matching the criteria")
		return
	}

	c.JSON(http.StatusOK, formatPoem(poem))
}
