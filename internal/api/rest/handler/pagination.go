package handler

import "github.com/gin-gonic/gin"

// PaginationParams 保存分页参数。
type PaginationParams struct {
	Page     int
	PageSize int
}

// Offset 换算出查询时使用的偏移量。
func (p PaginationParams) Offset() int {
	return (p.Page - 1) * p.PageSize
}

// 分页的默认值与上限。
const (
	DefaultPage     = 1
	DefaultPageSize = 20
	MaxPage         = 1000
	MaxPageSize     = 100
)

// ParsePagination 从请求上下文中解析分页参数。
func ParsePagination(c *gin.Context) (PaginationParams, bool) {
	page, ok := parseIntQuery(c, queryPage, DefaultPage, 1, MaxPage)
	if !ok {
		return PaginationParams{}, false
	}

	pageSize, ok := parseIntQuery(c, queryPageSize, DefaultPageSize, 1, MaxPageSize)
	if !ok {
		return PaginationParams{}, false
	}

	return PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}, true
}

// NewPaginationResponse 构造统一格式的分页响应。
func NewPaginationResponse(data any, params PaginationParams, total int64) gin.H {
	totalPages := (int(total) + params.PageSize - 1) / params.PageSize

	return gin.H{
		"data": data,
		"pagination": gin.H{
			"page":        params.Page,
			"page_size":   params.PageSize,
			"total":       total,
			"total_pages": totalPages,
		},
	}
}
