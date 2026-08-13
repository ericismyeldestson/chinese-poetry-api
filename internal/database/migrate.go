package database

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/classifier"
)

// DB 是对 gorm.DB 连接的封装。
type DB struct {
	*gorm.DB
}

// Open 打开 SQLite 数据库连接。
// maxOpenConns：最大连接数，传 0 则取保守默认值 1。
// maxIdleConns：最大空闲连接数，传 0 则取默认值 1。
func Open(path string, maxOpenConns, maxIdleConns int) (*DB, error) {
	dsn, err := sqliteFileDSN(path, false)
	if err != nil {
		return nil, err
	}
	return openDSN(dsn, maxOpenConns, maxIdleConns)
}

// OpenReadOnly opens an immutable SQLite snapshot for the API server and
// release inspection. The public API has no write operations, so using a
// read-only connection prevents runtime journal-mode changes from mutating a
// checksum-verified release database or creating WAL/SHM sidecars.
func OpenReadOnly(path string, maxOpenConns, maxIdleConns int) (*DB, error) {
	dsn, err := sqliteFileDSN(path, true)
	if err != nil {
		return nil, err
	}
	return openDSN(dsn, maxOpenConns, maxIdleConns)
}

func sqliteFileDSN(path string, readOnly bool) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve database path: %w", err)
	}
	uri := &url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	query := url.Values{
		"_busy_timeout": {"5000"},
		"_cache_size":   {"-64000"},
		"_foreign_keys": {"on"},
		"_temp_store":   {"MEMORY"},
		"cache":         {"shared"},
	}
	if readOnly {
		query.Set("mode", "ro")
		query.Set("immutable", "1")
	} else {
		query.Set("mode", "rwc")
		query.Set("_journal_mode", "WAL")
		query.Set("_synchronous", "NORMAL")
	}
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}

func openDSN(dsn string, maxOpenConns, maxIdleConns int) (*DB, error) {
	config := &gorm.Config{
		Logger:      logger.Default.LogMode(logger.Silent), // 调试时可改为 logger.Info
		NowFunc:     time.Now,
		PrepareStmt: true, // 预编译语句以提升性能
	}

	db, err := gorm.Open(sqlite.Open(dsn), config)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// 取出底层 sql.DB 以配置连接池
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	// 默认只用 1 条连接，对数据导入这类写密集场景更安全；
	// 以读为主的 API 服务可以调大。
	if maxOpenConns <= 0 {
		maxOpenConns = 1
	}
	if maxIdleConns <= 0 {
		maxIdleConns = 1
	}

	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	// 探活，确认连接可用
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{db}, nil
}

// NewDBFromGorm 包装一个已有的 gorm.DB 连接，便于测试时注入自定义配置。
func NewDBFromGorm(db *gorm.DB) *DB {
	return &DB{db}
}

// Migrate 为简体、繁体两套表创建全部表结构、索引与初始数据。
func (db *DB) Migrate() error {
	// 先建元数据表
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		return fmt.Errorf("failed to create metadata table: %w", err)
	}

	// Every source record rejected before product generation is retained in a
	// language-independent ledger. This makes quality losses explicit without
	// duplicating the same rejection in the Hans and Hant product tables.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS source_rejections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		locator_id TEXT NOT NULL UNIQUE,
		source_id TEXT,
		dataset_key TEXT NOT NULL,
		source_path TEXT NOT NULL,
		source_record_index INTEGER NOT NULL CHECK(source_record_index >= 0),
		stage TEXT NOT NULL,
		reason TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		return fmt.Errorf("failed to create source_rejections: %w", err)
	}
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS idx_source_rejections_dataset ON source_rejections(dataset_key)",
		"CREATE INDEX IF NOT EXISTS idx_source_rejections_reason ON source_rejections(stage, reason)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("failed to migrate source_rejections indexes: %w", err)
		}
	}

	// 简繁两套表分别建表
	for _, lang := range []Lang{LangHans, LangHant} {
		if err := db.migrateTablesForLang(lang); err != nil {
			return fmt.Errorf("failed to migrate tables for %s: %w", lang, err)
		}

		// 写入该语言变体的初始数据
		if err := db.insertInitialDataForLang(lang); err != nil {
			return fmt.Errorf("failed to insert initial data for %s: %w", lang, err)
		}
	}

	// 更新 schema 版本号
	if err := db.Exec(
		`INSERT OR REPLACE INTO metadata (key, value, updated_at) VALUES (?, ?, ?)`,
		"schema_version",
		fmt.Sprintf("%d", SchemaVersion),
		time.Now(),
	).Error; err != nil {
		return fmt.Errorf("failed to update schema version: %w", err)
	}

	return nil
}

