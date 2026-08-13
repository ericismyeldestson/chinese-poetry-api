package graph

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/graph/generated"
)

// setupTestResolver 基于内存数据库创建测试用的 resolver。
func setupTestResolver(t *testing.T) (*Resolver, *database.Repository) {
	// 创建内存数据库
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// SQLite gives each :memory: connection an independent database. Keep this
	// test fixture on one connection so Hans and Hant operations in the same
	// GraphQL request observe the schema created by Migrate.
	sqlDB, err := gormDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	db := &database.DB{DB: gormDB}

	// 用 Migrate 建出各语言变体的表
	err = db.Migrate()
	require.NoError(t, err)

	repo := database.NewRepository(db)

	resolver := NewResolver(db, repo)
	return resolver, repo
}

// createTestClient 创建 GraphQL 测试客户端。
func createTestClient(t *testing.T, resolver *Resolver) *client.Client {
	t.Helper()
	srv := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{
		Resolvers: resolver,
	}))
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(WithLanguageState(ctx))
	})
	return client.New(srv)
}

// createTestData 向数据库写入测试数据。
func createTestData(t *testing.T, repo *database.Repository) (dynastyID, authorID int64, poemID int64) {
	var err error

	// 写入朝代
	dynastyID, err = repo.GetOrCreateDynasty("唐")
	require.NoError(t, err)

	// 写入作者
	authorID, err = repo.GetOrCreateAuthor("李白", dynastyID)
	require.NoError(t, err)

	// 写入诗词
	poem := &database.Poem{
		ID:        1,
		Title:     "静夜思",
		Content:   datatypes.JSON([]byte(`["床前明月光","疑是地上霜","举头望明月","低头思故乡"]`)),
		AuthorID:  &authorID,
		DynastyID: &dynastyID,
	}
	err = repo.InsertPoem(poem)
	require.NoError(t, err)

	return dynastyID, authorID, poem.ID
}

func TestPoemQuery(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	_, _, _ = createTestData(t, repo)
	c := createTestClient(t, resolver)

	t.Run("get existing poem", func(t *testing.T) {
		var resp struct {
			Poem struct {
				Title   string
				Content []string
			}
		}

		err := c.Post(`query { poem(id: "1") { title content } }`, &resp)
		require.NoError(t, err)
		assert.Equal(t, "静夜思", resp.Poem.Title)
		assert.Len(t, resp.Poem.Content, 4)
	})

	t.Run("get non-existent poem returns error", func(t *testing.T) {
		var resp struct {
			Poem *struct {
				Title string
			}
		}

		// 查询不存在的诗词时 GraphQL 会返回错误
		err := c.Post(`query { poem(id: "999") { title } }`, &resp)
		// 诗词本就不存在，报错属于预期行为
		assert.Error(t, err)
	})
}

func TestPoemsQuery(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createTestData(t, repo)
	c := createTestClient(t, resolver)

	t.Run("get poems with default pagination", func(t *testing.T) {
		var resp struct {
			Poems struct {
				Edges []struct {
					Node struct {
						Title string
					}
				}
				PageInfo struct {
					HasNextPage     bool
					HasPreviousPage bool
				}
				TotalCount int
			}
		}

		err := c.Post(`query { poems { edges { node { title } } pageInfo { hasNextPage hasPreviousPage } totalCount } }`, &resp)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, resp.Poems.TotalCount, 1)
		assert.GreaterOrEqual(t, len(resp.Poems.Edges), 1)
	})

	t.Run("get poems with pagination", func(t *testing.T) {
		var resp struct {
			Poems struct {
				TotalCount int
			}
		}

		err := c.Post(`query { poems(page: 1, pageSize: 5) { totalCount } }`, &resp)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, resp.Poems.TotalCount, 1)
	})
}

