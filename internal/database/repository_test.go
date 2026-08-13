package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupTestDB 创建测试用的内存 SQLite 数据库，
// 并建出默认语言（zh_hans）对应的表。
func setupTestDB(t *testing.T) *DB {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "Failed to create test database")

	db := &DB{DB: gormDB}

	// 建出默认语言（zh_hans）的表
	err = db.migrateTablesForLang(LangHans)
	require.NoError(t, err, "Failed to run migrations")

	return db
}

// createTestPoem 是测试辅助函数，按动态表名写入诗词。
func createTestPoem(repo *Repository, poem *Poem) error {
	return repo.db.Table(repo.poemsTable()).Create(poem).Error
}

// createTestPoetryType 是测试辅助函数，按动态表名写入体裁。
func createTestPoetryType(repo *Repository, ptype *PoetryType) error {
	return repo.db.Table(repo.poetryTypesTable()).Create(ptype).Error
}

func TestNewRepository(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRepository(db)

	assert.NotNil(t, repo)
	assert.NotNil(t, repo.db)
}

func TestGetOrCreateDynasty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRepository(db)

	tests := []struct {
		name        string
		dynastyName string
		wantErr     bool
	}{
		{"create new dynasty", "唐", false},
		{"get existing dynasty", "唐", false},
		{"create another dynasty", "宋", false},
	}

	var firstID int64
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := repo.GetOrCreateDynasty(tt.dynastyName)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Greater(t, id, int64(0))

				switch i {
				case 0:
					firstID = id
				case 1:
					// 第二次调用应返回相同的 ID
					assert.Equal(t, firstID, id, "Should return same ID for existing dynasty")
				}
			}
		})
	}
}

func TestGetOrCreateAuthor(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRepository(db)

	// 先写入朝代
	dynastyID, err := repo.GetOrCreateDynasty("唐")
	require.NoError(t, err)

	tests := []struct {
		name       string
		authorName string
		dynastyID  int64
		wantErr    bool
	}{
		{
			name:       "create new author",
			authorName: "李白",
			dynastyID:  dynastyID,
			wantErr:    false,
		},
		{
			name:       "get existing author",
			authorName: "李白",
			dynastyID:  dynastyID,
			wantErr:    false,
		},
	}

	var firstID int64
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := repo.GetOrCreateAuthor(tt.authorName, tt.dynastyID)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Greater(t, id, int64(0))

				switch i {
				case 0:
					firstID = id
				case 1:
					assert.Equal(t, firstID, id, "Should return same ID for existing author")
				}
			}
		})
	}
}

func TestAuthorIdentityIncludesDynasty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRepository(db)
	cached := NewCachedRepository(repo)

	tangID, err := cached.GetOrCreateDynasty("唐")
	require.NoError(t, err)
	songID, err := cached.GetOrCreateDynasty("宋")
	require.NoError(t, err)

	tangAuthorID, err := cached.GetOrCreateAuthor("张说", tangID)
	require.NoError(t, err)
	songAuthorID, err := cached.GetOrCreateAuthor("张说", songID)
	require.NoError(t, err)
	assert.NotEqual(t, tangAuthorID, songAuthorID)

	tangAgain, err := cached.GetOrCreateAuthor("张说", tangID)
	require.NoError(t, err)
	assert.Equal(t, tangAuthorID, tangAgain)
	assert.Equal(t, 2, cached.GetCacheStats()["authors"])

	_, err = repo.GetAuthorByName("张说")
	require.ErrorIs(t, err, ErrAmbiguousAuthor)
	resolved, err := repo.GetAuthorByNameAndDynasty("张说", &songID)
	require.NoError(t, err)
	assert.Equal(t, songAuthorID, resolved.ID)
}

func TestGetPoetryTypeID(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRepository(db)

	// 先按动态表名写入测试用的体裁
	poetryType := &PoetryType{
		Name:     "五言绝句",
		Category: "诗",
	}
	err := db.Table(repo.poetryTypesTable()).Create(poetryType).Error
	require.NoError(t, err, "Failed to create test poetry type")

	tests := []struct {
		name     string
		typeName string
		wantErr  bool
	}{
		{"get existing type", "五言绝句", false},
		{"get non-existent type", "不存在的类型", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := repo.GetPoetryTypeID(tt.typeName)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Greater(t, id, int64(0))
			}
		})
	}
}

func TestCountPoems(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRepository(db)

	// 初始应为 0
	count, err := repo.CountPoems()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestCountAuthors(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRepository(db)

	// 初始应为 0
	count, err := repo.CountAuthors()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// 基准测试
func BenchmarkGetOrCreateDynasty(b *testing.B) {
	gormDB, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	_ = gormDB.AutoMigrate(&Dynasty{})
	db := &DB{DB: gormDB}
	repo := NewRepository(db)

	for b.Loop() {
		_, _ = repo.GetOrCreateDynasty("唐")
	}
}
