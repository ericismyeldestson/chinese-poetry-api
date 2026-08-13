package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/loader"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/logger"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/processor"
)

var (
	inputDir   string
	outputDB   string
	workers    int
	configPath string
)

func main() {
	// 数据处理程序始终以 debug 模式记录日志
	logger.Init(true)
	defer logger.Sync()

	rootCmd := &cobra.Command{
		Use:   "processor",
		Short: "Chinese Poetry Data Processor",
		Long:  "Process Chinese poetry JSON data and generate a unified SQLite database with both simplified and traditional Chinese versions",
		RunE:  run,
	}

	rootCmd.Flags().StringVarP(&inputDir, "input", "i", "poetry-data", "Input directory containing poetry JSON files")
	rootCmd.Flags().StringVarP(&outputDB, "output", "o", "poetry.db", "Output unified SQLite database")
	rootCmd.Flags().IntVarP(&workers, "workers", "w", 0, "Number of concurrent workers (0 = number of CPUs)")
	rootCmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to datas.json config file (default: <input>/loader/datas.json)")

	if err := rootCmd.Execute(); err != nil {
		logger.Fatal("Command execution failed", zap.Error(err))
	}
}

// run 是 processor 命令的主流程：加载数据、导入数据库、输出统计。
func run(cmd *cobra.Command, args []string) error {
	// 确定配置文件路径
	if configPath == "" {
		configPath = filepath.Join(inputDir, "loader", "datas.json")
	}

	logger.Info("Loading poetry data", zap.String("config", configPath))
	reportPath := outputDB + ".source-report.json"
	if err := recoverOutputPair(outputDB, reportPath); err != nil {
		return fmt.Errorf("failed to recover interrupted output installation: %w", err)
	}

	// 加载全部诗词数据
	jsonLoader, err := loader.NewJSONLoader(configPath)
	if err != nil {
		return fmt.Errorf("failed to create loader: %w", err)
	}

	poems, err := jsonLoader.LoadAll()
	if err != nil {
		return fmt.Errorf("failed to load poems: %w", err)
	}
	accepted, rejections, err := processor.PartitionSources(poems)
	if err != nil {
		return fmt.Errorf("failed to partition source records: %w", err)
	}
	report, err := jsonLoader.FinalizeReport(poems)
	if err != nil {
		return fmt.Errorf("failed to finalize source report: %w", err)
	}
	if report.Totals.TotalRecords != len(poems) ||
		report.Totals.AcceptedRecords != len(accepted) ||
		report.Totals.RejectedRecords != len(rejections) {
		return fmt.Errorf(
			"source accounting mismatch: report total/accepted/rejected=%d/%d/%d, records=%d/%d/%d",
			report.Totals.TotalRecords, report.Totals.AcceptedRecords, report.Totals.RejectedRecords,
			len(poems), len(accepted), len(rejections),
		)
	}

	logger.Info("Loaded source records",
		zap.Int("total", len(poems)),
		zap.Int("accepted", len(accepted)),
		zap.Int("rejected", len(rejections)),
	)

	// Build beside the destination and replace the live artifact only after every
	// database and provenance gate passes. A failed rebuild therefore cannot
	// destroy a previously verified database.
	outputDir := filepath.Dir(outputDB)
	tempDB, err := os.CreateTemp(outputDir, "."+filepath.Base(outputDB)+".build-*")
	if err != nil {
		return fmt.Errorf("failed to create staged database: %w", err)
	}
	tempDBPath := tempDB.Name()
	if err := tempDB.Close(); err != nil {
		return fmt.Errorf("failed to close staged database: %w", err)
	}
	defer func() {
		_ = os.Remove(tempDBPath)
		_ = os.Remove(tempDBPath + "-wal")
		_ = os.Remove(tempDBPath + "-shm")
	}()

	// 生成同时包含简繁两套表的统一数据库
	logger.Info("Processing unified database")
	if err := processUnifiedDatabase(tempDBPath, accepted, rejections, workers); err != nil {
		return fmt.Errorf("failed to process database: %w", err)
	}

	reportTempPath, err := stageJSONReport(reportPath, report)
	if err != nil {
		return fmt.Errorf("failed to stage source report: %w", err)
	}
	defer func() { _ = os.Remove(reportTempPath) }()

	if err := installOutputPair(tempDBPath, outputDB, reportTempPath, reportPath); err != nil {
		return fmt.Errorf("failed to install verified database/report pair: %w", err)
	}

	logger.Info("Processing complete",
		zap.String("database", outputDB),
		zap.String("source_report", reportPath),
	)

	// 输出统计信息
	if err := printStatistics(outputDB); err != nil {
		logger.Warn("Failed to print statistics", zap.Error(err))
	}

	return nil
}