// migrateTablesForLang 为指定语言变体创建全部表与索引。
func (db *DB) migrateTablesForLang(lang Lang) error {
	dynastyTable := dynastiesTable(lang)
	authorTable := authorsTable(lang)
	poetryTypeTable := poetryTypesTable(lang)
	poemTable := poemsTable(lang)
	poemSourceTable := poemSourcesTable(lang)

	// 朝代表
	dynastySQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		name_en TEXT,
		start_year INTEGER,
		end_year INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`, dynastyTable)
	if err := db.Exec(dynastySQL).Error; err != nil {
		return fmt.Errorf("failed to create %s: %w", dynastyTable, err)
	}

	// 作者表
	var existingAuthorSQL string
	if err := db.Raw(
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' AND name = ?`,
		authorTable,
	).Scan(&existingAuthorSQL).Error; err != nil {
		return fmt.Errorf("failed to inspect %s: %w", authorTable, err)
	}
	if existingAuthorSQL != "" && !strings.Contains(existingAuthorSQL, "canonical_id TEXT NOT NULL UNIQUE") {
		return fmt.Errorf(
			"%s lacks governed canonical author identity; rebuild the database for schema v%d",
			authorTable, SchemaVersion,
		)
	}
	authorSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY,
		canonical_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		dynasty_id INTEGER NOT NULL,
		description TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (dynasty_id) REFERENCES %s(id)
	)`, authorTable, dynastyTable)
	if err := db.Exec(authorSQL).Error; err != nil {
		return fmt.Errorf("failed to create %s: %w", authorTable, err)
	}
	// dynasty_id 索引
	if err := db.Exec(fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS idx_%s_dynasty ON %s(dynasty_id)", authorTable, authorTable,
	)).Error; err != nil {
		return fmt.Errorf("failed to create dynasty index for %s: %w", authorTable, err)
	}
	if err := db.Exec(fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS idx_%s_name ON %s(name)", authorTable, authorTable,
	)).Error; err != nil {
		return fmt.Errorf("failed to create name index for %s: %w", authorTable, err)
	}
	if err := db.Exec(fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS idx_%s_name_dynasty ON %s(name, dynasty_id)", authorTable, authorTable,
	)).Error; err != nil {
		return fmt.Errorf("failed to create composite lookup index for %s: %w", authorTable, err)
	}

	// 诗词体裁表
	poetryTypeSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		category TEXT NOT NULL,
		lines INTEGER,
		chars_per_line INTEGER,
		description TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`, poetryTypeTable)
	if err := db.Exec(poetryTypeSQL).Error; err != nil {
		return fmt.Errorf("failed to create %s: %w", poetryTypeTable, err)
	}

	// 诗词表
	poemSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY,
		type_id INTEGER,
		title TEXT NOT NULL,
		content TEXT NOT NULL,
		content_hash TEXT,
		canonical_id TEXT,
		canonical_fingerprint TEXT,
		author_id INTEGER,
		dynasty_id INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (type_id) REFERENCES %s(id),
		FOREIGN KEY (author_id) REFERENCES %s(id),
		FOREIGN KEY (dynasty_id) REFERENCES %s(id)
	)`, poemTable, poetryTypeTable, authorTable, dynastyTable)
	if err := db.Exec(poemSQL).Error; err != nil {
		return fmt.Errorf("failed to create %s: %w", poemTable, err)
	}
	// v2 introduces stable, language-independent poem identities. Existing v1
	// databases receive nullable columns so legacy rows remain readable until the
	// next data rebuild assigns canonical identities.
	if err := db.addColumnIfMissing(poemTable, "canonical_id", "TEXT"); err != nil {
		return err
	}
	if err := db.addColumnIfMissing(poemTable, "canonical_fingerprint", "TEXT"); err != nil {
		return err
	}

	// 诗词表索引
	poemIndexStatements := []string{
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_type ON %s(type_id)", poemTable, poemTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_title ON %s(title)", poemTable, poemTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_author ON %s(author_id)", poemTable, poemTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_dynasty ON %s(dynasty_id)", poemTable, poemTable),
		// v1 used (title, content_hash) as identity. It silently collapsed records
		// from different authors or source files, so v2 removes that constraint.
		fmt.Sprintf("DROP INDEX IF EXISTS idx_%s_unique", poemTable),
		// SQLite permits multiple NULL values in a UNIQUE index. That lets migrated
		// legacy rows coexist while all newly generated rows use stable IDs.
		fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS idx_%s_canonical_id ON %s(canonical_id)", poemTable, poemTable),
		// 复合索引，用于多体裁随机取词（type_id IN ... 叠加 id 范围查找）
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_type_id ON %s(type_id, id)", poemTable, poemTable),
	}
	for _, statement := range poemIndexStatements {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("failed to migrate indexes for %s: %w", poemTable, err)
		}
	}

	// Keep row-level source provenance separate from the API-facing poem row. A
	// poem may legitimately occur in more than one upstream file; locator_id is
	// the immutable identity of that source occurrence.
	poemSourceSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		poem_id INTEGER NOT NULL,
		locator_id TEXT NOT NULL UNIQUE,
		source_id TEXT,
		dataset_key TEXT NOT NULL,
		source_path TEXT NOT NULL,
		source_record_index INTEGER NOT NULL CHECK(source_record_index >= 0),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (poem_id) REFERENCES %s(id) ON DELETE CASCADE
	)`, poemSourceTable, poemTable)
	if err := db.Exec(poemSourceSQL).Error; err != nil {
		return fmt.Errorf("failed to create %s: %w", poemSourceTable, err)
	}
	for _, statement := range []string{
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_poem ON %s(poem_id)", poemSourceTable, poemSourceTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_dataset ON %s(dataset_key)", poemSourceTable, poemSourceTable),
	} {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("failed to migrate indexes for %s: %w", poemSourceTable, err)
		}
	}

	if err := db.migrateFtsForLang(lang); err != nil {
		return err
	}

	return nil
}