func TestSearchPoemsQuery(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createTestData(t, repo)
	c := createTestClient(t, resolver)

	t.Run("search poems", func(t *testing.T) {
		var resp struct {
			SearchPoems struct {
				Edges []struct {
					Node struct {
						Title string
					}
				}
				TotalCount int
			}
		}

		err := c.Post(`query { searchPoems(query: "静夜思") { edges { node { title } } totalCount } }`, &resp)
		require.NoError(t, err)
		// 搜索应能正常返回
		assert.NotNil(t, resp.SearchPoems)
	})

	t.Run("search with type", func(t *testing.T) {
		var resp struct {
			SearchPoems struct {
				TotalCount int
			}
		}

		err := c.Post(`query { searchPoems(query: "李白", searchType: AUTHOR) { totalCount } }`, &resp)
		require.NoError(t, err)
	})

	t.Run("surrounding whitespace is trimmed", func(t *testing.T) {
		var resp struct {
			SearchPoems struct{ TotalCount int }
		}
		err := c.Post(`query { searchPoems(query: "  静夜思　", searchType: TITLE) { totalCount } }`, &resp)
		require.NoError(t, err)
		assert.Equal(t, 1, resp.SearchPoems.TotalCount)
	})

	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"whitespace-only query is rejected", " \t　", "must not be empty"},
		{"query above Unicode length cap is rejected", strings.Repeat("诗", 101), "at most 100 characters"},
		{"short indexed query is rejected", "李白", "use type=author"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp struct{}
			query := fmt.Sprintf(`query { searchPoems(query: %s) { totalCount } }`, strconv.Quote(tc.query))
			err := c.Post(query, &resp)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestSearchPoemsRejectsLikeSyntaxForIndexedSearch(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	for i, title := range []string{"价格100%", "下划_线", `反斜\杠`} {
		err := repo.InsertPoem(&database.Poem{
			ID:      int64(900 + i),
			Title:   title,
			Content: datatypes.JSON([]byte(`["独特正文"]`)),
		})
		require.NoError(t, err)
	}
	c := createTestClient(t, resolver)

	for _, queryText := range []string{
		"100%",
		"划_线",
		`斜\杠`,
	} {
		t.Run(queryText, func(t *testing.T) {
			var resp struct{}
			query := fmt.Sprintf(`query { searchPoems(query: %s, searchType: TITLE) { totalCount } }`, strconv.Quote(queryText))
			err := c.Post(query, &resp)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not accept LIKE wildcard characters")
		})
	}
}

func TestAuthorsQuery(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createTestData(t, repo)
	c := createTestClient(t, resolver)

	t.Run("get authors", func(t *testing.T) {
		var resp struct {
			Authors struct {
				Edges []struct {
					Node struct {
						Name string
					}
				}
				TotalCount int
			}
		}

		err := c.Post(`query { authors { edges { node { name } } totalCount } }`, &resp)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, resp.Authors.TotalCount, 1)
	})
}

