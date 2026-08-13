package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenReadOnlyPreservesVerifiedDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "poetry %#?.db")
	writable, err := Open(dbPath, 1, 1)
	require.NoError(t, err)
	require.NoError(t, writable.Migrate())
	require.NoError(t, writable.Close())

	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)

	readonly, err := OpenReadOnly(dbPath, 2, 2)
	require.NoError(t, err)
	version, err := readonly.GetSchemaVersion()
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, version)
	require.Error(t, readonly.Exec("CREATE TABLE forbidden (id INTEGER)").Error)
	require.NoError(t, readonly.Close())

	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, suffix := range []string{"-wal", "-shm"} {
		_, err := os.Stat(dbPath + suffix)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}