type renameFunc func(string, string) error

func outputRecoveryPaths(databasePath, reportPath string) (databaseBackup, reportBackup, marker string) {
	return databasePath + ".lkg", reportPath + ".lkg", databasePath + ".installing"
}

// recoverOutputPair rolls back any interrupted two-file installation. Restoring
// the previous pair is deliberately conservative even if both new files appear
// present: without the removed commit marker they were never acknowledged as a
// complete provenance pair.
func recoverOutputPair(databasePath, reportPath string) error {
	databaseBackup, reportBackup, marker := outputRecoveryPaths(databasePath, reportPath)
	markerExists := fileExists(marker)
	databaseBackupExists := fileExists(databaseBackup)
	reportBackupExists := fileExists(reportBackup)
	if !markerExists {
		// Backups are created while the old pair is still live, before the commit
		// marker. A crash in that preparation window can therefore leave only
		// harmless stale copies; remove them only when the live pair is complete.
		if databaseBackupExists || reportBackupExists {
			if !fileExists(databasePath) || !fileExists(reportPath) {
				return fmt.Errorf("stale output backups exist without a complete live pair")
			}
			_ = os.Remove(databaseBackup)
			_ = os.Remove(reportBackup)
		}
		return nil
	}
	if _, err := os.Stat(marker); err != nil {
		return err
	}

	if databaseBackupExists != reportBackupExists {
		// Older builds removed the marker and both backups without an fsync
		// boundary between those phases. A crash could therefore leave a durable
		// marker plus only one backup even though both newly installed live files
		// were already synced. Preserve that complete live pair and finish the
		// interrupted commit cleanup instead of making recovery impossible.
		if !fileExists(databasePath) || !fileExists(reportPath) {
			return fmt.Errorf("incomplete last-known-good output backup")
		}
		if err := syncOutputDirectory(databasePath); err != nil {
			return err
		}
		if err := os.Remove(marker); err != nil {
			return err
		}
		if err := syncOutputDirectory(databasePath); err != nil {
			return err
		}
		_ = os.Remove(databaseBackup)
		_ = os.Remove(reportBackup)
		return syncOutputDirectory(databasePath)
	}
	if databaseBackupExists {
		if err := restoreRecoveryCopy(databaseBackup, databasePath, os.Rename); err != nil {
			return err
		}
		if err := restoreRecoveryCopy(reportBackup, reportPath, os.Rename); err != nil {
			return err
		}
		if !fileExists(databasePath) || !fileExists(reportPath) {
			return fmt.Errorf("restored output pair failed final existence check")
		}
	} else {
		if err := os.Remove(databasePath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(reportPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := syncOutputDirectory(databasePath); err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	if err := syncOutputDirectory(databasePath); err != nil {
		return err
	}
	_ = os.Remove(databaseBackup)
	_ = os.Remove(reportBackup)
	return syncOutputDirectory(databasePath)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func syncOutputDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func createRecoveryCopy(source, destination string) error {
	if err := os.Link(source, destination); err == nil {
		return nil
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()
	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	destinationFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = destinationFile.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		return err
	}
	if err := destinationFile.Sync(); err != nil {
		return err
	}
	if err := destinationFile.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

// restoreRecoveryCopy installs a copy of a backup without consuming the
// backup itself. If the final rename fails, both LKG files and the marker stay
// available for the next process to recover instead of silently losing the
// only complete previous pair.
func restoreRecoveryCopy(source, destination string, rename renameFunc) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()
	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".restore-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	installed := false
	defer func() {
		_ = temp.Close()
		if !installed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(temp, sourceFile); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := rename(tempPath, destination); err != nil {
		return err
	}
	installed = true
	return nil
}

func installOutputPair(stagedDatabase, databasePath, stagedReport, reportPath string) error {
	return installOutputPairWithRename(stagedDatabase, databasePath, stagedReport, reportPath, os.Rename)
}

// installOutputPairWithRename is intentionally one explicit state machine: its
// branches correspond to durable preparation, installation, rollback, and
// cleanup boundaries that must remain reviewable together.
//
//nolint:gocyclo // Splitting this atomic state machine would obscure its crash-order invariants.
func installOutputPairWithRename(stagedDatabase, databasePath, stagedReport, reportPath string, rename renameFunc) error {
	if !fileExists(stagedDatabase) || !fileExists(stagedReport) {
		return fmt.Errorf("both staged database and source report must be regular files")
	}
	outputDir := filepath.Clean(filepath.Dir(databasePath))
	for _, path := range []string{stagedDatabase, stagedReport, reportPath} {
		if filepath.Clean(filepath.Dir(path)) != outputDir {
			return fmt.Errorf("staged and live database/report files must share one directory")
		}
	}
	databaseBackup, reportBackup, marker := outputRecoveryPaths(databasePath, reportPath)
	if fileExists(marker) || fileExists(databaseBackup) || fileExists(reportBackup) {
		return fmt.Errorf("stale output recovery state exists")
	}
	databaseExists, reportExists := fileExists(databasePath), fileExists(reportPath)
	if databaseExists != reportExists {
		return fmt.Errorf("existing database/report pair is incomplete")
	}

	// Preserve the old pair without moving it. Only after both recovery copies
	// exist do we create the marker that authorizes destructive replacement.
	if databaseExists {
		if err := createRecoveryCopy(databasePath, databaseBackup); err != nil {
			return err
		}
		if err := createRecoveryCopy(reportPath, reportBackup); err != nil {
			_ = os.Remove(databaseBackup)
			return err
		}
		if err := syncOutputDirectory(databasePath); err != nil {
			_ = os.Remove(databaseBackup)
			_ = os.Remove(reportBackup)
			return err
		}
	}
	markerFile, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.Remove(databaseBackup)
		_ = os.Remove(reportBackup)
		return err
	}
	if err := markerFile.Sync(); err != nil {
		_ = markerFile.Close()
		_ = os.Remove(marker)
		_ = os.Remove(databaseBackup)
		_ = os.Remove(reportBackup)
		return err
	}
	if err := markerFile.Close(); err != nil {
		_ = os.Remove(marker)
		_ = os.Remove(databaseBackup)
		_ = os.Remove(reportBackup)
		return err
	}
	if err := syncOutputDirectory(databasePath); err != nil {
		_ = os.Remove(marker)
		_ = os.Remove(databaseBackup)
		_ = os.Remove(reportBackup)
		return err
	}

	rollback := func() error {
		if err := os.Remove(databasePath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(reportPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if databaseExists {
			if err := restoreRecoveryCopy(databaseBackup, databasePath, rename); err != nil {
				return err
			}
			if err := restoreRecoveryCopy(reportBackup, reportPath, rename); err != nil {
				return err
			}
		}
		if err := syncOutputDirectory(databasePath); err != nil {
			return err
		}
		// The complete old pair is now durable. Removing the marker acknowledges
		// rollback; stale backup cleanup is harmless and recoverOutputPair can
		// finish it if this process stops between the following phases.
		if err := os.Remove(marker); err != nil {
			return err
		}
		if err := syncOutputDirectory(databasePath); err != nil {
			return err
		}
		_ = os.Remove(databaseBackup)
		_ = os.Remove(reportBackup)
		return syncOutputDirectory(databasePath)
	}
	if err := rename(stagedDatabase, databasePath); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("%w (rollback failed and recovery state was retained: %v)", err, rollbackErr)
		}
		return err
	}
	if err := rename(stagedReport, reportPath); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("%w (rollback failed and recovery state was retained: %v)", err, rollbackErr)
		}
		return err
	}
	if err := syncOutputDirectory(databasePath); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("%w (rollback failed and recovery state was retained: %v)", err, rollbackErr)
		}
		return err
	}
	if !fileExists(databasePath) || !fileExists(reportPath) {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("installed output pair failed final existence check; rollback failed and recovery state was retained: %w", rollbackErr)
		}
		return fmt.Errorf("installed output pair failed final existence check")
	}
	if err := os.Remove(marker); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("%w (rollback failed and recovery state was retained: %v)", err, rollbackErr)
		}
		return err
	}
	if err := syncOutputDirectory(databasePath); err != nil {
		return err
	}
	_ = os.Remove(databaseBackup)
	_ = os.Remove(reportBackup)
	return syncOutputDirectory(databasePath)
}

