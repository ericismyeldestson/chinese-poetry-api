// Package testutil 提供测试用的公共辅助函数。
package testutil

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// SetupTestDB 创建一个已完成迁移的内存 SQLite 数据库，
// 返回 DB 封装与 Repository，并在测试结束时自动清理。
func SetupTestDB(t *testing.T) (*database.DB, *database.Repository) {
	t.Helper()

	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err, "Failed to open in-memory database")

	db := database.NewDBFromGorm(gormDB)
	require.NoError(t, db.Migrate(), "Failed to run migrations")

	repo := database.NewRepository(db)

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db, repo
}

// SetupTestDBWithLang 创建指定语言变体的内存数据库。
func SetupTestDBWithLang(t *testing.T, lang database.Lang) (*database.DB, *database.Repository) {
	t.Helper()

	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err, "Failed to open in-memory database")

	db := database.NewDBFromGorm(gormDB)
	require.NoError(t, db.Migrate(), "Failed to run migrations")

	repo := database.NewRepositoryWithLang(db, lang)

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db, repo
}

// SetupTestGin 创建处于测试模式的 Gin 引擎。
func SetupTestGin() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// GormDB 取出 database.DB 封装内部的 GORM 实例，便于测试中直接操作数据库。
func GormDB(db *database.DB) *gorm.DB {
	return db.DB
}
