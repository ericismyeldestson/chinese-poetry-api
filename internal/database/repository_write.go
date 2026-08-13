package database

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/logger"
)

// 本文件包含数据导入阶段使用的写操作。

// GetOrCreateDynasty 按名称查询或创建朝代，可安全并发调用。
// 借助 ON CONFLICT 来化解并发插入冲突。
func (r *Repository) GetOrCreateDynasty(name string) (int64, error) {
	dynasty := Dynasty{Name: name}

	// 以 ON CONFLICT DO NOTHING 的方式尝试插入
	err := r.db.Table(r.dynastiesTable()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoNothing: true, // 已存在则忽略
	}).Create(&dynasty).Error
	if err != nil {
		return 0, err
	}

	// ID 为 0 说明插入被跳过（记录已存在），需要回查已有记录
	if dynasty.ID == 0 {
		err = r.db.Table(r.dynastiesTable()).Where("name = ?", name).First(&dynasty).Error
		if err != nil {
			return 0, err
		}
	}

	return dynasty.ID, nil
}

// GetOrCreateAuthor 查询或创建作者，可安全并发调用。
// 作者身份以 (name, dynasty_id) 复合键表示。只以名字合并会把
// 不同朝代的同名人错当成一人。
func (r *Repository) GetOrCreateAuthor(name string, dynastyID int64) (int64, error) {
	// Compatibility entry point for tests and callers that already operate on a
	// localized database. The processor uses GetOrCreateCanonicalAuthor so IDs
	// remain aligned across Hans/Hant.
	dynasty, err := r.GetDynastyByID(dynastyID)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve author dynasty: %w", err)
	}
	canonicalID, err := NewCanonicalAuthorID(dynasty.Name, name)
	if err != nil {
		return 0, err
	}
	return r.GetOrCreateCanonicalAuthor(canonicalID, name, dynastyID)
}

// GetOrCreateCanonicalAuthor persists a localized display row with the shared
// canonical identity and public ID derived by the processor from fixed Hans.
func (r *Repository) GetOrCreateCanonicalAuthor(canonicalID, name string, dynastyID int64) (int64, error) {
	if name == "" {
		return 0, fmt.Errorf("author name must not be empty")
	}
	if dynastyID <= 0 {
		return 0, fmt.Errorf("author dynasty_id must be positive")
	}
	authorID, err := AuthorIDFromCanonical(canonicalID)
	if err != nil {
		return 0, err
	}
	author := Author{
		ID:          authorID,
		CanonicalID: canonicalID,
		Name:        name,
		DynastyID:   &dynastyID,
	}

	// 以 ON CONFLICT DO NOTHING 的方式尝试插入
	err = r.db.Table(r.authorsTable()).Clauses(clause.OnConflict{
		DoNothing: true,
	}).Create(&author).Error
	if err != nil {
		return 0, err
	}

	// ID 为 0 说明插入被跳过（记录已存在），需要回查已有记录
	var stored Author
	err = r.db.Table(r.authorsTable()).Where("canonical_id = ?", canonicalID).First(&stored).Error
	if err != nil {
		return 0, fmt.Errorf("canonical author %q was not persisted (possible numeric ID conflict): %w", canonicalID, err)
	}
	if stored.ID != authorID || stored.Name != name || stored.DynastyID == nil || *stored.DynastyID != dynastyID {
		return 0, fmt.Errorf("canonical author conflict for %q", canonicalID)
	}
	return stored.ID, nil
}

// GetPoetryTypeID 按名称查询体裁 ID。
func (r *Repository) GetPoetryTypeID(name string) (int64, error) {
	var poetryType PoetryType
	err := r.db.Table(r.poetryTypesTable()).Where("name = ?", name).First(&poetryType).Error
	if err != nil {
		return 0, err
	}
	return poetryType.ID, nil
}

