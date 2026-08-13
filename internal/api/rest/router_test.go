package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/config"
)

func TestSetupRouterIgnoresSpoofedForwardedForByDefault(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Mode: "test"},
		RateLimit: config.RateLimitConfig{
			Enabled:           true,
			RequestsPerSecond: 1,
			Burst:             2,
		},
	}

	router := SetupRouter(cfg, nil, nil)
	codes := make([]int, 3)
	for i, forwardedIP := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		req := httptest.NewRequest(http.MethodGet, "/not-found", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		req.Header.Set("X-Forwarded-For", forwardedIP)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		codes[i] = w.Code
	}

	assert.Equal(t, []int{http.StatusNotFound, http.StatusNotFound, http.StatusTooManyRequests}, codes,
		"rotating an untrusted X-Forwarded-For value must not create a fresh rate-limit bucket")
}
