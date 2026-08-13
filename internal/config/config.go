package config

import (
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const maxSQLiteConnections = 16

// Config 是应用的全部配置。
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`
	GraphQL   GraphQLConfig   `mapstructure:"graphql"`
}

// ServerConfig 是服务端配置。
type ServerConfig struct {
	Port              int           `mapstructure:"port"`
	Mode              string        `mapstructure:"mode"`
	TrustedProxies    []string      `mapstructure:"trusted_proxies"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	ReadTimeout       time.Duration `mapstructure:"read_timeout"`
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
}

// DatabaseConfig 是数据库配置。
type DatabaseConfig struct {
	Path         string `mapstructure:"path"`
	MaxOpenConns int    `mapstructure:"max_open_conns"` // 最大连接数
	MaxIdleConns int    `mapstructure:"max_idle_conns"` // 最大空闲连接数
}

// RateLimitConfig 是限流配置。
type RateLimitConfig struct {
	Enabled           bool    `mapstructure:"enabled"`
	RequestsPerSecond float64 `mapstructure:"requests_per_second"`
	Burst             int     `mapstructure:"burst"`
}

// GraphQLConfig 是 GraphQL 相关配置。
type GraphQLConfig struct {
	Playground          bool  `mapstructure:"playground"`
	Introspection       bool  `mapstructure:"introspection"`
	ComplexityLimit     int   `mapstructure:"complexity_limit"`
	MaxRequestBodyBytes int64 `mapstructure:"max_request_body_bytes"`
}

// Load 从配置文件与环境变量中加载配置。
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// 设置默认值
	setDefaults(v)

	// 指定了配置文件则读取
	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// 环境变量优先级更高，覆盖前面的取值
	if err := bindEnvVars(v); err != nil {
		return nil, fmt.Errorf("invalid environment configuration: %w", err)
	}

	var cfg Config
	if err := v.UnmarshalExact(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// 未配置连接池大小时按 CPU 核数自动推算
	cfg.applyConnectionPoolDefaults()

	// 校验配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.mode", "release")
	v.SetDefault("server.read_header_timeout", 5*time.Second)
	v.SetDefault("server.read_timeout", 15*time.Second)
	v.SetDefault("server.write_timeout", 30*time.Second)
	v.SetDefault("server.idle_timeout", 60*time.Second)
	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.requests_per_second", 10.0)
	v.SetDefault("rate_limit.burst", 20)
	v.SetDefault("graphql.playground", false)
	v.SetDefault("graphql.introspection", false)
	v.SetDefault("graphql.complexity_limit", 1000)
	v.SetDefault("graphql.max_request_body_bytes", int64(1<<20))
	// 数据库连接池，0 表示自动推算（在 Load 中依据 runtime.NumCPU 确定）
	v.SetDefault("database.max_open_conns", 0)
	v.SetDefault("database.max_idle_conns", 0)
}