// GetPoetryTypeIDs 用一次查询批量获取多个体裁的 ID，
// 返回顺序与传入的名称一致；任一名称查不到时返回错误。
func (r *Repository) GetPoetryTypeIDs(names []string) ([]int64, error) {
	if len(names) == 0 {
		return []int64{}, nil
	}

	var poetryTypes []PoetryType
	err := r.db.Table(r.poetryTypesTable()).
		Where("name IN ?", names).
		Find(&poetryTypes).Error
	if err != nil {
		return nil, err
	}

	// 注意：这里刻意不去比较返回行数与 len(names)。
	// 重复的名称（如 ?type=五言绝句&type=五言绝句）在 IN 子句中会合并成一行，
	// 若按行数比较会误拒完全合法的请求。下面逐名查表时已能识别真正不存在的名称。

	// 建映射表以便 O(1) 查找
	typeMap := make(map[string]int64, len(poetryTypes))
	for _, pt := range poetryTypes {
		typeMap[pt.Name] = pt.ID
	}

	// 按输入名称的顺序返回 ID
	ids := make([]int64, len(names))
	for i, name := range names {
		id, ok := typeMap[name]
		if !ok {
			return nil, gorm.ErrRecordNotFound
		}
		ids[i] = id
	}

	return ids, nil
}

// InsertPoem 插入单首诗词。
func (r *Repository) InsertPoem(poem *Poem) error {
	if poemUsesGovernedIdentity(poem) {
		return r.BatchInsertPoemsWithTransaction([]*Poem{poem}, 1, 1, nil)
	}
	return r.db.Table(r.poemsTable()).Create(poem).Error
}

// BatchInsertPoems 分批插入诗词；带 canonical identity 的诗词会同时保存并核验
// 每个来源 witness。旧调用者仍可写入不带治理字段的兼容记录。
func (r *Repository) BatchInsertPoems(poems []*Poem, batchSize int) error {
	return r.BatchInsertPoemsWithTransaction(poems, len(poems), batchSize, nil)
}

// BatchInsertPoemsWithTransaction 用大事务批量写入诗词以获得最佳性能，
// 把多个批次合并进同一个事务可显著降低 fsync 开销。
// transactionSize：每个事务写入的诗词数（如 10000）
// batchSize：单条 INSERT 语句写入的诗词数（如 1000）
// progress：用于展示写入进度的进度条容器
func (r *Repository) BatchInsertPoemsWithTransaction(poems []*Poem, transactionSize, batchSize int, progress *mpb.Progress) (returnErr error) {
	if len(poems) == 0 {
		return nil
	}
	prepared, err := preparePoemsForInsert(poems)
	if err != nil {
		return err
	}

	if transactionSize <= 0 {
		transactionSize = 20000 // 默认每个事务 2 万首
	}
	if batchSize <= 0 {
		batchSize = 1000 // 默认每条 INSERT 一千首
	}

	totalTransactions := (len(prepared) + transactionSize - 1) / transactionSize

	// 进度条按诗词数而非事务数计量，刷新更平滑
	var poemBar *mpb.Bar
	if progress != nil {
		poemBar = progress.AddBar(int64(len(poems)),
			mpb.PrependDecorators(
				decor.Name("Inserting Poems: ", decor.WC{W: 17, C: decor.DindentRight}),
				decor.CountersNoUnit("%d / %d", decor.WCSyncWidth),
			),
			mpb.AppendDecorators(
				decor.Percentage(decor.WC{W: 5}),
				decor.Name(" | "),
				decor.AverageETA(decor.ET_STYLE_GO, decor.WC{W: 6}),
			),
		)
		// A failed transaction must terminate the progress bar as well. Otherwise
		// the caller's Progress.Wait blocks forever waiting for a total that can no
		// longer be reached, hiding the original database error.
		defer func() {
			if returnErr != nil {
				poemBar.Abort(true)
			}
		}()
	}

	logger.Info("Starting batch insertion",
		zap.Int("source_records", len(poems)),
		zap.Int("canonical_products", len(prepared)),
		zap.Int("transactions", totalTransactions),
		zap.Int("batch_size", batchSize),
	)

	// 按事务粒度切分并逐块写入
	for i := 0; i < len(prepared); i += transactionSize {
		end := min(i+transactionSize, len(prepared))
		transactionChunk := prepared[i:end]

		err := r.db.Transaction(func(tx *gorm.DB) error {
			return r.insertPoemsAndSources(tx, transactionChunk, batchSize)
		})
		if err != nil {
			txNum := i/transactionSize + 1
			return fmt.Errorf("failed to insert transaction %d/%d (poems %d-%d): %w",
				txNum, totalTransactions, i, end, err)
		}
		if poemBar != nil {
			writtenSources := 0
			for _, poem := range transactionChunk {
				writtenSources += max(1, len(poem.Sources))
			}
			poemBar.IncrBy(writtenSources)
		}
	}

	return nil
}

