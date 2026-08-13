package database

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// setupTestDB 创建测试用的内存数据库。
func setupGetPoetryTypeIDsTestDB(t *testing.T) (*DB, *Repository) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	db := &DB{DB: gormDB}
	err = db.Migrate()
	require.NoError(t, err)

	repo := NewRepository(db)
	return db, repo
}

func TestGovernedInsertPreservesEverySourceWitness(t *testing.T) {
	_, repo := setupGetPoetryTypeIDsTestDB(t)
	id, fingerprint := testCanonicalIdentity("same-logical-poem")
	sourceA, err := NewPoemSource("upstream-a", "tangsong", "全唐诗/poet.tang.0.json", 0)
	require.NoError(t, err)
	sourceB, err := NewPoemSource("upstream-b", "yudingquantangshi", "御定全唐詩/json/poet.json", 8)
	require.NoError(t, err)

	poems := []*Poem{
		governedTestPoem("同源异证", id, fingerprint, sourceA),
		governedTestPoem("同源异证", id, fingerprint, sourceB),
	}
	require.NoError(t, repo.BatchInsertPoemsWithTransaction(poems, 100, 10, nil))
	wantPoemID, err := PoemIDFromCanonical(id)
	require.NoError(t, err)

	var productCount, sourceCount int64
	require.NoError(t, repo.db.Table(repo.poemsTable()).Count(&productCount).Error)
	require.NoError(t, repo.db.Table(repo.poemSourcesTable()).Count(&sourceCount).Error)
	assert.Equal(t, int64(1), productCount)
	assert.Equal(t, int64(2), sourceCount)

	var sources []PoemSource
	require.NoError(t, repo.db.Table(repo.poemSourcesTable()).Order("locator_id").Find(&sources).Error)
	require.Len(t, sources, 2)
	assert.ElementsMatch(t, []string{"upstream-a", "upstream-b"}, []string{sources[0].SourceID, sources[1].SourceID})
	for _, source := range sources {
		assert.Equal(t, wantPoemID, source.PoemID)
		assert.NotEmpty(t, source.DatasetKey)
		assert.True(t, strings.HasSuffix(source.SourcePath, ".json"))
	}
}

func TestGovernedInsertRejectsCanonicalCollisionAndInvalidSource(t *testing.T) {
	_, repo := setupGetPoetryTypeIDsTestDB(t)
	id, fingerprint := testCanonicalIdentity("collision-test")
	_, otherFingerprint := testCanonicalIdentity("different-input")
	sourceA, err := NewPoemSource("a", "tangsong", "全唐诗/poet.tang.0.json", 0)
	require.NoError(t, err)
	sourceB, err := NewPoemSource("b", "tangsong", "全唐诗/poet.tang.0.json", 1)
	require.NoError(t, err)

	err = repo.BatchInsertPoems([]*Poem{
		governedTestPoem("碰撞", id, fingerprint, sourceA),
		governedTestPoem("碰撞", id, otherFingerprint, sourceB),
	}, 10)
	require.ErrorContains(t, err, "canonical collision")

	invalid := sourceA
	invalid.SourcePath = "../escape.json"
	err = repo.BatchInsertPoems([]*Poem{governedTestPoem("非法来源", id, fingerprint, invalid)}, 10)
	require.ErrorContains(t, err, "source path")

	var count int64
	require.NoError(t, repo.db.Table(repo.poemsTable()).Count(&count).Error)
	assert.Zero(t, count, "preflight failures must not partially write products")
}