func stageJSONReport(destination string, report loader.LoadReport) (string, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')

	file, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".build-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

// processUnifiedDatabase 重建数据库，并依次导入简体与繁体两套数据。
func processUnifiedDatabase(dbPath string, poems []loader.PoemWithMeta, rejections []database.SourceRejection, workers int) error {
	// 删除已存在的数据库文件
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing database: %w", err)
	}

	// 数据处理场景下单连接更安全
	db, err := database.Open(dbPath, 1, 1)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	// 执行迁移，创建简繁两套表
	logger.Info("Creating database schema (simplified + traditional tables)")
	if err := db.Migrate(); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
	if err := db.InsertSourceRejections(rejections); err != nil {
		return fmt.Errorf("failed to persist source rejection ledger: %w", err)
	}

	// 处理简体版本
	logger.Info("Processing language variant", zap.String("lang", "zh-Hans"))
	repoSimp := database.NewRepositoryWithLang(db, database.LangHans)
	procSimp := processor.NewProcessor(repoSimp, workers, false)
	if err := procSimp.Process(poems); err != nil {
		return fmt.Errorf("failed to process simplified poems: %w", err)
	}

	// 处理繁体版本
	logger.Info("Processing language variant", zap.String("lang", "zh-Hant"))
	repoTrad := database.NewRepositoryWithLang(db, database.LangHant)
	procTrad := processor.NewProcessor(repoTrad, workers, true)
	if err := procTrad.Process(poems); err != nil {
		return fmt.Errorf("failed to process traditional poems: %w", err)
	}

	var rejectionCount int64
	if err := db.Table("source_rejections").Count(&rejectionCount).Error; err != nil {
		return fmt.Errorf("failed to count source rejections: %w", err)
	}
	if rejectionCount != int64(len(rejections)) {
		return fmt.Errorf("source rejection ledger has %d rows, expected %d", rejectionCount, len(rejections))
	}
	for _, lang := range []database.Lang{database.LangHans, database.LangHant} {
		var sourceCount int64
		if err := db.Table(database.PoemSourcesTable(lang)).Count(&sourceCount).Error; err != nil {
			return fmt.Errorf("failed to count %s source witnesses: %w", lang, err)
		}
		if sourceCount != int64(len(poems)) {
			return fmt.Errorf("%s source witness table has %d rows, expected %d", lang, sourceCount, len(poems))
		}
	}

	// Generation time belongs in the external release manifest, not in logical
	// corpus rows. Normalize audit-neutral timestamps so identical source and
	// pipeline inputs can produce byte-identical SQLite assets in one toolchain.
	const deterministicTimestamp = "1970-01-01T00:00:00Z"
	timestampTables := []string{"source_rejections"}
	for _, lang := range []database.Lang{database.LangHans, database.LangHant} {
		timestampTables = append(timestampTables,
			database.DynastiesTable(lang),
			database.AuthorsTable(lang),
			database.PoetryTypesTable(lang),
			database.PoemsTable(lang),
			database.PoemSourcesTable(lang),
		)
	}
	for _, table := range timestampTables {
		if err := db.Exec(fmt.Sprintf("UPDATE %s SET created_at = ?", table), deterministicTimestamp).Error; err != nil {
			return fmt.Errorf("failed to normalize generated timestamps in %s: %w", table, err)
		}
	}
	if err := db.Exec("UPDATE metadata SET updated_at = ?", deterministicTimestamp).Error; err != nil {
		return fmt.Errorf("failed to normalize metadata timestamp: %w", err)
	}

	// 优化数据库文件
	logger.Info("Optimizing database")
	if err := db.Exec("VACUUM").Error; err != nil {
		return fmt.Errorf("failed to vacuum generated database: %w", err)
	}

	if err := db.Exec("ANALYZE").Error; err != nil {
		return fmt.Errorf("failed to analyze generated database: %w", err)
	}

	// database.Open uses WAL for server concurrency. Release assets, however,
	// must be self-contained single files: checkpoint every page and return to
	// DELETE journaling before the connection closes and the staged file moves.
	// wal_checkpoint returns one result row. Consume it explicitly; executing
	// the row-producing pragma through Exec can leave the sqlite statement open
	// long enough for the following journal_mode change to be rejected as being
	// inside a transaction.
	var checkpoint struct {
		Busy       int `gorm:"column:busy"`
		Log        int `gorm:"column:log"`
		Checkpoint int `gorm:"column:checkpointed"`
	}
	if err := db.Raw("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&checkpoint).Error; err != nil {
		return fmt.Errorf("failed to checkpoint generated database: %w", err)
	}
	if checkpoint.Busy != 0 || checkpoint.Log != checkpoint.Checkpoint {
		return fmt.Errorf(
			"generated database checkpoint incomplete: busy=%d log=%d checkpointed=%d",
			checkpoint.Busy, checkpoint.Log, checkpoint.Checkpoint,
		)
	}
	var journalMode string
	if err := db.Raw("PRAGMA journal_mode=DELETE").Scan(&journalMode).Error; err != nil {
		return fmt.Errorf("failed to make generated database portable: %w", err)
	}
	if journalMode != "delete" {
		return fmt.Errorf("generated database journal mode is %q, expected delete", journalMode)
	}

	return nil
}