func TestTraditionalAuthorAndStatisticsPreserveLanguageContext(t *testing.T) {
	resolver, baseRepo := setupTestResolver(t)
	hansRepo := baseRepo.WithLang(database.LangHans)
	hantRepo := baseRepo.WithLang(database.LangHant)

	const relationID int64 = 8001
	require.NoError(t, resolver.DB.Table(database.DynastiesTable(database.LangHans)).Create(&database.Dynasty{
		ID: relationID, Name: "简朝",
	}).Error)
	require.NoError(t, resolver.DB.Table(database.DynastiesTable(database.LangHant)).Create(&database.Dynasty{
		ID: relationID, Name: "繁朝",
	}).Error)
	require.NoError(t, resolver.DB.Table(database.PoetryTypesTable(database.LangHans)).Create(&database.PoetryType{
		ID: relationID, Name: "简体类", Category: "诗",
	}).Error)
	require.NoError(t, resolver.DB.Table(database.PoetryTypesTable(database.LangHant)).Create(&database.PoetryType{
		ID: relationID, Name: "繁體類", Category: "詩",
	}).Error)
	require.NoError(t, resolver.DB.Table(database.AuthorsTable(database.LangHans)).Create(&database.Author{
		ID: relationID, CanonicalID: database.CanonicalAuthorIDPrefix + strings.Repeat("8", 64), Name: "简体错误作者", DynastyID: ptrInt64(relationID),
	}).Error)
	require.NoError(t, resolver.DB.Table(database.AuthorsTable(database.LangHant)).Create(&database.Author{
		ID: relationID, CanonicalID: database.CanonicalAuthorIDPrefix + strings.Repeat("8", 64), Name: "繁體正確作者", DynastyID: ptrInt64(relationID),
	}).Error)

	for i, title := range []string{"简体错误一", "简体错误二"} {
		require.NoError(t, hansRepo.InsertPoem(&database.Poem{
			ID:        8101 + int64(i),
			Title:     title,
			Content:   datatypes.JSON([]byte(`["简体正文"]`)),
			AuthorID:  ptrInt64(relationID),
			DynastyID: ptrInt64(relationID),
			TypeID:    ptrInt64(relationID),
		}))
	}
	require.NoError(t, hantRepo.InsertPoem(&database.Poem{
		ID:        8101,
		Title:     "繁體正確作品",
		Content:   datatypes.JSON([]byte(`["繁體正文"]`)),
		AuthorID:  ptrInt64(relationID),
		DynastyID: ptrInt64(relationID),
		TypeID:    ptrInt64(relationID),
	}))

	c := createTestClient(t, resolver)
	var resp struct {
		Author struct {
			Name      string
			PoemCount int
			Poems     struct {
				TotalCount int
				Edges      []struct{ Node struct{ Title string } }
			}
		}
		Authors struct {
			Edges []struct {
				Node struct {
					Name      string
					PoemCount int
					Poems     struct{ TotalCount int }
				}
			}
		}
		Poems struct {
			Edges []struct {
				Node struct {
					Title  string
					Author struct {
						Name      string
						PoemCount int
					}
				}
			}
		}
		Statistics struct {
			PoemsByDynasty []struct {
				Dynasty struct {
					Name        string
					PoemCount   int
					AuthorCount int
				}
				Count int
			}
			PoemsByType []struct {
				Type struct {
					Name      string
					PoemCount int
				}
				Count int
			}
		}
	}
	query := fmt.Sprintf(`query {
		author(id: "%d", lang: ZH_HANT) { name poemCount poems { totalCount edges { node { title } } } }
		authors(lang: ZH_HANT) { edges { node { name poemCount poems { totalCount } } } }
		poems(lang: ZH_HANT, authorId: "%d") { edges { node { title author { name poemCount } } } }
		statistics(lang: ZH_HANT) {
			poemsByDynasty { dynasty { name poemCount authorCount } count }
			poemsByType { type { name poemCount } count }
		}
	}`, relationID, relationID)
	require.NoError(t, c.Post(query, &resp))

	assert.Equal(t, "繁體正確作者", resp.Author.Name)
	assert.Equal(t, 1, resp.Author.PoemCount)
	assert.Equal(t, 1, resp.Author.Poems.TotalCount)
	require.Len(t, resp.Author.Poems.Edges, 1)
	assert.Equal(t, "繁體正確作品", resp.Author.Poems.Edges[0].Node.Title)
	require.Len(t, resp.Authors.Edges, 1)
	assert.Equal(t, "繁體正確作者", resp.Authors.Edges[0].Node.Name)
	assert.Equal(t, 1, resp.Authors.Edges[0].Node.PoemCount)
	assert.Equal(t, 1, resp.Authors.Edges[0].Node.Poems.TotalCount)
	require.Len(t, resp.Poems.Edges, 1)
	assert.Equal(t, "繁體正確作品", resp.Poems.Edges[0].Node.Title)
	assert.Equal(t, "繁體正確作者", resp.Poems.Edges[0].Node.Author.Name)
	assert.Equal(t, 1, resp.Poems.Edges[0].Node.Author.PoemCount)

	var dynastyFound, typeFound bool
	for _, stat := range resp.Statistics.PoemsByDynasty {
		if stat.Dynasty.Name == "繁朝" {
			dynastyFound = true
			assert.Equal(t, 1, stat.Count)
			assert.Equal(t, 1, stat.Dynasty.PoemCount)
			assert.Equal(t, 1, stat.Dynasty.AuthorCount)
		}
		assert.NotEqual(t, "简朝", stat.Dynasty.Name)
	}
	for _, stat := range resp.Statistics.PoemsByType {
		if stat.Type.Name == "繁體類" {
			typeFound = true
			assert.Equal(t, 1, stat.Count)
			assert.Equal(t, 1, stat.Type.PoemCount)
		}
		assert.NotEqual(t, "简体类", stat.Type.Name)
	}
	assert.True(t, dynastyFound)
	assert.True(t, typeFound)
}