func TestGovernedInsertRejectsStableIntegerIDCollision(t *testing.T) {
	_, repo := setupGetPoetryTypeIDsTestDB(t)
	idA, fingerprintA := testCanonicalIdentity("integer-collision-a")
	_, fingerprintB := testCanonicalIdentity("integer-collision-b")
	// Public IDs use the first 53 bits of the canonical SHA-256 digest. Change
	// only the tail so these two valid canonical IDs deliberately exercise the
	// repository's truncated-integer collision guard.
	tail := byte('0')
	if idA[len(idA)-1] == tail {
		tail = '1'
	}
	idB := idA[:len(idA)-1] + string(tail)
	publicID, err := PoemIDFromCanonical(idA)
	require.NoError(t, err)
	publicIDB, err := PoemIDFromCanonical(idB)
	require.NoError(t, err)
	require.Equal(t, publicID, publicIDB)
	sourceA, err := NewPoemSource("a", "tangsong", "全唐诗/poet.tang.0.json", 0)
	require.NoError(t, err)
	sourceB, err := NewPoemSource("b", "tangsong", "全唐诗/poet.tang.0.json", 1)
	require.NoError(t, err)

	err = repo.BatchInsertPoems([]*Poem{
		governedTestPoem("整数碰撞甲", idA, fingerprintA, sourceA),
		governedTestPoem("整数碰撞乙", idB, fingerprintB, sourceB),
	}, 10)
	require.ErrorContains(t, err, "stable integer ID")
	require.ErrorContains(t, err, "is shared")

	var count int64
	require.NoError(t, repo.db.Table(repo.poemsTable()).Count(&count).Error)
	assert.Zero(t, count)
}

func TestSourceRejectionLedgerAndAcceptedOverlapFailClosed(t *testing.T) {
	db, repo := setupGetPoetryTypeIDsTestDB(t)
	rejection, err := NewSourceRejection(
		"bad-source", "tangsong", "全唐诗/poet.song.0.json", 7,
		"normalization", "placeholder_content",
	)
	require.NoError(t, err)
	require.NoError(t, db.InsertSourceRejections([]SourceRejection{rejection}))

	conflicting := rejection
	conflicting.Reason = "unicode_replacement_character"
	err = db.InsertSourceRejections([]SourceRejection{conflicting})
	require.ErrorContains(t, err, "stored row differs")

	id, fingerprint := testCanonicalIdentity("accepted-overlap")
	source := PoemSource{
		LocatorID:         rejection.LocatorID,
		SourceID:          rejection.SourceID,
		DatasetKey:        rejection.DatasetKey,
		SourcePath:        rejection.SourcePath,
		SourceRecordIndex: rejection.SourceRecordIndex,
	}
	err = repo.BatchInsertPoems([]*Poem{governedTestPoem("冲突来源", id, fingerprint, source)}, 10)
	require.ErrorContains(t, err, "already recorded as rejected")

	var productCount, rejectionCount int64
	require.NoError(t, repo.db.Table(repo.poemsTable()).Count(&productCount).Error)
	require.NoError(t, db.Table("source_rejections").Count(&rejectionCount).Error)
	assert.Zero(t, productCount)
	assert.Equal(t, int64(1), rejectionCount)
}