// printStatistics 打印各语言变体下的数据量统计。
func printStatistics(dbPath string) error {
	// 统计为只读操作，单连接即可
	db, err := database.OpenReadOnly(dbPath, 1, 1)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	fmt.Println("\n=== Database Statistics ===")
	fmt.Println("+-----------------------+----------+----------+----------+-------------+")
	fmt.Println("| Language              | Poems    | Authors  | Dynasties| Poetry Types|")
	fmt.Println("+-----------------------+----------+----------+----------+-------------+")

	for _, lang := range []database.Lang{database.LangHans, database.LangHant} {
		var poemCount, authorCount, dynastyCount, typeCount int64

		for table, destination := range map[string]*int64{
			database.PoemsTable(lang):       &poemCount,
			database.AuthorsTable(lang):     &authorCount,
			database.DynastiesTable(lang):   &dynastyCount,
			database.PoetryTypesTable(lang): &typeCount,
		} {
			if err := db.Table(table).Count(destination).Error; err != nil {
				return fmt.Errorf("failed to count %s: %w", table, err)
			}
		}

		langName := "Simplified (zh-Hans)"
		if lang == database.LangHant {
			langName = "Traditional (zh-Hant)"
		}

		fmt.Printf("| %-21s | %8d | %8d | %8d | %11d |\n",
			langName, poemCount, authorCount, dynastyCount, typeCount)
	}

	fmt.Println("+-----------------------+----------+----------+----------+-------------+")

	return nil
}
