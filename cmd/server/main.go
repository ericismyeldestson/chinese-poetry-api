package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/api/rest"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/config"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/graph"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/graph/generated"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/logger"
)

func newGraphQLServer(resolver *graph.Resolver, cfg config.GraphQLConfig) *handler.Server {
	schema := generated.NewExecutableSchema(generated.Config{
		Resolvers:  resolver,
		Complexity: graph.ComplexityRoot(),
	})
	h := handler.New(schema)
	h.AddTransport(transport.Options{})
	h.AddTransport(transport.POST{})
	h.SetQueryCache(lru.New[*ast.QueryDocument](1000))
	// Retain the previous server's bounded APQ support while configuring the
	// production transports and security extensions explicitly.
	h.Use(extension.AutomaticPersistedQuery{Cache: lru.New[string](100)})
	h.Use(extension.FixedComplexityLimit(cfg.ComplexityLimit))
	if cfg.Introspection {
		h.Use(extension.Introspection{})
	}
	// Parent resolvers record the selected language in this operation-scoped
	// registry so nested Author resolvers cannot accidentally fall back to the
	// Simplified Chinese tables.
	h.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(graph.WithLanguageState(ctx))
	})
	return h
}

func newHTTPServer(cfg *config.Config, httpHandler http.Handler) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           httpHandler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}
}

// graphqlHandler 构造 GraphQL 请求的 Gin handler。
func graphqlHandler(resolver *graph.Resolver, cfg config.GraphQLConfig) gin.HandlerFunc {
	h := newGraphQLServer(resolver, cfg)

	return func(c *gin.Context) {
		if c.Request.ContentLength > cfg.MaxRequestBodyBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		originalBody := c.Request.Body
		payload, err := io.ReadAll(io.LimitReader(originalBody, cfg.MaxRequestBodyBytes+1))
		_ = originalBody.Close()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}
		if int64(len(payload)) > cfg.MaxRequestBodyBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(payload))
		c.Request.ContentLength = int64(len(payload))
		h.ServeHTTP(c.Writer, c.Request)
	}
}

// playgroundHandler 构造 GraphQL Playground 页面的 Gin handler。
func playgroundHandler() gin.HandlerFunc {
	h := playground.Handler("GraphQL", "/graphql")

	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}

func main() {
	// 初始化日志
	debug := os.Getenv("GIN_MODE") != "release"
	logger.Init(debug)
	defer logger.Sync()

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml"
	}
	// An explicit release configuration is part of the deployment contract.
	// Missing, malformed, or invalid settings fail closed instead of silently
	// starting with a different port, proxy trust boundary, or rate limit.
	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.String("path", configPath), zap.Error(err))
	}

	logger.Info("Starting Chinese Poetry API server",
		zap.String("database", cfg.Database.Path),
		zap.Int("port", cfg.Server.Port),
		zap.Int("max_open_conns", cfg.Database.MaxOpenConns),
		zap.Int("max_idle_conns", cfg.Database.MaxIdleConns),
	)

	// The API is read-only. Keep the verified release database immutable at
	// runtime so startup checksums remain meaningful and no WAL/SHM sidecars are
	// created inside the data volume.
	db, err := database.OpenReadOnly(cfg.Database.Path, cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns)
	if err != nil {
		logger.Fatal("Failed to open database", zap.Error(err))
	}
	defer func() { _ = db.Close() }()

	// 创建仓储
	repo := database.NewRepository(db)

	// 创建 GraphQL resolver
	resolver := graph.NewResolver(db, repo)

	// 初始化 Gin 路由
	router := rest.SetupRouter(cfg, db, repo)

	// 注册 GraphQL 相关路由
	router.POST("/graphql", graphqlHandler(resolver, cfg.GraphQL))
	if cfg.GraphQL.Playground {
		router.GET("/playground", playgroundHandler())
		logger.Info("GraphQL Playground enabled", zap.String("path", "/playground"))
	}

	// 构造 HTTP 服务
	srv := newHTTPServer(cfg, router)

	// 在独立协程中启动服务
	go func() {
		logger.Info("Server started",
			zap.Int("port", cfg.Server.Port),
			zap.String("rest_api", fmt.Sprintf("http://localhost:%d/api/v1", cfg.Server.Port)),
			zap.String("graphql", fmt.Sprintf("http://localhost:%d/graphql", cfg.Server.Port)),
		)

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	// 带超时的优雅退出
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Warn("Server forced to shutdown", zap.Error(err))
	}

	logger.Info("Server exited")
}