func TestSameTitleAndContentDoNotCollapseDistinctCanonicalPoems(t *testing.T) {
	_, repo := setupGetPoetryTypeIDsTestDB(t)
	idA, fingerprintA := testCanonicalIdentity("author-a")
	idB, fingerprintB := testCanonicalIdentity("author-b")
	sourceA, err := NewPoemSource("a", "tangsong", "全唐诗/poet.tang.0.json", 0)
	require.NoError(t, err)
	sourceB, err := NewPoemSource("b", "tangsong", "全唐诗/poet.tang.0.json", 1)
	require.NoError(t, err)

	poemA := governedTestPoem("同题", idA, fingerprintA, sourceA)
	poemB := governedTestPoem("同题", idB, fingerprintB, sourceB)
	poemA.ContentHash = "same-content-hash"
	poemB.ContentHash = "same-content-hash"
	require.NoError(t, repo.BatchInsertPoems([]*Poem{poemA, poemB}, 10))

	var count int64
	require.NoError(t, repo.db.Table(repo.poemsTable()).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

func TestGovernedInsertRejectsDifferentProductsForOneCanonicalIdentity(t *testing.T) {
	fields := []struct {
		name   string
		mutate func(*Poem)
	}{
		{"title", func(poem *Poem) { poem.Title = "不同标题" }},
		{"content", func(poem *Poem) { poem.Content = datatypes.JSON([]byte(`["不同正文。"]`)) }},
		{"content hash", func(poem *Poem) { poem.ContentHash = "different-content-hash" }},
		{"type id", func(poem *Poem) { poem.TypeID = testInt64Ptr(12) }},
		{"author id", func(poem *Poem) { poem.AuthorID = testInt64Ptr(22) }},
		{"dynasty id", func(poem *Poem) { poem.DynastyID = testInt64Ptr(32) }},
	}

	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			_, repo := setupGetPoetryTypeIDsTestDB(t)
			installGovernedProductRelationFixtures(t, repo)
			canonicalID, fingerprint := testCanonicalIdentity("same-canonical-different-product-" + field.name)
			sourceA, err := NewPoemSource("a", "tangsong", "全唐诗/poet.tang.0.json", 0)
			require.NoError(t, err)
			sourceB, err := NewPoemSource("b", "tangsong", "全唐诗/poet.tang.0.json", 1)
			require.NoError(t, err)

			first := governedTestPoem("同一成品", canonicalID, fingerprint, sourceA)
			second := governedTestPoem("同一成品", canonicalID, fingerprint, sourceB)
			first.TypeID, first.AuthorID, first.DynastyID = testInt64Ptr(11), testInt64Ptr(21), testInt64Ptr(31)
			second.TypeID, second.AuthorID, second.DynastyID = testInt64Ptr(11), testInt64Ptr(21), testInt64Ptr(31)
			field.mutate(second)

			err = repo.BatchInsertPoems([]*Poem{first, second}, 10)
			require.ErrorContains(t, err, "canonical")
			require.ErrorContains(t, err, "product")

			var products, witnesses int64
			require.NoError(t, repo.db.Table(repo.poemsTable()).Count(&products).Error)
			require.NoError(t, repo.db.Table(repo.poemSourcesTable()).Count(&witnesses).Error)
			assert.Zero(t, products, "preflight conflict must not persist either candidate product")
			assert.Zero(t, witnesses, "preflight conflict must not persist either witness")
		})
	}
}

func TestGovernedInsertReadbackRejectsDifferentStoredProductAndDoesNotAttachWitness(t *testing.T) {
	fields := []struct {
		name   string
		mutate func(*Poem)
	}{
		{"public id", func(poem *Poem) { poem.ID++ }},
		{"title", func(poem *Poem) { poem.Title = "数据库中的不同标题" }},
		{"content", func(poem *Poem) { poem.Content = datatypes.JSON([]byte(`["数据库中的不同正文。"]`)) }},
		{"content hash", func(poem *Poem) { poem.ContentHash = "stored-different-content-hash" }},
		{"type id", func(poem *Poem) { poem.TypeID = testInt64Ptr(12) }},
		{"author id", func(poem *Poem) { poem.AuthorID = testInt64Ptr(22) }},
		{"dynasty id", func(poem *Poem) { poem.DynastyID = testInt64Ptr(32) }},
	}

	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			_, repo := setupGetPoetryTypeIDsTestDB(t)
			installGovernedProductRelationFixtures(t, repo)
			canonicalID, fingerprint := testCanonicalIdentity("stored-product-conflict-" + field.name)
			originalSource, err := NewPoemSource("original", "tangsong", "全唐诗/poet.tang.0.json", 0)
			require.NoError(t, err)
			newSource, err := NewPoemSource("new", "tangsong", "全唐诗/poet.tang.0.json", 1)
			require.NoError(t, err)

			expected := governedTestPoem("应有成品", canonicalID, fingerprint, newSource)
			expected.TypeID, expected.AuthorID, expected.DynastyID = testInt64Ptr(11), testInt64Ptr(21), testInt64Ptr(31)
			stored := *expected
			stored.Sources = nil
			field.mutate(&stored)
			require.NoError(t, repo.db.Table(repo.poemsTable()).Create(&stored).Error)
			// This original witness proves the transaction must leave pre-existing
			// provenance intact while refusing to attach the new source occurrence.
			originalSource.PoemID = stored.ID
			require.NoError(t, repo.db.Table(repo.poemSourcesTable()).Create(&originalSource).Error)

			err = repo.BatchInsertPoems([]*Poem{expected}, 10)
			require.ErrorContains(t, err, "canonical")
			require.ErrorContains(t, err, "product")

			var witnesses []PoemSource
			require.NoError(t, repo.db.Table(repo.poemSourcesTable()).Order("locator_id").Find(&witnesses).Error)
			require.Len(t, witnesses, 1)
			assert.Equal(t, originalSource.LocatorID, witnesses[0].LocatorID)
		})
	}
}

