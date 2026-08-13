package graph

import (
	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// 本文件不会被自动重新生成。
//
// 它承担依赖注入的职责，需要的依赖都加在这里。

// Resolver 持有 GraphQL resolver 所需的依赖。
type Resolver struct {
	DB   *database.DB
	Repo *database.Repository
}

// NewResolver 创建 GraphQL 根 resolver。
func NewResolver(db *database.DB, repo *database.Repository) *Resolver {
	return &Resolver{
		DB:   db,
		Repo: repo,
	}
}
