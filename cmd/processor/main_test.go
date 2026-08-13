package main

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	appdatabase "github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/loader"
)

func TestProcessUnifiedDatabaseProducesPortableSingleFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "poetry.db")
	require.NoError(t, processUnifiedDatabase(dbPath, nil, nil, 1))

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	var journalMode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journalMode))
	require.Equal(t, "delete", journalMode)

	for _, suffix := range []string{"-wal", "-shm"} {
		_, err := os.Stat(dbPath + suffix)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestNormalizeSQLiteStatisticsCanonicalizesPhysicalRowOrder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "statistics.db")
	db, err := appdatabase.Open(dbPath, 1, 1)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	for _, statement := range []string{
		"CREATE TABLE alpha (id INTEGER PRIMARY KEY, value TEXT NOT NULL)",
		"CREATE INDEX alpha_value ON alpha(value)",
		"CREATE TABLE omega (id INTEGER PRIMARY KEY, value TEXT NOT NULL)",
		"CREATE INDEX omega_value ON omega(value)",
		"INSERT INTO alpha(value) VALUES ('a'), ('b'), ('c')",
		"INSERT INTO omega(value) VALUES ('x'), ('y'), ('z')",
		"ANALYZE",
		`CREATE TEMP TABLE reversed_stat1 AS
		 SELECT tbl, idx, stat FROM sqlite_stat1
		 ORDER BY tbl COLLATE BINARY DESC, idx COLLATE BINARY DESC, stat COLLATE BINARY DESC`,
		"DELETE FROM sqlite_stat1",
		`INSERT INTO sqlite_stat1(tbl, idx, stat)
		 SELECT tbl, idx, stat FROM reversed_stat1`,
		"DROP TABLE reversed_stat1",
	} {
		require.NoError(t, db.Exec(statement).Error)
	}

	statOrder := func(orderBy string) string {
		var value string
		require.NoError(t, db.Raw(
			`SELECT group_concat(quote(tbl) || '|' || quote(idx) || '|' || quote(stat), char(10))
			 FROM (SELECT tbl, idx, stat FROM sqlite_stat1 ORDER BY `+orderBy+`)`,
		).Scan(&value).Error)
		return value
	}

	require.NotEqual(t, statOrder("rowid"), statOrder("tbl COLLATE BINARY, idx COLLATE BINARY, stat COLLATE BINARY"))
	require.NoError(t, normalizeSQLiteStatistics(db))
	require.Equal(t, statOrder("tbl COLLATE BINARY, idx COLLATE BINARY, stat COLLATE BINARY"), statOrder("rowid"))
	require.NoError(t, db.Exec("VACUUM").Error)
	require.Equal(t, statOrder("tbl COLLATE BINARY, idx COLLATE BINARY, stat COLLATE BINARY"), statOrder("rowid"))
}

