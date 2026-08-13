package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
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