func TestPoemIDFromCanonicalMatchesGovernedInsertFormula(t *testing.T) {
	canonicalID, fingerprint := testCanonicalIdentity("repository-public-id-formula")
	want, err := PoemIDFromCanonical(canonicalID)
	require.NoError(t, err)
	require.Positive(t, want)
	require.LessOrEqual(t, want, int64((1<<53)-1))

	source, err := NewPoemSource("formula", "tangsong", "全唐诗/poet.tang.0.json", 0)
	require.NoError(t, err)
	poem := governedTestPoem("公式回归", canonicalID, fingerprint, source)
	assert.Equal(t, want, poem.ID)

	_, repo := setupGetPoetryTypeIDsTestDB(t)
	require.NoError(t, repo.BatchInsertPoems([]*Poem{poem}, 10))
	var stored Poem
	require.NoError(t, repo.db.Table(repo.poemsTable()).Where("canonical_id = ?", canonicalID).First(&stored).Error)
	assert.Equal(t, want, stored.ID)

	invalid := governedTestPoem("公式回归", canonicalID, fingerprint, source)
	invalid.ID++
	err = repo.BatchInsertPoems([]*Poem{invalid}, 10)
	require.ErrorContains(t, err, "has public ID")
	require.ErrorContains(t, err, "expected")
}

func testCanonicalIdentity(seed string) (string, string) {
	primary := sha256.Sum256([]byte(seed))
	fingerprint := sha512.Sum512([]byte(seed))
	return CanonicalIDPrefix + hex.EncodeToString(primary[:]), hex.EncodeToString(fingerprint[:])
}

func governedTestPoem(title, canonicalID, fingerprint string, source PoemSource) *Poem {
	id, err := PoemIDFromCanonical(canonicalID)
	if err != nil {
		panic(err)
	}
	return &Poem{
		ID:                   id,
		Title:                title,
		Content:              datatypes.JSON([]byte(`["正文。"]`)),
		ContentHash:          "content-hash",
		CanonicalID:          &canonicalID,
		CanonicalFingerprint: &fingerprint,
		Sources:              []PoemSource{source},
	}
}

func testInt64Ptr(value int64) *int64 { return &value }

func installGovernedProductRelationFixtures(t *testing.T, repo *Repository) {
	t.Helper()
	for _, dynasty := range []Dynasty{
		{ID: 31, Name: "测试朝代甲"},
		{ID: 32, Name: "测试朝代乙"},
	} {
		require.NoError(t, repo.db.Table(repo.dynastiesTable()).Create(&dynasty).Error)
	}
	for _, author := range []Author{
		{ID: 21, CanonicalID: CanonicalAuthorIDPrefix + strings.Repeat("1", 64), Name: "测试作者甲", DynastyID: testInt64Ptr(31)},
		{ID: 22, CanonicalID: CanonicalAuthorIDPrefix + strings.Repeat("2", 64), Name: "测试作者乙", DynastyID: testInt64Ptr(32)},
	} {
		require.NoError(t, repo.db.Table(repo.authorsTable()).Create(&author).Error)
	}
}

