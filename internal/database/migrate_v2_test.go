package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateV1DatabaseToGovernedSchemaIsIdempotent(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open("file:migrate-v1?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	db := NewDBFromGorm(gormDB)

	for _, lang := range []Lang{LangHans, LangHant} {
		poemTable := PoemsTable(lang)
		require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TABLE %s (
			id INTEGER PRIMARY KEY,
			type_id INTEGER,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			content_hash TEXT,
			author_id INTEGER,
			dynasty_id INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`, poemTable)).Error)
		require.NoError(t, db.Exec(fmt.Sprintf(
			"CREATE UNIQUE INDEX idx_%s_unique ON %s(title, content_hash)", poemTable, poemTable,
		)).Error)
		require.NoError(t, db.Exec(fmt.Sprintf(
			`INSERT INTO %s(id,title,content,content_hash) VALUES (1,'旧诗','["旧正文。"]','legacy-hash')`, poemTable,
		)).Error)
	}

	require.NoError(t, db.Migrate())
	require.NoError(t, db.Migrate(), "v2 migration must be safe to repeat")

	version, err := db.GetSchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, version)

	for _, lang := range []Lang{LangHans, LangHant} {
		poemTable := PoemsTable(lang)
		var columns []struct {
			Name string `gorm:"column:name"`
		}
		require.NoError(t, db.Raw(fmt.Sprintf("PRAGMA table_info(%s)", poemTable)).Scan(&columns).Error)
		columnNames := make([]string, 0, len(columns))
		for _, column := range columns {
			columnNames = append(columnNames, column.Name)
		}
		assert.Contains(t, columnNames, "canonical_id")
		assert.Contains(t, columnNames, "canonical_fingerprint")

		var oldIndex, canonicalIndex, sourceTable, legacyRows int64
		require.NoError(t, db.Raw(
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, "idx_"+poemTable+"_unique",
		).Scan(&oldIndex).Error)
		require.NoError(t, db.Raw(
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, "idx_"+poemTable+"_canonical_id",
		).Scan(&canonicalIndex).Error)
		require.NoError(t, db.Raw(
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, PoemSourcesTable(lang),
		).Scan(&sourceTable).Error)
		require.NoError(t, db.Table(poemTable).Where("id = 1 AND title = ?", "旧诗").Count(&legacyRows).Error)
		assert.Zero(t, oldIndex)
		assert.Equal(t, int64(1), canonicalIndex)
		assert.Equal(t, int64(1), sourceTable)
		assert.Equal(t, int64(1), legacyRows)
	}

	var rejectionTable int64
	require.NoError(t, db.Raw(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='source_rejections'`,
	).Scan(&rejectionTable).Error)
	assert.Equal(t, int64(1), rejectionTable)
}

func TestMigrateRejectsLegacyAuthorIdentityEvenWhenMetadataClaimsV2(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open("file:migrate-legacy-author?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	db := NewDBFromGorm(gormDB)

	require.NoError(t, db.Exec(`CREATE TABLE authors_zh_hans (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		dynasty_id INTEGER
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO metadata(key, value) VALUES ('schema_version', '2')`).Error)

	err = db.Migrate()
	require.ErrorContains(t, err, "canonical author identity")
}