// UpsertPoem 插入诗词，若已存在则更新（用于处理重复数据）。
func (r *Repository) UpsertPoem(poem *Poem) error {
	if poemUsesGovernedIdentity(poem) {
		return r.BatchInsertPoemsWithTransaction([]*Poem{poem}, 1, 1, nil)
	}
	return r.db.Table(r.poemsTable()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"title", "content", "author_id", "dynasty_id", "type_id"}),
	}).Create(poem).Error
}

func poemUsesGovernedIdentity(poem *Poem) bool {
	return poem != nil && (poem.CanonicalID != nil || poem.CanonicalFingerprint != nil || len(poem.Sources) > 0)
}

// preparePoemsForInsert validates the complete input before the first write,
// aggregates every witness for the same canonical poem, and rejects any
// disagreement in the canonical product fields before the first database write.
func preparePoemsForInsert(poems []*Poem) ([]*Poem, error) {
	canonical := make(map[string]*Poem)
	seenLocators := make(map[string]string)
	prepared := make([]*Poem, 0, len(poems))

	for inputIndex, poem := range poems {
		if poem == nil {
			return nil, fmt.Errorf("poem input %d is nil", inputIndex)
		}
		if !poemUsesGovernedIdentity(poem) {
			copyPoem := *poem
			prepared = append(prepared, &copyPoem)
			continue
		}
		if poem.CanonicalID == nil || poem.CanonicalFingerprint == nil {
			return nil, fmt.Errorf("poem input %d has incomplete canonical identity", inputIndex)
		}
		if err := validateCanonicalIdentity(*poem.CanonicalID, *poem.CanonicalFingerprint); err != nil {
			return nil, fmt.Errorf("poem input %d: %w", inputIndex, err)
		}
		expectedID, err := PoemIDFromCanonical(*poem.CanonicalID)
		if err != nil {
			return nil, fmt.Errorf("poem input %d: %w", inputIndex, err)
		}
		if poem.ID != expectedID {
			return nil, fmt.Errorf(
				"canonical poem %q has public ID %d, expected %d",
				*poem.CanonicalID, poem.ID, expectedID,
			)
		}
		if len(poem.Sources) == 0 {
			return nil, fmt.Errorf("canonical poem %q has no source witness", *poem.CanonicalID)
		}

		validatedSources := make([]PoemSource, len(poem.Sources))
		for sourceIndex, source := range poem.Sources {
			if err := validatePoemSource(source); err != nil {
				return nil, fmt.Errorf("canonical poem %q source %d: %w", *poem.CanonicalID, sourceIndex, err)
			}
			if owner, exists := seenLocators[source.LocatorID]; exists {
				return nil, fmt.Errorf("source locator %q occurs more than once (canonical poems %q and %q)", source.LocatorID, owner, *poem.CanonicalID)
			}
			seenLocators[source.LocatorID] = *poem.CanonicalID
			source.ID = 0
			source.PoemID = 0
			validatedSources[sourceIndex] = source
		}

		if existing, exists := canonical[*poem.CanonicalID]; exists {
			if *existing.CanonicalFingerprint != *poem.CanonicalFingerprint {
				return nil, fmt.Errorf("canonical collision for %q: fingerprints differ", *poem.CanonicalID)
			}
			if field := poemProductDifference(existing, poem); field != "" {
				return nil, fmt.Errorf(
					"canonical product conflict for %q: field %s differs",
					*poem.CanonicalID, field,
				)
			}
			existing.Sources = append(existing.Sources, validatedSources...)
			continue
		}

		copyPoem := *poem
		copyPoem.Sources = validatedSources
		canonical[*poem.CanonicalID] = &copyPoem
	}

	for _, poem := range canonical {
		sort.Slice(poem.Sources, func(i, j int) bool {
			return poem.Sources[i].LocatorID < poem.Sources[j].LocatorID
		})
		prepared = append(prepared, poem)
	}
	sort.SliceStable(prepared, func(i, j int) bool {
		if prepared[i].ID != prepared[j].ID {
			return prepared[i].ID < prepared[j].ID
		}
		left, right := "", ""
		if prepared[i].CanonicalID != nil {
			left = *prepared[i].CanonicalID
		}
		if prepared[j].CanonicalID != nil {
			right = *prepared[j].CanonicalID
		}
		return left < right
	})

	governedIDs := make(map[int64]string)
	for _, poem := range prepared {
		if poem.CanonicalID == nil {
			continue
		}
		if previous, exists := governedIDs[poem.ID]; exists && previous != *poem.CanonicalID {
			return nil, fmt.Errorf("stable integer ID %d is shared by canonical poems %q and %q", poem.ID, previous, *poem.CanonicalID)
		}
		governedIDs[poem.ID] = *poem.CanonicalID
	}
	return prepared, nil
}