func TestGetPoetryTypeIDs(t *testing.T) {
	_, repo := setupGetPoetryTypeIDsTestDB(t)

	// 借助 ON CONFLICT 写入测试用的体裁，规避唯一约束冲突
	types := []string{"五言绝句", "七言绝句", "五言律诗", "七言律诗"}
	for _, typeName := range types {
		poetryType := PoetryType{Name: typeName}
		err := repo.db.Table(repo.poetryTypesTable()).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&poetryType).Error
		require.NoError(t, err)
	}

	tests := []struct {
		name          string
		inputNames    []string
		expectError   bool
		expectedCount int
	}{
		{
			name:          "fetch multiple existing types",
			inputNames:    []string{"五言绝句", "七言绝句"},
			expectError:   false,
			expectedCount: 2,
		},
		{
			name:          "fetch all types",
			inputNames:    types,
			expectError:   false,
			expectedCount: 4,
		},
		{
			name:          "fetch single type",
			inputNames:    []string{"五言绝句"},
			expectError:   false,
			expectedCount: 1,
		},
		{
			name:          "empty input",
			inputNames:    []string{},
			expectError:   false,
			expectedCount: 0,
		},
		{
			name:        "non-existent type",
			inputNames:  []string{"不存在的类型"},
			expectError: true,
		},
		{
			name:        "mixed existing and non-existent",
			inputNames:  []string{"五言绝句", "不存在的类型"},
			expectError: true,
		},
		{
			// 重复的名称在 IN 子句中会合并成一行。早先拿返回行数与 len(names)
			// 比较会因此误拒该请求，表现为
			// as a 404 for ?type=五言绝句&type=五言绝句.
			name:          "repeated name",
			inputNames:    []string{"五言绝句", "五言绝句"},
			expectError:   false,
			expectedCount: 2,
		},
		{
			name:          "repeated name mixed with a distinct one",
			inputNames:    []string{"五言绝句", "七言绝句", "五言绝句"},
			expectError:   false,
			expectedCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := repo.GetPoetryTypeIDs(tt.inputNames)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, ids)
			} else {
				require.NoError(t, err)
				assert.Len(t, ids, tt.expectedCount)

				// 返回的 ID 顺序应与输入名称一致
				if len(tt.inputNames) > 0 {
					for i, name := range tt.inputNames {
						// 反查该 ID 应能得到相同的名称
						var poetryType PoetryType
						err := repo.db.Table(repo.poetryTypesTable()).First(&poetryType, ids[i]).Error
						require.NoError(t, err)
						assert.Equal(t, name, poetryType.Name)
					}
				}
			}
		})
	}
}

func TestGetPoetryTypeIDsWithCache(t *testing.T) {
	db, repo := setupGetPoetryTypeIDsTestDB(t)
	cachedRepo := NewCachedRepository(repo)

	// 借助 ON CONFLICT 写入测试用的体裁，规避唯一约束冲突
	types := []string{"五言绝句", "七言绝句", "五言律诗"}
	for _, typeName := range types {
		poetryType := PoetryType{Name: typeName}
		err := db.Table(repo.poetryTypesTable()).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&poetryType).Error
		require.NoError(t, err)
	}

	// 首次调用应填充缓存
	ids1, err := cachedRepo.GetPoetryTypeIDs(types)
	require.NoError(t, err)
	assert.Len(t, ids1, 3)

	// 第二次调用应命中缓存
	ids2, err := cachedRepo.GetPoetryTypeIDs(types)
	require.NoError(t, err)
	assert.Equal(t, ids1, ids2)

	// 部分命中缓存
	partialTypes := []string{"五言绝句", "七言绝句"} // These should be in cache
	ids3, err := cachedRepo.GetPoetryTypeIDs(partialTypes)
	require.NoError(t, err)
	assert.Len(t, ids3, 2)
	assert.Equal(t, ids1[0], ids3[0])
	assert.Equal(t, ids1[1], ids3[1])

	// 校验缓存统计
	stats := cachedRepo.GetCacheStats()
	assert.Equal(t, 3, stats["types"])
}
