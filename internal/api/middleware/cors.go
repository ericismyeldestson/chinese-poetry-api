package middleware

import (
	"github.com/gin-gonic/gin"
)

// CORS 返回处理跨域请求的中间件。
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		// A wildcard origin cannot be combined with credentialed CORS. This API
		// is public and does not use browser cookies, so omit the credentials
		// header instead of advertising an invalid/insecure combination.
		c.Writer.Header().Del("Access-Control-Allow-Credentials")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