func ptrInt64(value int64) *int64 { return &value }

func TestDynastiesQuery(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createTestData(t, repo)
	c := createTestClient(t, resolver)

	t.Run("get dynasties", func(t *testing.T) {
		var resp struct {
			Dynasties []struct {
				Name string
			}
		}

		err := c.Post(`query { dynasties { name } }`, &resp)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(resp.Dynasties), 1)
	})
}

func TestPoemTypesQuery(t *testing.T) {
	resolver, _ := setupTestResolver(t)

	// 体裁已由 Migrate 预置，无需手动写入

	c := createTestClient(t, resolver)

	t.Run("get poem types", func(t *testing.T) {
		var resp struct {
			PoemTypes []struct {
				Name     string
				Category string
			}
		}

		err := c.Post(`query { poemTypes { name category } }`, &resp)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(resp.PoemTypes), 1)
	})
}

func TestCollectionCountResolversUsePrefetchedStats(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	dynastyID, _, _ := createTestData(t, repo)
	c := createTestClient(t, resolver)

	var resp struct {
		Authors struct {
			Edges []struct{ Node struct{ PoemCount int } }
		}
		Dynasties []struct {
			ID          int64
			PoemCount   int
			AuthorCount int
		}
		PoemTypes []struct {
			ID        int64
			PoemCount int
		}
	}
	require.NoError(t, c.Post(`query {
		authors { edges { node { poemCount } } }
		dynasties { id poemCount authorCount }
		poemTypes { id poemCount }
	}`, &resp))

	require.NotEmpty(t, resp.Authors.Edges)
	assert.Equal(t, 1, resp.Authors.Edges[0].Node.PoemCount)
	for _, dynasty := range resp.Dynasties {
		if dynasty.ID == dynastyID {
			assert.Equal(t, 1, dynasty.PoemCount)
			assert.Equal(t, 1, dynasty.AuthorCount)
		}
	}
}