func TestCanonicalSQLiteSnapshotEliminatesWriteHistory(t *testing.T) {
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.db")
	seed, err := appdatabase.Open(seedPath, 1, 1)
	require.NoError(t, err)
	for _, statement := range []string{
		"CREATE TABLE entries (id INTEGER PRIMARY KEY, value TEXT NOT NULL)",
		"CREATE INDEX entries_value ON entries(value)",
		"INSERT INTO entries(value) VALUES ('stable-a'), ('stable-b')",
		"ANALYZE",
	} {
		require.NoError(t, seed.Exec(statement).Error)
	}
	require.NoError(t, normalizeSQLiteStatistics(seed))
	var checkpoint struct {
		Busy       int `gorm:"column:busy"`
		Log        int `gorm:"column:log"`
		Checkpoint int `gorm:"column:checkpointed"`
	}
	require.NoError(t, seed.Raw("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&checkpoint).Error)
	require.Zero(t, checkpoint.Busy)
	require.Equal(t, checkpoint.Log, checkpoint.Checkpoint)
	require.NoError(t, seed.Close())

	seedBytes, err := os.ReadFile(seedPath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(seedBytes), 100)
	directPath := filepath.Join(dir, "direct.db")
	historyPath := filepath.Join(dir, "history.db")
	require.NoError(t, os.WriteFile(directPath, seedBytes, 0o600))
	historyBytes := append([]byte(nil), seedBytes...)
	changeCounter := binary.BigEndian.Uint32(historyBytes[24:28]) + 1
	binary.BigEndian.PutUint32(historyBytes[24:28], changeCounter)
	binary.BigEndian.PutUint32(historyBytes[92:96], changeCounter)
	schemaCookie := binary.BigEndian.Uint32(historyBytes[40:44]) + 1
	binary.BigEndian.PutUint32(historyBytes[40:44], schemaCookie)
	require.NoError(t, os.WriteFile(historyPath, historyBytes, 0o600))
	require.NotEqual(t, seedBytes[24:28], historyBytes[24:28])
	require.NotEqual(t, seedBytes[40:44], historyBytes[40:44])

	buildSnapshot := func(dbPath string) []byte {
		db, err := appdatabase.Open(dbPath, 1, 1)
		require.NoError(t, err)
		var count int
		require.NoError(t, db.Raw("SELECT count(*) FROM entries").Scan(&count).Error)
		require.Equal(t, 2, count)
		require.NoError(t, normalizeSQLiteSchemaCookie(db))
		snapshotPath, err := createCanonicalSQLiteSnapshot(db, dbPath)
		require.NoError(t, err)
		require.NoError(t, db.Close())
		t.Cleanup(func() { require.NoError(t, os.Remove(snapshotPath)) })
		snapshot, err := os.ReadFile(snapshotPath)
		require.NoError(t, err)
		return snapshot
	}

	direct := buildSnapshot(directPath)
	withHistory := buildSnapshot(historyPath)
	require.Equal(t, direct, withHistory)
	require.Equal(
		t,
		binary.BigEndian.Uint32(direct[24:28]),
		binary.BigEndian.Uint32(direct[92:96]),
	)
	// VACUUM INTO creates the destination schema and advances the normalized
	// source cookie once, so every canonical snapshot starts at cookie 2.
	require.Equal(t, uint32(2), binary.BigEndian.Uint32(direct[40:44]))
}

func TestProcessUnifiedDatabaseCanonicalizesFTSBatchHistory(t *testing.T) {
	poems := make([]loader.PoemWithMeta, 1105)
	for index := range poems {
		poems[index] = loader.PoemWithMeta{
			PoemData: loader.PoemData{
				ID:         fmt.Sprintf("fixture-%04d", index),
				Title:      fmt.Sprintf("测试诗题%04d", index),
				Author:     "测试作者",
				Paragraphs: []string{fmt.Sprintf("测试正文段落%04d", index)},
			},
			Dynasty:           "唐",
			DatasetName:       "fixture",
			DatasetKey:        "fixture",
			SourceID:          fmt.Sprintf("fixture-%04d", index),
			SourcePath:        "fixture.json",
			SourceRecordIndex: index,
		}
	}

	dir := t.TempDir()
	batch300 := filepath.Join(dir, "batch-300.db")
	batch1000 := filepath.Join(dir, "batch-1000.db")
	require.NoError(t, processUnifiedDatabaseWithBatchSize(batch300, poems, nil, 4, 300))
	require.NoError(t, processUnifiedDatabaseWithBatchSize(batch1000, poems, nil, 4, 1000))

	first, err := os.ReadFile(batch300)
	require.NoError(t, err)
	second, err := os.ReadFile(batch1000)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestSearchIndexVerificationRejectsProductDrift(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "search-drift.db")
	require.NoError(t, processUnifiedDatabaseWithBatchSize(dbPath, []loader.PoemWithMeta{{
		PoemData: loader.PoemData{
			ID:         "fixture",
			Title:      "原始诗题",
			Author:     "测试作者",
			Paragraphs: []string{"原始正文"},
		},
		Dynasty:           "唐",
		DatasetName:       "fixture",
		DatasetKey:        "fixture",
		SourceID:          "fixture",
		SourcePath:        "fixture.json",
		SourceRecordIndex: 0,
	}}, nil, 1, 1000))

	db, err := appdatabase.Open(dbPath, 1, 1)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	trigger := appdatabase.PoemsTable(appdatabase.LangHans) + "_fts_au"
	require.NoError(t, db.Exec("DROP TRIGGER "+trigger).Error)
	require.NoError(t, db.Exec(
		"UPDATE "+appdatabase.PoemsTable(appdatabase.LangHans)+" SET title = ?",
		"未同步诗题",
	).Error)
	// FTS5's own integrity-check only checks its shadow tables, not the base
	// products table. Our explicit relational verification must catch drift.
	ftsTable := appdatabase.PoemsFtsTable(appdatabase.LangHans)
	require.NoError(t, db.Exec(fmt.Sprintf(
		"INSERT INTO %[1]s(%[1]s) VALUES('integrity-check')",
		ftsTable,
	)).Error)
	require.ErrorContains(t, verifySearchIndexMatchesProducts(db.DB, appdatabase.LangHans), "differs")
}

func TestInstallOutputPairRollsBackWhenSecondInstallRenameFails(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "poetry.db")
	reportPath := databasePath + ".source-report.json"
	stagedDatabase := filepath.Join(dir, ".new.db")
	stagedReport := filepath.Join(dir, ".new-report.json")
	require.NoError(t, os.WriteFile(databasePath, []byte("old-database"), 0o600))
	require.NoError(t, os.WriteFile(reportPath, []byte("old-report"), 0o600))
	require.NoError(t, os.WriteFile(stagedDatabase, []byte("new-database"), 0o600))
	require.NoError(t, os.WriteFile(stagedReport, []byte("new-report"), 0o600))

	call := 0
	injected := errors.New("injected report rename failure")
	err := installOutputPairWithRename(stagedDatabase, databasePath, stagedReport, reportPath, func(old, new string) error {
		call++
		if call == 2 { // new DB, then new report; backups are hard links/copies
			return injected
		}
		return os.Rename(old, new)
	})
	require.ErrorIs(t, err, injected)
	database, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	report, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Equal(t, "old-database", string(database))
	require.Equal(t, "old-report", string(report))
	databaseBackup, reportBackup, marker := outputRecoveryPaths(databasePath, reportPath)
	for _, path := range []string{databaseBackup, reportBackup, marker} {
		_, err := os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestInstallOutputPairRetainsRecoverableStateWhenRollbackRenameFails(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "poetry.db")
	reportPath := databasePath + ".source-report.json"
	stagedDatabase := filepath.Join(dir, ".new.db")
	stagedReport := filepath.Join(dir, ".new-report.json")
	require.NoError(t, os.WriteFile(databasePath, []byte("old-database"), 0o600))
	require.NoError(t, os.WriteFile(reportPath, []byte("old-report"), 0o600))
	require.NoError(t, os.WriteFile(stagedDatabase, []byte("new-database"), 0o600))
	require.NoError(t, os.WriteFile(stagedReport, []byte("new-report"), 0o600))

	call := 0
	installFailure := errors.New("injected report install failure")
	restoreFailure := errors.New("injected database restore failure")
	err := installOutputPairWithRename(stagedDatabase, databasePath, stagedReport, reportPath, func(old, new string) error {
		call++
		switch call {
		case 2:
			return installFailure
		case 3:
			return restoreFailure
		default:
			return os.Rename(old, new)
		}
	})
	require.ErrorIs(t, err, installFailure)
	require.ErrorContains(t, err, "recovery state was retained")

	databaseBackup, reportBackup, marker := outputRecoveryPaths(databasePath, reportPath)
	for _, path := range []string{databaseBackup, reportBackup, marker} {
		require.FileExists(t, path)
	}
	require.NoError(t, recoverOutputPair(databasePath, reportPath))
	database, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	report, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Equal(t, "old-database", string(database))
	require.Equal(t, "old-report", string(report))
}

func TestRecoverOutputPairRestoresLastKnownGoodPair(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "poetry.db")
	reportPath := databasePath + ".source-report.json"
	databaseBackup, reportBackup, marker := outputRecoveryPaths(databasePath, reportPath)
	require.NoError(t, os.WriteFile(databasePath, []byte("partial-new-database"), 0o600))
	require.NoError(t, os.WriteFile(reportPath, []byte("partial-new-report"), 0o600))
	require.NoError(t, os.WriteFile(databaseBackup, []byte("old-database"), 0o600))
	require.NoError(t, os.WriteFile(reportBackup, []byte("old-report"), 0o600))
	require.NoError(t, os.WriteFile(marker, nil, 0o600))

	require.NoError(t, recoverOutputPair(databasePath, reportPath))
	database, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	report, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Equal(t, "old-database", string(database))
	require.Equal(t, "old-report", string(report))
}

func TestRecoverOutputPairCanRetryAfterPartialRestore(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "poetry.db")
	reportPath := databasePath + ".source-report.json"
	databaseBackup, reportBackup, marker := outputRecoveryPaths(databasePath, reportPath)
	require.NoError(t, os.WriteFile(databasePath, []byte("old-database"), 0o600))
	require.NoError(t, os.WriteFile(reportPath, []byte("partial-new-report"), 0o600))
	require.NoError(t, os.WriteFile(databaseBackup, []byte("old-database"), 0o600))
	require.NoError(t, os.WriteFile(reportBackup, []byte("old-report"), 0o600))
	require.NoError(t, os.WriteFile(marker, nil, 0o600))

	// This is the durable shape left if a process stops after restoring only
	// the first file: both unconsumed backups and the marker are still present.
	require.NoError(t, recoverOutputPair(databasePath, reportPath))
	database, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	report, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Equal(t, "old-database", string(database))
	require.Equal(t, "old-report", string(report))
	for _, path := range []string{databaseBackup, reportBackup, marker} {
		_, err := os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestRecoverOutputPairCoversPreparationAndFreshInstallCrashWindows(t *testing.T) {
	t.Run("preparation copies without marker leave live pair intact", func(t *testing.T) {
		dir := t.TempDir()
		databasePath := filepath.Join(dir, "poetry.db")
		reportPath := databasePath + ".source-report.json"
		databaseBackup, reportBackup, _ := outputRecoveryPaths(databasePath, reportPath)
		require.NoError(t, os.WriteFile(databasePath, []byte("old-database"), 0o600))
		require.NoError(t, os.WriteFile(reportPath, []byte("old-report"), 0o600))
		require.NoError(t, createRecoveryCopy(databasePath, databaseBackup))
		require.NoError(t, createRecoveryCopy(reportPath, reportBackup))

		require.NoError(t, recoverOutputPair(databasePath, reportPath))
		database, err := os.ReadFile(databasePath)
		require.NoError(t, err)
		require.Equal(t, "old-database", string(database))
		for _, path := range []string{databaseBackup, reportBackup} {
			_, err := os.Stat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	})

	t.Run("fresh install marker removes an uncommitted partial pair", func(t *testing.T) {
		dir := t.TempDir()
		databasePath := filepath.Join(dir, "poetry.db")
		reportPath := databasePath + ".source-report.json"
		_, _, marker := outputRecoveryPaths(databasePath, reportPath)
		require.NoError(t, os.WriteFile(marker, nil, 0o600))
		require.NoError(t, os.WriteFile(databasePath, []byte("partial"), 0o600))

		require.NoError(t, recoverOutputPair(databasePath, reportPath))
		for _, path := range []string{databasePath, reportPath, marker} {
			_, err := os.Stat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	})

	t.Run("legacy post-commit single backup preserves complete live pair", func(t *testing.T) {
		dir := t.TempDir()
		databasePath := filepath.Join(dir, "poetry.db")
		reportPath := databasePath + ".source-report.json"
		databaseBackup, reportBackup, marker := outputRecoveryPaths(databasePath, reportPath)
		require.NoError(t, os.WriteFile(databasePath, []byte("committed-database"), 0o600))
		require.NoError(t, os.WriteFile(reportPath, []byte("committed-report"), 0o600))
		require.NoError(t, os.WriteFile(databaseBackup, []byte("old-database"), 0o600))
		require.NoError(t, os.WriteFile(marker, nil, 0o600))

		require.NoError(t, recoverOutputPair(databasePath, reportPath))
		database, err := os.ReadFile(databasePath)
		require.NoError(t, err)
		report, err := os.ReadFile(reportPath)
		require.NoError(t, err)
		require.Equal(t, "committed-database", string(database))
		require.Equal(t, "committed-report", string(report))
		for _, path := range []string{databaseBackup, reportBackup, marker} {
			_, err := os.Stat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	})
}
