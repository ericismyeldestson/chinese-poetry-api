package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// HealthHandler 处理健康检查请求。
func HealthHandler(db *database.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 检查数据库连接是否可用
		sqlDB, err := db.DB.DB()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unhealthy",
				"error":  "failed to get database connection",
			})
			return
		}

		if err := sqlDB.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unhealthy",
				"error":  "database connection failed",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "healthy",
		})
	}
}

// StatsHandler 返回指定语言产品的整体统计数据。
//
// 作者数等语义指标不能假定简繁始终相同，因此与其他读取接口一样
// 接受 ?lang=zh-Hans|zh-Hant，默认 zh-Hans。
// 而 /health 则有意保持宽松，因为探针常会附加防缓存参数，
// 若一并拒绝，会让本来健康的服务在健康检查中被判为异常。
func StatsHandler(repo *database.Repository) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !checkQueryParams(c, queryLang) {
			return
		}
		lang, ok := parseLang(c)
		if !ok {
			return
		}

		stats, err := repo.WithLang(lang).GetStatistics()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to get statistics",
			})
			return
		}

		c.JSON(http.StatusOK, stats)
	}
}