func bindEnvVars(v *viper.Viper) error {
	// 服务端
	if port := os.Getenv("PORT"); port != "" {
		v.Set("server.port", port)
	}
	if mode := os.Getenv("GIN_MODE"); mode != "" {
		v.Set("server.mode", mode)
	}
	if raw, ok := os.LookupEnv("TRUSTED_PROXIES"); ok {
		var proxies []string
		for _, proxy := range strings.Split(raw, ",") {
			if proxy = strings.TrimSpace(proxy); proxy != "" {
				proxies = append(proxies, proxy)
			}
		}
		v.Set("server.trusted_proxies", proxies)
	}
	for envName, configKey := range map[string]string{
		"HTTP_READ_HEADER_TIMEOUT": "server.read_header_timeout",
		"HTTP_READ_TIMEOUT":        "server.read_timeout",
		"HTTP_WRITE_TIMEOUT":       "server.write_timeout",
		"HTTP_IDLE_TIMEOUT":        "server.idle_timeout",
	} {
		if value := os.Getenv(envName); value != "" {
			v.Set(configKey, value)
		}
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	dbFile := os.Getenv("DB_FILE")
	if dbFile == "" {
		dbFile = "poetry.db"
	}
	if dbFile == "." || dbFile == ".." || filepath.Base(dbFile) != dbFile {
		return fmt.Errorf("DB_FILE must be a single file name")
	}

	// 统一使用 poetry.db，其中同时包含简体与繁体两套表；
	// 具体查哪套由 API 请求中的 lang 参数决定
	v.Set("database.path", filepath.Join(dataDir, dbFile))

	// 限流
	if enabled := os.Getenv("RATE_LIMIT_ENABLED"); enabled != "" {
		v.Set("rate_limit.enabled", enabled)
	}
	if rps := os.Getenv("RATE_LIMIT_RPS"); rps != "" {
		v.Set("rate_limit.requests_per_second", rps)
	}
	if burst := os.Getenv("RATE_LIMIT_BURST"); burst != "" {
		v.Set("rate_limit.burst", burst)
	}

	// GraphQL
	if value := os.Getenv("GRAPHQL_INTROSPECTION"); value != "" {
		v.Set("graphql.introspection", value)
	}
	if value := os.Getenv("GRAPHQL_COMPLEXITY_LIMIT"); value != "" {
		v.Set("graphql.complexity_limit", value)
	}
	if value := os.Getenv("GRAPHQL_MAX_REQUEST_BODY_BYTES"); value != "" {
		v.Set("graphql.max_request_body_bytes", value)
	}

	// 数据库连接池
	if maxOpen := os.Getenv("DB_MAX_OPEN_CONNS"); maxOpen != "" {
		v.Set("database.max_open_conns", maxOpen)
	}
	if maxIdle := os.Getenv("DB_MAX_IDLE_CONNS"); maxIdle != "" {
		v.Set("database.max_idle_conns", maxIdle)
	}
	return nil
}

// Validate 校验配置的合法性。
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Server.Port)
	}

	if c.Server.Mode != "debug" && c.Server.Mode != "release" && c.Server.Mode != "test" {
		return fmt.Errorf("invalid server mode: %s (must be 'debug', 'release', or 'test')", c.Server.Mode)
	}

	for _, proxy := range c.Server.TrustedProxies {
		if net.ParseIP(proxy) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(proxy); err != nil {
			return fmt.Errorf("invalid trusted proxy %q: must be an IP address or CIDR", proxy)
		}
	}

	for name, timeout := range map[string]time.Duration{
		"read_header_timeout": c.Server.ReadHeaderTimeout,
		"read_timeout":        c.Server.ReadTimeout,
		"write_timeout":       c.Server.WriteTimeout,
		"idle_timeout":        c.Server.IdleTimeout,
	} {
		if timeout <= 0 {
			return fmt.Errorf("server %s must be positive", name)
		}
	}

	if c.Database.Path == "" {
		return fmt.Errorf("database path cannot be empty")
	}
	if c.Database.MaxOpenConns < 1 || c.Database.MaxOpenConns > maxSQLiteConnections {
		return fmt.Errorf("database max_open_conns must be between 1 and %d", maxSQLiteConnections)
	}
	if c.Database.MaxIdleConns < 1 || c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("database max_idle_conns must be between 1 and max_open_conns")
	}

	if c.RateLimit.Enabled {
		if c.RateLimit.RequestsPerSecond <= 0 ||
			math.IsNaN(c.RateLimit.RequestsPerSecond) ||
			math.IsInf(c.RateLimit.RequestsPerSecond, 0) {
			return fmt.Errorf("rate limit requests_per_second must be positive")
		}

		if c.RateLimit.Burst <= 0 {
			return fmt.Errorf("rate limit burst must be positive")
		}
		refillSeconds := float64(c.RateLimit.Burst) / c.RateLimit.RequestsPerSecond
		if math.IsInf(refillSeconds, 0) || refillSeconds > float64((365*24*time.Hour)/time.Second) {
			return fmt.Errorf("rate limit burst refill window must not exceed one year")
		}
	}

	if c.GraphQL.ComplexityLimit <= 0 {
		return fmt.Errorf("graphql complexity_limit must be positive")
	}

	if c.GraphQL.MaxRequestBodyBytes <= 0 || c.GraphQL.MaxRequestBodyBytes > 16<<20 {
		return fmt.Errorf("graphql max_request_body_bytes must be between 1 and %d", 16<<20)
	}

	return nil
}

// applyConnectionPoolDefaults 依据 CPU 核数为连接池设置合理的默认值。
func (c *Config) applyConnectionPoolDefaults() {
	numCPU := runtime.NumCPU()
	// SQLite permits concurrent readers, but write transactions are still
	// serialized. Cap the pool to prevent public request bursts from turning
	// into a large queue of lock contenders and goroutines.
	// max_open_conns 未配置（为 0 或负数）时自动推算
	if c.Database.MaxOpenConns <= 0 {
		// 按核数自适应：
		//   - 多核（>4）：直接取核数，并行度已足够
		//   - 少核（≤4）：取核数的两倍，以更好地利用 I/O 等待时间
		//   - 默认统一以 16 封顶，避免 SQLite 锁竞争和连接队列膨胀
		if numCPU > 4 {
			c.Database.MaxOpenConns = min(numCPU, maxSQLiteConnections)
		} else {
			c.Database.MaxOpenConns = min(numCPU*2, maxSQLiteConnections)
		}
	}

	// max_idle_conns 未配置时自动推算
	if c.Database.MaxIdleConns <= 0 {
		// 空闲连接数取最大连接数的一半左右
		c.Database.MaxIdleConns = max(c.Database.MaxOpenConns/2, 1)
	}
}
