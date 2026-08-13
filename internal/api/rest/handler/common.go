package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// parseID 从 URL 路径参数中解析并校验 int64 类型的 ID。
// 成功时返回 ID 与 true；失败时直接写出错误响应并返回 false。
func parseID(c *gin.Context, param, entityName string) (int64, bool) {
	idStr := c.Param(param)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + entityName + " ID"})
		return 0, false
	}
	return id, true
}

// respondError 以指定状态码返回 JSON 格式的错误响应。
func respondError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}

// respondOK 返回携带数据的 JSON 成功响应。
func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// parseLang 从 lang 查询参数中解析语言变体。
// 可用取值："zh-Hans"（简体）、"zh-Hant"（繁体），以及 database.LookupLang 中的别名；
// 参数缺省时默认简体。
func parseLang(c *gin.Context) (database.Lang, bool) {
	raw := c.Query(queryLang)
	if raw == "" {
		return database.LangHans, true
	}

	lang, ok := database.LookupLang(raw)
	if !ok {
		respondError(c, http.StatusBadRequest, "unsupported lang "+strconv.Quote(raw)+"; supported: zh-Hans, zh-Hant")
		return "", false
	}
	return lang, true
}