// addColumnIfMissing performs an idempotent SQLite column migration. The table
// and column names passed here come exclusively from the Lang mapping and fixed
// migration constants, not from user input.
func (db *DB) addColumnIfMissing(table, column, definition string) error {
	var columns []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw(fmt.Sprintf("PRAGMA table_info(%s)", table)).Scan(&columns).Error; err != nil {
		return fmt.Errorf("failed to inspect %s: %w", table, err)
	}
	for _, existing := range columns {
		if existing.Name == column {
			return nil
		}
	}
	if err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)).Error; err != nil {
		return fmt.Errorf("failed to add %s.%s: %w", table, column, err)
	}
	return nil
}

// migrateFtsForLang 创建支撑全文检索的 FTS5 虚拟表及其同步触发器，
// 若表是本次新建的，还会做一次数据回填。
//
// 这里用的是自包含（而非 external-content）的 FTS5 索引：content_text 是派生值，
// 由以 JSON 数组形式存放的 poems.content 拍平而来，并非 poems 表上真实存在的列，
// 而 FTS5 的 external-content 模式要求能从关联表的同名列读回取值。
// 复制一份被索引的文本会多占些磁盘空间，但换来索引实现的简单与正确。
// 下面的触发器负责在 poems 表每次 INSERT/UPDATE/DELETE（含 loader 使用的
// ON CONFLICT upsert）时同步索引。
//
// 分词器选用 trigram 而非默认的 unicode61：中文没有空格作词边界，
// 标准分词器无法有效切分。trigram 索引让 `col LIKE '%...%'` 这类任意子串查询
// 也能让至少 3 个 Unicode 字符的 LIKE 模式使用索引。单字/双字模式不能利用
// trigram 索引，因此 API 层会拒绝对应的 title/content/all 搜索。
func (db *DB) migrateFtsForLang(lang Lang) error {
	poemTable := poemsTable(lang)
	ftsTable := poemsFtsTable(lang)

	// 判断该表是首次创建（需要一次性回填），还是已经存在（已由触发器增量同步，每次 Migrate 都重建纯属浪费）
	var existingCount int64
	if err := db.Raw(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, ftsTable,
	).Scan(&existingCount).Error; err != nil {
		return fmt.Errorf("failed to check for existing %s: %w", ftsTable, err)
	}

	ftsSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(
		title,
		content_text,
		tokenize='trigram'
	)`, ftsTable)
	if err := db.Exec(ftsSQL).Error; err != nil {
		return fmt.Errorf(
			"failed to create %s (requires SQLite built with FTS5 support, e.g. the sqlite_fts5 build tag): %w",
			ftsTable, err,
		)
	}

	// content_text 是从 poems.content 这个 JSON 数组中取出各段落拼接而成的纯文本，
	// 这样索引（以及针对它的 LIKE 查询）匹配的是可读正文，而非 JSON 里的原始标点。
	const contentTextExpr = `(SELECT COALESCE(group_concat(value, ''), '') FROM json_each(%s.content))`

	insertTrigger := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %[2]s_fts_ai AFTER INSERT ON %[2]s BEGIN
		INSERT INTO %[1]s(rowid, title, content_text)
		VALUES (new.id, new.title, `+fmt.Sprintf(contentTextExpr, "new")+`);
	END`, ftsTable, poemTable)
	if err := db.Exec(insertTrigger).Error; err != nil {
		return fmt.Errorf("failed to create insert trigger for %s: %w", ftsTable, err)
	}

	// 注意：FTS5 中 "INSERT INTO fts(fts, rowid, ...) VALUES ('delete', ...)" 这种
	// 特殊删除语法只适用于 external-content 表；本表是自包含的（见上文说明），
	// 因此直接按 rowid 执行普通 DELETE 即可。
	deleteTrigger := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %[2]s_fts_ad AFTER DELETE ON %[2]s BEGIN
		DELETE FROM %[1]s WHERE rowid = old.id;
	END`, ftsTable, poemTable)
	if err := db.Exec(deleteTrigger).Error; err != nil {
		return fmt.Errorf("failed to create delete trigger for %s: %w", ftsTable, err)
	}

	updateTrigger := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %[2]s_fts_au AFTER UPDATE ON %[2]s BEGIN
		DELETE FROM %[1]s WHERE rowid = old.id;
		INSERT INTO %[1]s(rowid, title, content_text)
		VALUES (new.id, new.title, `+fmt.Sprintf(contentTextExpr, "new")+`);
	END`, ftsTable, poemTable)
	if err := db.Exec(updateTrigger).Error; err != nil {
		return fmt.Errorf("failed to create update trigger for %s: %w", ftsTable, err)
	}

	// 仅在首次创建时回填：已有数据早于触发器存在，需要补进索引；
	// 而此前已存在的表本身就是最新的，没必要在每次迁移时都做一遍全量重建。
	//
	// FTS5 内置的 'rebuild' 命令要求 fts5 表的列名与内容表的真实列一一对应，
	// 而 content_text 是派生表达式而非存储列，因此这里用触发器里相同的表达式手动回填。
	if existingCount == 0 {
		backfillSQL := fmt.Sprintf(`INSERT INTO %[1]s(rowid, title, content_text)
			SELECT id, title, `+fmt.Sprintf(contentTextExpr, poemTable)+`
			FROM %[2]s`, ftsTable, poemTable)
		if err := db.Exec(backfillSQL).Error; err != nil {
			return fmt.Errorf("failed to backfill %s: %w", ftsTable, err)
		}
	}

	return nil
}

