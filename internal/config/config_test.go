package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPublicServerDefaults(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)

	assert.Nil(t, cfg.Server.TrustedProxies)
	assert.Equal(t, 5*time.Second, cfg.Server.ReadHeaderTimeout)
	assert.Equal(t, 15*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 60*time.Second, cfg.Server.IdleTimeout)
	assert.False(t, cfg.GraphQL.Introspection)
	assert.Equal(t, 1000, cfg.GraphQL.ComplexityLimit)
	assert.EqualValues(t, 1<<20, cfg.GraphQL.MaxRequestBodyBytes)
	assert.LessOrEqual(t, cfg.Database.MaxOpenConns, 16)
}

func TestLoadRejectsMalformedEnvironmentOverrides(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "port", key: "PORT", value: "not-a-port"},
		{name: "rate enabled", key: "RATE_LIMIT_ENABLED", value: "sometimes"},
		{name: "rate", key: "RATE_LIMIT_RPS", value: "fast"},
		{name: "rate NaN", key: "RATE_LIMIT_RPS", value: "NaN"},
		{name: "rate infinity", key: "RATE_LIMIT_RPS", value: "+Inf"},
		{name: "burst", key: "RATE_LIMIT_BURST", value: "many"},
		{name: "graphql complexity", key: "GRAPHQL_COMPLEXITY_LIMIT", value: "large"},
		{name: "database pool", key: "DB_MAX_OPEN_CONNS", value: "several"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			_, err := Load("")
			require.Error(t, err)
		})
	}
}

func TestLoadRejectsUnknownNestedConfigurationKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`rate_limit:
  requests_per_second_typo: 1000000
`), 0o600))

	_, err := Load(configPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requests_per_second_typo")
}

func TestLoadPublicServerEnvironmentOverrides(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", " 127.0.0.1, 10.0.0.0/8 ")
	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "7s")
	t.Setenv("HTTP_READ_TIMEOUT", "17s")
	t.Setenv("HTTP_WRITE_TIMEOUT", "37s")
	t.Setenv("HTTP_IDLE_TIMEOUT", "67s")
	t.Setenv("GRAPHQL_INTROSPECTION", "true")
	t.Setenv("GRAPHQL_COMPLEXITY_LIMIT", "42")
	t.Setenv("GRAPHQL_MAX_REQUEST_BODY_BYTES", "2048")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, []string{"127.0.0.1", "10.0.0.0/8"}, cfg.Server.TrustedProxies)
	assert.Equal(t, 7*time.Second, cfg.Server.ReadHeaderTimeout)
	assert.Equal(t, 17*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 37*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 67*time.Second, cfg.Server.IdleTimeout)
	assert.True(t, cfg.GraphQL.Introspection)
	assert.Equal(t, 42, cfg.GraphQL.ComplexityLimit)
	assert.EqualValues(t, 2048, cfg.GraphQL.MaxRequestBodyBytes)
}

func TestLoadRejectsInvalidTrustedProxy(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "not-a-proxy")
	_, err := Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid trusted proxy")
}

func TestLoadUsesStartupDatabaseLocation(t *testing.T) {
	t.Setenv("DATA_DIR", "/var/lib/poetry corpus")
	t.Setenv("DB_FILE", "release.db")

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/poetry corpus/release.db", cfg.Database.Path)
}

func TestLoadRejectsDatabaseFileTraversal(t *testing.T) {
	t.Setenv("DB_FILE", "../outside.db")
	_, err := Load("")
	require.ErrorContains(t, err, "DB_FILE must be a single file name")
}

func TestDisabledRateLimitDoesNotRequireBucketSettings(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	cfg.RateLimit = RateLimitConfig{Enabled: false}
	assert.NoError(t, cfg.Validate())
}

func TestValidateCapsSQLiteConnectionPool(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	cfg.Database.MaxOpenConns = 17
	assert.ErrorContains(t, cfg.Validate(), "max_open_conns")

	cfg.Database.MaxOpenConns = 4
	cfg.Database.MaxIdleConns = 5
	assert.ErrorContains(t, cfg.Validate(), "max_idle_conns")
}

func TestCheckedInConfigDeclaresPublicSafetySettings(t *testing.T) {
	cfg, err := Load("../../config.yaml")
	require.NoError(t, err)

	assert.Empty(t, cfg.Server.TrustedProxies)
	assert.Equal(t, 5*time.Second, cfg.Server.ReadHeaderTimeout)
	assert.Equal(t, 15*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 60*time.Second, cfg.Server.IdleTimeout)
	assert.Equal(t, 1000, cfg.GraphQL.ComplexityLimit)
	assert.EqualValues(t, 1<<20, cfg.GraphQL.MaxRequestBodyBytes)
}