func (r *Repository) insertPoemsAndSources(tx *gorm.DB, poems []*Poem, batchSize int) error {
	legacy := make([]*Poem, 0, len(poems))
	governed := make([]*Poem, 0, len(poems))
	for _, poem := range poems {
		if poem.CanonicalID == nil {
			legacy = append(legacy, poem)
		} else {
			governed = append(governed, poem)
		}
	}

	if len(legacy) > 0 {
		if err := tx.Table(r.poemsTable()).Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(legacy, batchSize).Error; err != nil {
			return fmt.Errorf("failed to insert legacy poems: %w", err)
		}
	}
	if len(governed) == 0 {
		return nil
	}

	products := make([]*Poem, len(governed))
	for i, poem := range governed {
		copyPoem := *poem
		copyPoem.Sources = nil
		products[i] = &copyPoem
	}
	// A target-less DO NOTHING covers both a prior canonical row and a numeric ID
	// conflict. Both cases are distinguished and validated by the mandatory readback.
	if err := tx.Table(r.poemsTable()).Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(products, batchSize).Error; err != nil {
		return fmt.Errorf("failed to insert canonical poems: %w", err)
	}

	canonicalIDs := make([]string, len(governed))
	for i, poem := range governed {
		canonicalIDs[i] = *poem.CanonicalID
	}
	rowsByCanonical, err := r.readCanonicalRows(tx, canonicalIDs)
	if err != nil {
		return err
	}

	expectedSources := make([]PoemSource, 0)
	for _, poem := range governed {
		row, exists := rowsByCanonical[*poem.CanonicalID]
		if !exists {
			return fmt.Errorf("canonical poem %q was not persisted (possible numeric ID conflict)", *poem.CanonicalID)
		}
		if row.CanonicalFingerprint == nil || *row.CanonicalFingerprint != *poem.CanonicalFingerprint {
			return fmt.Errorf("canonical collision for %q: stored fingerprint differs", *poem.CanonicalID)
		}
		if field := poemProductDifference(poem, &row); field != "" {
			return fmt.Errorf(
				"canonical product conflict for %q: stored field %s differs",
				*poem.CanonicalID, field,
			)
		}
		for _, source := range poem.Sources {
			source.PoemID = row.ID
			expectedSources = append(expectedSources, source)
		}
	}
	if err := rejectAcceptedSourceOverlap(tx, expectedSources); err != nil {
		return err
	}

	if err := tx.Table(r.poemSourcesTable()).Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(expectedSources, batchSize).Error; err != nil {
		return fmt.Errorf("failed to insert source witnesses: %w", err)
	}
	return r.verifySourceWitnesses(tx, expectedSources)
}

