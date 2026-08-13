package helpers

import (
	"strconv"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// ParseOptionalInt64 把字符串指针解析为 *int64，为 nil 或空串时返回 nil。
func ParseOptionalInt64(s *string) (*int64, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(*s, 10, 64)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// ParseFilterIDs 依次解析朝代、作者、体裁三个 ID，任一解析失败即返回错误。
func ParseFilterIDs(dynastyID, authorID, typeID *string) (*int64, *int64, *int64, error) {
	dID, err := ParseOptionalInt64(dynastyID)
	if err != nil {
		return nil, nil, nil, err
	}
	aID, err := ParseOptionalInt64(authorID)
	if err != nil {
		return nil, nil, nil, err
	}
	tID, err := ParseOptionalInt64(typeID)
	if err != nil {
		return nil, nil, nil, err
	}
	return dID, aID, tID, nil
}

// ParseLangString 把字符串转换为 Lang，支持 "zh-Hans"（简体）与 "zh-Hant"（繁体），
// 其余取值一律按简体处理。
func ParseLangString(langStr string) database.Lang {
	if langStr == "zh-Hant" {
		return database.LangHant
	}
	return database.LangHans
}

// ParseLangPointer 把 *Lang 转换为 Lang，指针为 nil 时返回简体。
func ParseLangPointer(lang *database.Lang) database.Lang {
	if lang != nil {
		return *lang
	}
	return database.LangHans
}

// Pagination 保存分页参数。
type Pagination struct {
	Page     int
	PageSize int
}

// NewPagination 创建并校正分页参数：page 不小于 1，pageSize 限定在 1-100，
// 非法取值分别回落到 page=1、pageSize=20。
func NewPagination(page, pageSize int) *Pagination {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return &Pagination{
		Page:     page,
		PageSize: pageSize,
	}
}

// Offset 换算出当前页对应的查询偏移量。
func (p *Pagination) Offset() int {
	return (p.Page - 1) * p.PageSize
}
