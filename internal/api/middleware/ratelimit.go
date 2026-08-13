package middleware

import (
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const (
	// sweepInterval 是回收过期限流器的间隔。
	sweepInterval = time.Minute

	// Bound per-client state even under a distributed source-IP spray. Clients
	// beyond this cap share one overflow bucket until the next idle sweep frees
	// tracked entries.
	maxTrackedClients = 65536
)

// clientLimiter 是单个客户端的限流器及其最近一次使用时间。
type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter 保存限流配置及各客户端的限流器。
type RateLimiter struct {
	limiters map[string]*clientLimiter
	mu       sync.Mutex
	rps      rate.Limit
	burst    int
	maxKeys  int
	overflow *rate.Limiter
	idleTTL  time.Duration

	stop     chan struct{}
	stopOnce sync.Once
}

// NewRateLimiter 创建限流器，并启动一个协程回收闲置超过 idleTTL 的限流器，
// 用完需调用 Stop 释放。
//
// Idle eviction and a hard key cap keep the state bounded. Router setup also
// disables forwarded-client headers unless the operator explicitly configures
// a trusted reverse proxy.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	// Eviction is safe only once an empty bucket would have refilled completely.
	// Round up to avoid granting a fresh burst earlier than the configured rate.
	refillSeconds := math.Ceil(float64(burst) / rps)
	idleTTL := time.Duration(refillSeconds * float64(time.Second))
	if idleTTL < sweepInterval {
		idleTTL = sweepInterval
	}
	rl := &RateLimiter{
		limiters: make(map[string]*clientLimiter),
		rps:      rate.Limit(rps),
		burst:    burst,
		maxKeys:  maxTrackedClients,
		overflow: rate.NewLimiter(rate.Limit(rps), burst),
		idleTTL:  idleTTL,
		stop:     make(chan struct{}),
	}

	go rl.sweepLoop()

	return rl
}

// Stop 结束回收协程，可安全重复调用。
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stop) })
}

func (rl *RateLimiter) sweepLoop() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stop:
			return
		case now := <-ticker.C:
			rl.sweep(now)
		}
	}
}

// sweep 清理闲置超过 idleTTL 的限流器。
func (rl *RateLimiter) sweep(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	for key, cl := range rl.limiters {
		if now.Sub(cl.lastSeen) > rl.idleTTL {
			delete(rl.limiters, key)
		}
	}
}

// getLimiter 返回指定 key（客户端 IP）对应的限流器，
// 同时记录访问时间，以便后续回收闲置客户端的限流器。
func (rl *RateLimiter) getLimiter(key string) *rate.Limiter {
	now := time.Now()

	// 这里用普通 Mutex 而非 RWMutex：每次调用都要写 lastSeen，
	// RWMutex 所能带来的只读快路径已不复存在。
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if cl, exists := rl.limiters[key]; exists {
		cl.lastSeen = now
		return cl.limiter
	}
	if len(rl.limiters) >= rl.maxKeys {
		return rl.overflow
	}

	limiter := rate.NewLimiter(rl.rps, rl.burst)
	rl.limiters[key] = &clientLimiter{limiter: limiter, lastSeen: now}

	return limiter
}

// Middleware 返回用于限流的 Gin 中间件。
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP()
		limiter := rl.getLimiter(key)

		if !limiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "rate limit exceeded",
				"message": "too many requests, please try again later",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
