package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiterAllowsThenBlocks(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rl := NewRateLimiter(1, 2)
	defer rl.Stop()

	router := gin.New()
	router.Use(rl.Middleware())
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	codes := make([]int, 3)
	for i := range codes {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		codes[i] = w.Code
	}

	// 前两次请求耗尽 burst，同一瞬间的第三次请求应被拒绝
	assert.Equal(t, []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests}, codes)
}

// TestRateLimiterEvictsIdleClients covers bounded lifecycle of per-client
// state; every retained client key otherwise consumes memory until shutdown.
func TestRateLimiterEvictsIdleClients(t *testing.T) {
	rl := NewRateLimiter(10, 10)
	defer rl.Stop()

	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		rl.getLimiter(ip)
	}
	require.Len(t, rl.limiters, 3)

	// 闲置时长还不够，不应回收任何条目
	rl.sweep(time.Now().Add(rl.idleTTL / 2))
	assert.Len(t, rl.limiters, 3)

	// 时钟推进越过 TTL 期间，保持其中一个客户端处于活跃状态
	future := time.Now().Add(rl.idleTTL + time.Minute)
	rl.limiters["192.0.2.2"].lastSeen = future

	rl.sweep(future)
	require.Len(t, rl.limiters, 1, "only the recently seen client should survive")
	_, ok := rl.limiters["192.0.2.2"]
	assert.True(t, ok)
}

func TestRateLimiterIdleTTLTracksSlowBucketRefill(t *testing.T) {
	rl := NewRateLimiter(0.001, 2)
	defer rl.Stop()
	assert.GreaterOrEqual(t, rl.idleTTL, 2000*time.Second)

	rl.getLimiter("192.0.2.10")
	lastSeen := rl.limiters["192.0.2.10"].lastSeen
	rl.sweep(lastSeen.Add(10 * time.Minute))
	assert.Contains(t, rl.limiters, "192.0.2.10", "a partially refilled bucket must not be reset")
	rl.sweep(lastSeen.Add(rl.idleTTL + time.Second))
	assert.NotContains(t, rl.limiters, "192.0.2.10")
}

func TestRateLimiterSeparatesClients(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	defer rl.Stop()

	a := rl.getLimiter("192.0.2.1")
	b := rl.getLimiter("192.0.2.2")
	assert.NotSame(t, a, b, "each client gets its own limiter")
	assert.Same(t, a, rl.getLimiter("192.0.2.1"), "the same client reuses its limiter")

	assert.True(t, a.Allow())
	assert.False(t, a.Allow(), "first client exhausted its burst")
	assert.True(t, b.Allow(), "second client is unaffected")
}

func TestRateLimiterCapsTrackedClients(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	defer rl.Stop()
	rl.maxKeys = 2

	rl.getLimiter("192.0.2.1")
	rl.getLimiter("192.0.2.2")
	overflowA := rl.getLimiter("192.0.2.3")
	overflowB := rl.getLimiter("192.0.2.4")

	require.Len(t, rl.limiters, 2)
	assert.Same(t, rl.overflow, overflowA)
	assert.Same(t, overflowA, overflowB)
}

func TestRateLimiterConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(1000, 1000)
	defer rl.Stop()

	// 需在 -race 下运行：getLimiter 与 sweep 都会修改同一个 map
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			for range 20 {
				rl.getLimiter(string(rune('a' + i%10))).Allow()
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			rl.sweep(time.Now())
		}
	})
	wg.Wait()

	assert.LessOrEqual(t, len(rl.limiters), 10)
}

func TestRateLimiterStopIsIdempotent(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.Stop()
	assert.NotPanics(t, rl.Stop)
}