func rejectAcceptedSourceOverlap(tx *gorm.DB, expected []PoemSource) error {
	const queryChunkSize = 400
	for i := 0; i < len(expected); i += queryChunkSize {
		end := min(i+queryChunkSize, len(expected))
		locators := make([]string, 0, end-i)
		for _, source := range expected[i:end] {
			locators = append(locators, source.LocatorID)
		}
		var count int64
		if err := tx.Table("source_rejections").Where("locator_id IN ?", locators).Count(&count).Error; err != nil {
			return fmt.Errorf("failed to check accepted/rejected source overlap: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("%d accepted source locators are already recorded as rejected", count)
		}
	}
	return nil
}

func (r *Repository) readCanonicalRows(tx *gorm.DB, canonicalIDs []string) (map[string]Poem, error) {
	const queryChunkSize = 400
	rowsByCanonical := make(map[string]Poem, len(canonicalIDs))
	for i := 0; i < len(canonicalIDs); i += queryChunkSize {
		end := min(i+queryChunkSize, len(canonicalIDs))
		var rows []Poem
		if err := tx.Table(r.poemsTable()).
			Select(
				"id", "type_id", "title", "content", "content_hash",
				"canonical_id", "canonical_fingerprint", "author_id", "dynasty_id",
			).
			Where("canonical_id IN ?", canonicalIDs[i:end]).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("failed to read back canonical poems: %w", err)
		}
		for _, row := range rows {
			if row.CanonicalID != nil {
				rowsByCanonical[*row.CanonicalID] = row
			}
		}
	}
	return rowsByCanonical, nil
}

// poemProductDifference returns the first persisted product field that differs.
// Provenance witnesses, timestamps, and loaded relation objects are deliberately
// excluded: they are not part of the canonical API product row.
func poemProductDifference(left, right *Poem) string {
	if left.ID != right.ID {
		return "id"
	}
	if !equalInt64Pointers(left.TypeID, right.TypeID) {
		return "type_id"
	}
	if left.Title != right.Title {
		return "title"
	}
	if !bytes.Equal(left.Content, right.Content) {
		return "content"
	}
	if left.ContentHash != right.ContentHash {
		return "content_hash"
	}
	if !equalInt64Pointers(left.AuthorID, right.AuthorID) {
		return "author_id"
	}
	if !equalInt64Pointers(left.DynastyID, right.DynastyID) {
		return "dynasty_id"
	}
	return ""
}

func equalInt64Pointers(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func (r *Repository) verifySourceWitnesses(tx *gorm.DB, expected []PoemSource) error {
	const queryChunkSize = 400
	actualByLocator := make(map[string]PoemSource, len(expected))
	for i := 0; i < len(expected); i += queryChunkSize {
		end := min(i+queryChunkSize, len(expected))
		locators := make([]string, 0, end-i)
		for _, source := range expected[i:end] {
			locators = append(locators, source.LocatorID)
		}
		var rows []PoemSource
		if err := tx.Table(r.poemSourcesTable()).Where("locator_id IN ?", locators).Find(&rows).Error; err != nil {
			return fmt.Errorf("failed to read back source witnesses: %w", err)
		}
		for _, row := range rows {
			actualByLocator[row.LocatorID] = row
		}
	}

	missing := 0
	for _, source := range expected {
		actual, exists := actualByLocator[source.LocatorID]
		if !exists {
			missing++
			continue
		}
		if actual.PoemID != source.PoemID || actual.SourceID != source.SourceID ||
			actual.DatasetKey != source.DatasetKey || actual.SourcePath != source.SourcePath ||
			actual.SourceRecordIndex != source.SourceRecordIndex {
			return fmt.Errorf("source locator collision for %q: stored witness differs", source.LocatorID)
		}
	}
	if missing > 0 {
		return fmt.Errorf("failed to persist %d of %d source witnesses", missing, len(expected))
	}
	return nil
}