func TestCollectionCountsAreResolvedWithoutNPlusOneQueries(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	dynastyID, authorID, _ := createTestData(t, repo)
	typeID := int64(12)
	for i := 0; i < 12; i++ {
		require.NoError(t, repo.InsertPoem(&database.Poem{
			ID:        int64(9200 + i),
			Title:     fmt.Sprintf("批量计数测试%d", i),
			Content:   datatypes.JSON([]byte(`["批量计数正文"]`)),
			AuthorID:  &authorID,
			DynastyID: &dynastyID,
			TypeID:    &typeID,
		}))
	}
	for i := 0; i < 20; i++ {
		additionalAuthorID, err := repo.GetOrCreateAuthor(fmt.Sprintf("批量作者%d", i), dynastyID)
		require.NoError(t, err)
		require.NoError(t, repo.InsertPoem(&database.Poem{
			ID:        int64(9300 + i),
			Title:     fmt.Sprintf("批量作者作品%d", i),
			Content:   datatypes.JSON([]byte(`["批量作者正文"]`)),
			AuthorID:  &additionalAuthorID,
			DynastyID: &dynastyID,
			TypeID:    &typeID,
		}))
	}
	c := createTestClient(t, resolver)

	var queryCount int64
	callbackName := "test:count-collection-queries"
	require.NoError(t, resolver.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(*gorm.DB) {
		atomic.AddInt64(&queryCount, 1)
	}))
	t.Cleanup(func() { _ = resolver.DB.Callback().Query().Remove(callbackName) })

	var resp struct {
		Authors struct {
			Edges []struct {
				Node struct {
					PoemCount int
					Dynasty   struct {
						PoemCount   int
						AuthorCount int
					}
				}
			}
		}
		Dynasties []struct {
			PoemCount   int
			AuthorCount int
		}
		PoemTypes []struct{ PoemCount int }
	}
	require.NoError(t, c.Post(`query {
		authors(pageSize: 100) { edges { node { poemCount dynasty { poemCount authorCount } } } }
		dynasties { poemCount authorCount }
		poemTypes { poemCount }
	}`, &resp))

	assert.LessOrEqual(t, atomic.LoadInt64(&queryCount), int64(8),
		"collection sizes must not multiply count-query volume")
	require.Len(t, resp.Authors.Edges, 21)
	for _, edge := range resp.Authors.Edges {
		assert.Equal(t, 21, edge.Node.Dynasty.AuthorCount)
		assert.Equal(t, 33, edge.Node.Dynasty.PoemCount)
	}

	atomic.StoreInt64(&queryCount, 0)
	var poemResp struct {
		Poems struct {
			Edges []struct {
				Node struct {
					Author  struct{ PoemCount int }
					Dynasty struct {
						PoemCount   int
						AuthorCount int
					}
					Type struct{ PoemCount int }
				}
			}
		}
	}
	require.NoError(t, c.Post(`query { poems(pageSize: 20) { edges { node {
		author { poemCount }
		dynasty { poemCount authorCount }
		type { poemCount }
	} } } }`, &poemResp))
	assert.LessOrEqual(t, atomic.LoadInt64(&queryCount), int64(12),
		"poem relation counts must be prefetched in fixed query volume")
}

func TestStatisticsQuery(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createTestData(t, repo)
	c := createTestClient(t, resolver)

	t.Run("get statistics", func(t *testing.T) {
		var resp struct {
			Statistics struct {
				TotalPoems     int
				TotalAuthors   int
				TotalDynasties int
			}
		}

		err := c.Post(`query { statistics { totalPoems totalAuthors totalDynasties } }`, &resp)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, resp.Statistics.TotalPoems, 1)
		assert.GreaterOrEqual(t, resp.Statistics.TotalAuthors, 1)
	})
}

