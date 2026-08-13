package database

import (
	"context"
	"crypto/rand"
	"math/big"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// 本文件包含 Repository 的诗词查询方法。

// literalLikePredicate 构造包含匹配，并在调用方输入含 SQL LIKE 元字符时
// 使用反斜杠逐字转义。普通输入不附加 ESCAPE 子句，以保留 SQLite FTS5
// trigram 对 LIKE 模式的索引优化路径。
func literalLikePredicate(column, value string) (predicate, pattern string) {
	pattern = "%" + value + "%"
	if !strings.ContainsAny(value, `%_\`) {
		return column + " LIKE ?", pattern
	}

	escaped := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	).Replace(value)
	return column + ` LIKE ? ESCAPE '\'`, "%" + escaped + "%"
}

// GetPoemByID 按 ID 查询单首诗词，并加载全部关联数据。
func (r *Repository) GetPoemByID(id string) (*Poem, error) {
	var poem Poem
	// 注意：表名是动态的（简繁两套表），GORM 的 Preload 无法正确处理，因此关联数据统一改为手动查询
	err := r.db.Table(r.poemsTable()).
		Where("id = ?", id).
		First(&poem).Error
	if err != nil {
		return nil, err
	}

	// 加载作者
	if poem.AuthorID != nil {
		var author Author
		if err := r.db.Table(r.authorsTable()).First(&author, *poem.AuthorID).Error; err == nil {
			poem.Author = &author
			// 加载作者所属朝代
			if author.DynastyID != nil {
				var dynasty Dynasty
				if err := r.db.Table(r.dynastiesTable()).First(&dynasty, *author.DynastyID).Error; err == nil {
					poem.Author.Dynasty = &dynasty
				}
			}
		}
	}

	// 加载诗词所属朝代
	if poem.DynastyID != nil {
		var dynasty Dynasty
		if err := r.db.Table(r.dynastiesTable()).First(&dynasty, *poem.DynastyID).Error; err == nil {
			poem.Dynasty = &dynasty
		}
	}

	// 加载体裁
	if poem.TypeID != nil {
		var ptype PoetryType
		if err := r.db.Table(r.poetryTypesTable()).First(&ptype, *poem.TypeID).Error; err == nil {
			poem.Type = &ptype
		}
	}

	return &poem, nil
}

// loadPoemRelations 为一批诗词批量加载作者、朝代与体裁，
// 通过先收集 ID 再按 IN 查询的方式避免 N+1 查询。
func (r *Repository) loadPoemRelations(poems []Poem) {
	if len(poems) == 0 {
		return
	}

	// 收集去重后的关联 ID
	authorIDs := make(map[int64]bool)
	dynastyIDs := make(map[int64]bool)
	typeIDs := make(map[int64]bool)

	for _, p := range poems {
		if p.AuthorID != nil {
			authorIDs[*p.AuthorID] = true
		}
		if p.DynastyID != nil {
			dynastyIDs[*p.DynastyID] = true
		}
		if p.TypeID != nil {
			typeIDs[*p.TypeID] = true
		}
	}

	// 批量加载作者
	authors := make(map[int64]*Author)
	if len(authorIDs) > 0 {
		ids := make([]int64, 0, len(authorIDs))
		for id := range authorIDs {
			ids = append(ids, id)
		}
		var authorList []Author
		r.db.Table(r.authorsTable()).Where("id IN ?", ids).Find(&authorList)
		for i := range authorList {
			authors[authorList[i].ID] = &authorList[i]
			// 作者的朝代也一并纳入待查集合
			if authorList[i].DynastyID != nil {
				dynastyIDs[*authorList[i].DynastyID] = true
			}
		}
	}

	// 批量加载朝代
	dynasties := make(map[int64]*Dynasty)
	if len(dynastyIDs) > 0 {
		ids := make([]int64, 0, len(dynastyIDs))
		for id := range dynastyIDs {
			ids = append(ids, id)
		}
		var dynastyList []Dynasty
		r.db.Table(r.dynastiesTable()).Where("id IN ?", ids).Find(&dynastyList)
		for i := range dynastyList {
			dynasties[dynastyList[i].ID] = &dynastyList[i]
		}
	}

	// 批量加载体裁
	types := make(map[int64]*PoetryType)
	if len(typeIDs) > 0 {
		ids := make([]int64, 0, len(typeIDs))
		for id := range typeIDs {
			ids = append(ids, id)
		}
		var typeList []PoetryType
		r.db.Table(r.poetryTypesTable()).Where("id IN ?", ids).Find(&typeList)
		for i := range typeList {
			types[typeList[i].ID] = &typeList[i]
		}
	}

	// 回填关联对象
	for i := range poems {
		if poems[i].AuthorID != nil {
			if author, ok := authors[*poems[i].AuthorID]; ok {
				poems[i].Author = author
				if author.DynastyID != nil {
					if d, ok := dynasties[*author.DynastyID]; ok {
						poems[i].Author.Dynasty = d
					}
				}
			}
		}
		if poems[i].DynastyID != nil {
			if dynasty, ok := dynasties[*poems[i].DynastyID]; ok {
				poems[i].Dynasty = dynasty
			}
		}
		if poems[i].TypeID != nil {
			if ptype, ok := types[*poems[i].TypeID]; ok {
				poems[i].Type = ptype
			}
		}
	}
}

// ListPoemsWithFilter 按可选条件分页查询诗词列表。
// 多个 typeID 之间是 OR 关系，与 GetRandomPoem 的行为保持一致。
//
// 结果固定按 canonical-derived id 升序排列。这个顺序不表示语料来源顺序或
// 新旧，只提供跨请求、跨语言和相同输入重建的一致分页顺序。
func (r *Repository) ListPoemsWithFilter(limit, offset int, dynastyID, authorID *int64, typeIDs []int64) ([]Poem, int, error) {
	query := r.db.Table(r.poemsTable())

	// 应用过滤条件
	if dynastyID != nil {
		query = query.Where("dynasty_id = ?", *dynastyID)
	}
	if authorID != nil {
		query = query.Where("author_id = ?", *authorID)
	}
	if len(typeIDs) > 0 {
		query = query.Where("type_id IN ?", typeIDs)
	}

	// 先取满足条件的总数
	var totalCount int64
	if err := query.Count(&totalCount).Error; err != nil {
		return nil, 0, err
	}

	// 再取当前分页数据
	var poems []Poem
	err := query.
		Limit(limit).Offset(offset).
		Order("id ASC").
		Find(&poems).Error
	if err != nil {
		return nil, 0, err
	}

	r.loadPoemRelations(poems)
	return poems, int(totalCount), nil
}

// GetRandomPoem 按可选条件随机返回一首诗词，多个体裁之间为 OR 关系。
// 采用「先 COUNT 再随机 OFFSET」的方式，保证结果在过滤集合内均匀分布。
func (r *Repository) GetRandomPoem(dynastyID, authorID *int64, typeIDs []int64) (*Poem, error) {
	poemTable := r.poemsTable()

	// 把过滤条件统一附加到查询上
	applyFilters := func(q *gorm.DB) *gorm.DB {
		if dynastyID != nil {
			q = q.Where("dynasty_id = ?", *dynastyID)
		}
		if authorID != nil {
			q = q.Where("author_id = ?", *authorID)
		}
		if len(typeIDs) > 0 {
			q = q.Where("type_id IN ?", typeIDs)
		}
		return q
	}

	// 统计命中数量
	var count int64
	if err := applyFilters(r.db.Table(poemTable)).Count(&count).Error; err != nil || count == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// 在 [0, count) 内取随机偏移
	randomBig, err := rand.Int(rand.Reader, big.NewInt(count))
	if err != nil {
		return nil, err
	}
	offset := int(randomBig.Int64())

	// 取该偏移位置上的诗词
	var poem Poem
	err = applyFilters(r.db.Table(poemTable)).Order("id ASC").Offset(offset).Limit(1).First(&poem).Error
	if err != nil {
		return nil, err
	}

	// 再按 ID 完整加载一次，补齐关联数据
	return r.GetPoemByID(strconv.FormatInt(poem.ID, 10))
}

// GetRandomPoemByChar 随机返回一首正文包含指定汉字的诗词（用于飞花令等玩法）。
// 与 GetRandomPoem 不同，此方法有意不支持叠加作者/体裁/朝代过滤：
// 它依赖 FTS 联表来定位候选，与其他方法使用的 id/dynasty/author/type 过滤属于
// 两种不同的查询形态，混用会让「除汉字外不接受其他过滤条件」这一 API 约定
// （由 handler 层强制）在不知不觉中被破坏。
// 同样采用「先 COUNT 再随机 OFFSET」保证均匀分布。
func (r *Repository) GetRandomPoemByChar(char string) (*Poem, error) {
	poemTable := r.poemsTable()
	ftsTable := r.poemsFtsTable()
	predicate, pattern := literalLikePredicate(ftsTable+".content_text", char)

	matches := func(q *gorm.DB) *gorm.DB {
		return q.Joins("JOIN "+ftsTable+" ON "+ftsTable+".rowid = "+poemTable+".id").
			Where(predicate, pattern)
	}

	// 统计命中数量
	var count int64
	if err := matches(r.db.Table(poemTable)).Count(&count).Error; err != nil || count == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// 在 [0, count) 内取随机偏移
	randomBig, err := rand.Int(rand.Reader, big.NewInt(count))
	if err != nil {
		return nil, err
	}
	offset := int(randomBig.Int64())

	// 取该偏移位置上的诗词
	var poem Poem
	err = matches(r.db.Table(poemTable)).
		Select(poemTable + ".*").
		Order(poemTable + ".id ASC").
		Offset(offset).Limit(1).First(&poem).Error
	if err != nil {
		return nil, err
	}

	// 再按 ID 完整加载一次，补齐关联数据
	return r.GetPoemByID(strconv.FormatInt(poem.ID, 10))
}

// ListAuthorPoems 分页查询指定作者的诗词。
func (r *Repository) ListAuthorPoems(authorID int64, limit, offset int) ([]Poem, int, error) {
	var totalCount int64
	if err := r.db.Table(r.poemsTable()).Where("author_id = ?", authorID).Count(&totalCount).Error; err != nil {
		return nil, 0, err
	}

	var poems []Poem
	err := r.db.Table(r.poemsTable()).
		Where("author_id = ?", authorID).
		Limit(limit).Offset(offset).
		Order("id ASC").
		Find(&poems).Error
	if err != nil {
		return nil, 0, err
	}

	r.loadPoemRelations(poems)
	return poems, int(totalCount), nil
}

// SearchPoems 基于建立在标题与正文上的 FTS5 trigram 索引搜索诗词，
// 索引的创建见 migrateFtsForLang。trigram 分词器使得 LIKE '%...%' 可以走 FTS 索引，
// 无需全表扫描 poems，同时保留至少 3 字符的子串匹配语义。API 层拒绝短查询
// 和 LIKE 语法字符，避免触发不能利用 trigram 索引的慢路径。
// searchType 可取："all"、"title"、"content"、"author"。
func (r *Repository) SearchPoems(ctx context.Context, query string, searchType string, page, pageSize int) ([]Poem, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bind every count, page query, and relation lookup below to the request
	// lifetime so an HTTP/GraphQL cancellation interrupts SQLite work.
	scoped := *r
	scoped.db = &DB{DB: r.db.WithContext(ctx)}
	r = &scoped
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}

	offset := (page - 1) * pageSize
	poemTable := r.poemsTable()
	authorTable := r.authorsTable()
	ftsTable := r.poemsFtsTable()
	ftsJoin := "JOIN " + ftsTable + " ON " + ftsTable + ".rowid = " + poemTable + ".id"
	titlePredicate, pattern := literalLikePredicate(ftsTable+".title", query)
	contentPredicate, _ := literalLikePredicate(ftsTable+".content_text", query)
	authorPredicate, _ := literalLikePredicate(authorTable+".name", query)

	var poems []Poem
	var total int64

	switch searchType {
	case "title":
		// 仅搜标题，走 FTS trigram 索引
		if err := r.db.Table(poemTable).
			Joins(ftsJoin).
			Where(titlePredicate, pattern).
			Count(&total).Error; err != nil {
			return nil, 0, err
		}
		err := r.db.Table(poemTable).
			Select(poemTable+".*").
			Joins(ftsJoin).
			Where(titlePredicate, pattern).
			Order(poemTable + ".id").
			Limit(pageSize).Offset(offset).
			Find(&poems).Error
		if err != nil {
			return nil, 0, err
		}

	case "content":
		// 仅搜正文，走 FTS trigram 索引
		if err := r.db.Table(poemTable).
			Joins(ftsJoin).
			Where(contentPredicate, pattern).
			Count(&total).Error; err != nil {
			return nil, 0, err
		}
		err := r.db.Table(poemTable).
			Select(poemTable+".*").
			Joins(ftsJoin).
			Where(contentPredicate, pattern).
			Order(poemTable + ".id").
			Limit(pageSize).Offset(offset).
			Find(&poems).Error
		if err != nil {
			return nil, 0, err
		}

	case "author":
		// 先在较小的作者表筛 ID，再经 poems.author_id 索引取作品；避免查询
		// 计划从全量诗词出发、逐行回表检查作者名。
		authorIDs := r.db.Table(authorTable).Select("id").Where(authorPredicate, pattern)
		if err := r.db.Table(poemTable).
			Where(poemTable+".author_id IN (?)", authorIDs).
			Count(&total).Error; err != nil {
			return nil, 0, err
		}
		err := r.db.Table(poemTable).
			Select(poemTable+".*").
			Where(poemTable+".author_id IN (?)", authorIDs).
			Order(poemTable + ".id").
			Limit(pageSize).Offset(offset).
			Find(&poems).Error
		if err != nil {
			return nil, 0, err
		}

	default: // "all"
		// 把三条候选路径分别求解后 UNION poem ID。这样 title/content 的
		// LIKE 能各自使用 trigram 虚表索引，不会被跨表 OR 迫使成全表扫描。
		matchJoin := "JOIN (" +
			"SELECT rowid AS poem_id FROM " + ftsTable + " WHERE " + titlePredicate +
			" UNION SELECT rowid AS poem_id FROM " + ftsTable + " WHERE " + contentPredicate +
			" UNION SELECT " + poemTable + ".id AS poem_id FROM " + poemTable +
			" WHERE " + poemTable + ".author_id IN (SELECT id FROM " + authorTable + " WHERE " + authorPredicate + ")" +
			") AS search_matches ON search_matches.poem_id = " + poemTable + ".id"
		if err := r.db.Table(poemTable).
			Joins(matchJoin, pattern, pattern, pattern).
			Count(&total).Error; err != nil {
			return nil, 0, err
		}
		err := r.db.Table(poemTable).
			Select(poemTable+".*").
			Joins(matchJoin, pattern, pattern, pattern).
			Order(poemTable + ".id").
			Limit(pageSize).Offset(offset).
			Find(&poems).Error
		if err != nil {
			return nil, 0, err
		}
	}

	r.loadPoemRelations(poems)
	return poems, total, nil
}
