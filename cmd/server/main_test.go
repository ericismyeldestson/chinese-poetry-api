package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/config"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/graph"
)

func TestNewHTTPServerAppliesTimeoutConfiguration(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{
		Port:              9123,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       11 * time.Second,
		WriteTimeout:      23 * time.Second,
		IdleTimeout:       47 * time.Second,
	}}
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	srv := newHTTPServer(cfg, h)

	assert.Equal(t, ":9123", srv.Addr)
	assert.NotNil(t, srv.Handler)
	assert.Equal(t, 3*time.Second, srv.ReadHeaderTimeout)
	assert.Equal(t, 11*time.Second, srv.ReadTimeout)
	assert.Equal(t, 23*time.Second, srv.WriteTimeout)
	assert.Equal(t, 47*time.Second, srv.IdleTimeout)
}

func TestGraphQLConfigurationIsEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolver := graph.NewResolver(nil, nil)

	request := func(cfg config.GraphQLConfig, query string) *httptest.ResponseRecorder {
		t.Helper()
		router := gin.New()
		router.POST("/graphql", graphqlHandler(resolver, cfg))
		body := []byte(`{"query":` + strconv.Quote(query) + `}`)
		req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("introspection can be disabled", func(t *testing.T) {
		cfg := config.GraphQLConfig{Introspection: false, ComplexityLimit: 100, MaxRequestBodyBytes: 4096}
		w := request(cfg, `query { __schema { queryType { name } } }`)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "introspection disabled")
	})

	t.Run("introspection can be enabled", func(t *testing.T) {
		cfg := config.GraphQLConfig{Introspection: true, ComplexityLimit: 100, MaxRequestBodyBytes: 4096}
		w := request(cfg, `query { __schema { queryType { name } } }`)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"name":"Query"`)
	})

	t.Run("complexity limit rejects an expensive operation", func(t *testing.T) {
		cfg := config.GraphQLConfig{Introspection: true, ComplexityLimit: 1, MaxRequestBodyBytes: 4096}
		w := request(cfg, `query { poems { edges { node { title content } } totalCount } }`)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "exceeds the limit of 1")
	})

	t.Run("request body limit rejects oversized payloads", func(t *testing.T) {
		cfg := config.GraphQLConfig{Introspection: true, ComplexityLimit: 100, MaxRequestBodyBytes: 16}
		w := request(cfg, `query { __typename }`)
		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
		assert.Contains(t, w.Body.String(), "request body too large")
	})

	t.Run("request body limit also bounds unknown content length", func(t *testing.T) {
		cfg := config.GraphQLConfig{Introspection: true, ComplexityLimit: 100, MaxRequestBodyBytes: 16}
		router := gin.New()
		router.POST("/graphql", graphqlHandler(resolver, cfg))
		req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader([]byte(`{"query":"query { __typename }"}`)))
		req.ContentLength = -1 // simulate chunked transfer encoding
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.NotEqual(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "request body too large")
	})
}