func TestRandomPoemQuery(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createTestData(t, repo)
	c := createTestClient(t, resolver)

	t.Run("get random poem", func(t *testing.T) {
		var resp struct {
			RandomPoem *struct {
				Title string
			}
		}

		err := c.Post(`query { randomPoem { title } }`, &resp)
		require.NoError(t, err)
		// 已有数据，应能返回一首诗
		assert.NotNil(t, resp.RandomPoem)
	})

	// 格式错误的 ID 曾被直接丢弃，过滤范围于是悄悄扩大到全量语料，
	// 随手返回一首无关的诗且不报任何错误。
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"malformed dynastyId is rejected", `query { randomPoem(dynastyId: "abc") { title } }`},
		{"malformed typeId is rejected", `query { randomPoem(typeId: "1.5") { title } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp struct{}
			err := c.Post(tc.query, &resp)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid syntax")
		})
	}
}

// 上下文传递的集成测试
func TestResolverWithContext(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createTestData(t, repo)

	ctx := context.Background()

	// 直接测试 poem resolver（lang 为 nil 时默认简体）
	poem, err := resolver.Query().Poem(ctx, "1", nil)
	require.NoError(t, err)
	assert.NotNil(t, poem)
	assert.Equal(t, "静夜思", poem.Title)
}

// createExtendedTestData 写入用于过滤条件测试的补充数据。
func createExtendedTestData(t *testing.T, resolver *Resolver, repo *database.Repository) (tangDynastyID, songDynastyID, libaiAuthorID, dumuAuthorID, typeID int64) {
	var err error

	// 写入唐朝
	tangDynastyID, err = repo.GetOrCreateDynasty("唐")
	require.NoError(t, err)

	// 写入宋朝
	songDynastyID, err = repo.GetOrCreateDynasty("宋")
	require.NoError(t, err)

	// 写入作者
	libaiAuthorID, err = repo.GetOrCreateAuthor("李白", tangDynastyID)
	require.NoError(t, err)

	dumuAuthorID, err = repo.GetOrCreateAuthor("杜牧", tangDynastyID)
	require.NoError(t, err)

	// 体裁由 Migrate 预置，这里直接使用「七言绝句」的既有 ID 12
	typeID = 12

	// 写入分属不同作者与体裁的诗词
	poems := []*database.Poem{
		{
			ID:        1001,
			Title:     "静夜思",
			Content:   datatypes.JSON([]byte(`["床前明月光","疑是地上霜"]`)),
			AuthorID:  &libaiAuthorID,
			DynastyID: &tangDynastyID,
			TypeID:    &typeID,
		},
		{
			ID:        1002,
			Title:     "将进酒",
			Content:   datatypes.JSON([]byte(`["君不见黄河之水天上来"]`)),
			AuthorID:  &libaiAuthorID,
			DynastyID: &tangDynastyID,
			TypeID:    &typeID,
		},
		{
			ID:        1003,
			Title:     "清明",
			Content:   datatypes.JSON([]byte(`["清明时节雨纷纷"]`)),
			AuthorID:  &dumuAuthorID,
			DynastyID: &tangDynastyID,
			TypeID:    &typeID,
		},
	}

	for _, poem := range poems {
		err = repo.InsertPoem(poem)
		require.NoError(t, err)
	}

	return tangDynastyID, songDynastyID, libaiAuthorID, dumuAuthorID, typeID
}

// TestPoemsWithFilters 测试 GraphQL poems 查询的 dynastyId、authorId、typeId 过滤。
func TestPoemsWithFilters(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	tangID, _, libaiID, _, typeID := createExtendedTestData(t, resolver, repo)
	c := createTestClient(t, resolver)

	t.Run("filter by dynastyId", func(t *testing.T) {
		var resp struct {
			Poems struct {
				Edges []struct {
					Node struct {
						Title string
					}
				}
				TotalCount int
			}
		}

		query := fmt.Sprintf(`query { poems(dynastyId: "%d") { edges { node { title } } totalCount } }`, tangID)
		err := c.Post(query, &resp)
		require.NoError(t, err)
		assert.Equal(t, 3, resp.Poems.TotalCount)
	})

	t.Run("filter by authorId", func(t *testing.T) {
		var resp struct {
			Poems struct {
				Edges []struct {
					Node struct {
						Title string
					}
				}
				TotalCount int
			}
		}

		query := fmt.Sprintf(`query { poems(authorId: "%d") { edges { node { title } } totalCount } }`, libaiID)
		err := c.Post(query, &resp)
		require.NoError(t, err)
		assert.Equal(t, 2, resp.Poems.TotalCount) // 李白有两首
	})

	t.Run("filter by typeId", func(t *testing.T) {
		var resp struct {
			Poems struct {
				Edges []struct {
					Node struct {
						Title string
					}
				}
				TotalCount int
			}
		}

		query := fmt.Sprintf(`query { poems(typeId: "%d") { edges { node { title } } totalCount } }`, typeID)
		err := c.Post(query, &resp)
		require.NoError(t, err)
		assert.Equal(t, 3, resp.Poems.TotalCount)
	})

	t.Run("filter with multiple conditions", func(t *testing.T) {
		var resp struct {
			Poems struct {
				TotalCount int
			}
		}

		query := fmt.Sprintf(`query { poems(dynastyId: "%d", authorId: "%d") { totalCount } }`, tangID, libaiID)
		err := c.Post(query, &resp)
		require.NoError(t, err)
		assert.Equal(t, 2, resp.Poems.TotalCount)
	})

	t.Run("filter with non-existent dynastyId returns empty", func(t *testing.T) {
		var resp struct {
			Poems struct {
				TotalCount int
			}
		}

		err := c.Post(`query { poems(dynastyId: "99999") { totalCount } }`, &resp)
		require.NoError(t, err)
		assert.Equal(t, 0, resp.Poems.TotalCount)
	})
}

// TestPaginationBoundaries 测试分页的各种边界情况。
func TestPaginationBoundaries(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	createExtendedTestData(t, resolver, repo)
	c := createTestClient(t, resolver)

	// 越界的分页参数一律报错而非截断，这样客户端传 pageSize: 500 时
	// 能明确知道自己并没有拿到 500 条。
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"page 0 is rejected", `query { poems(page: 0) { totalCount } }`, "page must be at least 1"},
		{"negative page is rejected", `query { poems(page: -1) { totalCount } }`, "page must be at least 1"},
		{"pageSize 0 is rejected", `query { poems(pageSize: 0) { totalCount } }`, "pageSize must be between"},
		{"pageSize above the cap is rejected", `query { poems(pageSize: 500) { totalCount } }`, "pageSize must be between"},
		// searchPoems 曾直接读取参数，完全没有做上限约束
		{"searchPoems pageSize above the cap is rejected", `query { searchPoems(query: "李太白", pageSize: 1000000) { totalCount } }`, "pageSize must be between"},
		{"authors pageSize above the cap is rejected", `query { authors(pageSize: 500) { totalCount } }`, "pageSize must be between"},
		{"deep page is rejected", `query { poems(page: 1001) { totalCount } }`, "page must be at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp struct{}
			err := c.Post(tc.query, &resp)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("pageSize at the cap is accepted", func(t *testing.T) {
		var resp struct {
			Poems struct {
				TotalCount int
			}
		}
		require.NoError(t, c.Post(`query { poems(pageSize: 100) { totalCount } }`, &resp))
	})

	t.Run("hasNextPage is true when more data exists", func(t *testing.T) {
		var resp struct {
			Poems struct {
				PageInfo struct {
					HasNextPage bool
				}
				TotalCount int
			}
		}

		err := c.Post(`query { poems(page: 1, pageSize: 2) { pageInfo { hasNextPage } totalCount } }`, &resp)
		require.NoError(t, err)
		if resp.Poems.TotalCount > 2 {
			assert.True(t, resp.Poems.PageInfo.HasNextPage)
		}
	})

	t.Run("hasPreviousPage is true on page 2", func(t *testing.T) {
		var resp struct {
			Poems struct {
				PageInfo struct {
					HasPreviousPage bool
				}
			}
		}

		err := c.Post(`query { poems(page: 2, pageSize: 1) { pageInfo { hasPreviousPage } } }`, &resp)
		require.NoError(t, err)
		assert.True(t, resp.Poems.PageInfo.HasPreviousPage)
	})
}

// TestAuthorsWithFilters 测试 GraphQL authors 查询的 dynastyId 过滤。
func TestAuthorsWithFilters(t *testing.T) {
	resolver, repo := setupTestResolver(t)
	tangID, songID, _, _, _ := createExtendedTestData(t, resolver, repo)
	c := createTestClient(t, resolver)

	t.Run("filter authors by dynastyId", func(t *testing.T) {
		var resp struct {
			Authors struct {
				Edges []struct {
					Node struct {
						Name string
					}
				}
				TotalCount int
			}
		}

		query := fmt.Sprintf(`query { authors(dynastyId: "%d") { edges { node { name } } totalCount } }`, tangID)
		err := c.Post(query, &resp)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, resp.Authors.TotalCount, 2) // 李白与杜牧
	})

	t.Run("filter authors by non-existent dynastyId", func(t *testing.T) {
		var resp struct {
			Authors struct {
				TotalCount int
			}
		}

		query := fmt.Sprintf(`query { authors(dynastyId: "%d") { totalCount } }`, songID)
		err := c.Post(query, &resp)
		require.NoError(t, err)
		// 测试数据中宋朝没有作者
		assert.Equal(t, 0, resp.Authors.TotalCount)
	})
}