// insertInitialDataForLang 为指定语言变体写入朝代、体裁等初始数据。
func (db *DB) insertInitialDataForLang(lang Lang) error {
	dynastyTable := dynastiesTable(lang)
	poetryTypeTable := poetryTypesTable(lang)

	// 把 SQL 中的表名替换为该变体的表名，繁体库还需做简转繁
	dynastiesSQL := strings.ReplaceAll(InitialDynastiesSQL, "dynasties", dynastyTable)
	poetryTypesSQL := strings.ReplaceAll(InitialPoetryTypesSQL, "poetry_types", poetryTypeTable)

	if lang == LangHant {
		var err error
		dynastiesSQL, err = convertSQLToTraditional(dynastiesSQL)
		if err != nil {
			return fmt.Errorf("failed to convert dynasties SQL: %w", err)
		}
		poetryTypesSQL, err = convertSQLToTraditional(poetryTypesSQL)
		if err != nil {
			return fmt.Errorf("failed to convert poetry types SQL: %w", err)
		}
	}

	// 写入朝代数据
	if err := db.Exec(dynastiesSQL).Error; err != nil {
		return fmt.Errorf("failed to insert dynasties: %w", err)
	}

	// 写入体裁数据
	if err := db.Exec(poetryTypesSQL).Error; err != nil {
		return fmt.Errorf("failed to insert poetry types: %w", err)
	}

	return nil
}

// convertSQLToTraditional 把 SQL 语句中的中文转为繁体，
// 只转换单引号内的字符串字面量，保证 SQL 语法本身不受影响。
func convertSQLToTraditional(sql string) (string, error) {
	// 以单引号切分，奇数下标的片段即字符串字面量内部
	parts := strings.Split(sql, "'")

	for i := range parts {
		if i%2 == 1 {
			converted, err := classifier.ToTraditional(parts[i])
			if err != nil {
				return "", err
			}
			parts[i] = converted
		}
	}

	return strings.Join(parts, "'"), nil
}

// GetSchemaVersion 返回当前的 schema 版本号，未记录时返回 0。
func (db *DB) GetSchemaVersion() (int, error) {
	var version int
	err := db.Raw(`SELECT value FROM metadata WHERE key = ?`, "schema_version").Scan(&version).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return version, nil
}

// Close 关闭数据库连接。
func (db *DB) Close() error {
	sqlDB, err := db.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
