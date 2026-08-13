package database

import (
	"errors"

	"gorm.io/gorm"
)

// ErrAmbiguousAuthor is returned when a display name maps to more than one
// dynasty-scoped author identity. Callers must add a dynasty or use author_id;
// silently selecting the first row would return another person's work.
var ErrAmbiguousAuthor = errors.New("author name is ambiguous across dynasties")

// 本文件包含供 REST API handler 使用的补充查询方法。

// GetAuthorsWithStats 返回作者列表及各自的作品数量。
func (r *Repository) GetAuthorsWithStats(limit, offset int) ([]AuthorWithStats, error) {
	authorTable := r.authorsTable()
	poemTable := r.poemsTable()
	dynastyTable := r.dynastiesTable()

	var authors []AuthorWithStats

	// 先按 author_id 聚合作品数，再联表分页。
	//
	// 排序里加上 id 是为了在 poem_count 相同时打破并列：
	// 否则大量作品数相同的作者之间顺序不确定，执行计划一变，
	// LIMIT/OFFSET 分页就会出现重复和遗漏。
	err := r.db.Table(authorTable).
		Select(authorTable + ".*, COUNT(" + poemTable + ".id) AS poem_count").
		Joins("LEFT JOIN " + poemTable + " ON " + authorTable + ".id = " + poemTable + ".author_id").
		Group(authorTable + ".id").
		Order("poem_count DESC, " + authorTable + ".id ASC").
		Limit(limit).
		Offset(offset).
		Find(&authors).Error
	if err != nil {
		return nil, err
	}

	// 为每位作者补上所属朝代
	dynastyIDs := make(map[int64]bool)
	for _, a := range authors {
		if a.DynastyID != nil {
			dynastyIDs[*a.DynastyID] = true
		}
	}

	if len(dynastyIDs) > 0 {
		ids := make([]int64, 0, len(dynastyIDs))
		for id := range dynastyIDs {
			ids = append(ids, id)
		}
		var dynasties []Dynasty
		r.db.Table(dynastyTable).Where("id IN ?", ids).Find(&dynasties)

		dynastyMap := make(map[int64]*Dynasty)
		for i := range dynasties {
			dynastyMap[dynasties[i].ID] = &dynasties[i]
		}

		for i := range authors {
			if authors[i].DynastyID != nil {
				if d, ok := dynastyMap[*authors[i].DynastyID]; ok {
					authors[i].Dynasty = d
				}
			}
		}
	}

	return authors, nil
}

// GetAuthorByID 按 ID 查询作者。
func (r *Repository) GetAuthorByID(id int64) (*Author, error) {
	var author Author
	err := r.db.Table(r.authorsTable()).First(&author, id).Error
	if err != nil {
		return nil, err
	}

	// 加载所属朝代
	if author.DynastyID != nil {
		var dynasty Dynasty
		if err := r.db.Table(r.dynastiesTable()).First(&dynasty, *author.DynastyID).Error; err == nil {
			author.Dynasty = &dynasty
		}
	}

	return &author, nil
}

// GetAuthorByName 按姓名查询作者。
func (r *Repository) GetAuthorByName(name string) (*Author, error) {
	return r.GetAuthorByNameAndDynasty(name, nil)
}

// GetAuthorByNameAndDynasty resolves the composite author identity. When no
// dynasty is supplied, a name remains backward-compatible only if it resolves
// to exactly one row.
func (r *Repository) GetAuthorByNameAndDynasty(name string, dynastyID *int64) (*Author, error) {
	query := r.db.Table(r.authorsTable()).Where("name = ?", name)
	if dynastyID != nil {
		query = query.Where("dynasty_id = ?", *dynastyID)
	}
	var authors []Author
	if err := query.Order("id ASC").Limit(2).Find(&authors).Error; err != nil {
		return nil, err
	}
	if len(authors) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(authors) > 1 {
		return nil, ErrAmbiguousAuthor
	}
	author := authors[0]

	// 加载所属朝代
	if author.DynastyID != nil {
		var dynasty Dynasty
		if err := r.db.Table(r.dynastiesTable()).First(&dynasty, *author.DynastyID).Error; err == nil {
			author.Dynasty = &dynasty
		}
	}

	return &author, nil
}

// GetPoemsByAuthor 查询指定作者的诗词。
func (r *Repository) GetPoemsByAuthor(authorID int64, limit, offset int) ([]Poem, error) {
	var poems []Poem
	err := r.db.Table(r.poemsTable()).
		Where("author_id = ?", authorID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&poems).Error
	if err != nil {
		return nil, err
	}

	r.loadPoemRelations(poems)
	return poems, nil
}

// GetDynastiesWithStats 返回朝代列表及各自的作品数与作者数。
func (r *Repository) GetDynastiesWithStats() ([]DynastyWithStats, error) {
	dynastyTable := r.dynastiesTable()
	poemTable := r.poemsTable()
	authorTable := r.authorsTable()

	var dynasties []DynastyWithStats

	// 数据量大时子查询比 JOIN 更快，故此处用子查询统计
	err := r.db.Table(dynastyTable).
		Select(dynastyTable + ".*, " +
			"(SELECT COUNT(*) FROM " + poemTable + " WHERE " + poemTable + ".dynasty_id = " + dynastyTable + ".id) as poem_count, " +
			"(SELECT COUNT(*) FROM " + authorTable + " WHERE " + authorTable + ".dynasty_id = " + dynastyTable + ".id) as author_count").
		Order("poem_count DESC, " + dynastyTable + ".id ASC").
		Find(&dynasties).Error

	return dynasties, err
}

// GetDynastyByID 按 ID 查询朝代。
func (r *Repository) GetDynastyByID(id int64) (*Dynasty, error) {
	var dynasty Dynasty
	err := r.db.Table(r.dynastiesTable()).First(&dynasty, id).Error
	return &dynasty, err
}

// GetDynastyByName 按名称查询朝代。
func (r *Repository) GetDynastyByName(name string) (*Dynasty, error) {
	var dynasty Dynasty
	err := r.db.Table(r.dynastiesTable()).Where("name = ?", name).First(&dynasty).Error
	return &dynasty, err
}

// GetPoemsByDynasty 查询指定朝代的诗词。
func (r *Repository) GetPoemsByDynasty(dynastyID int64, limit, offset int) ([]Poem, error) {
	var poems []Poem
	err := r.db.Table(r.poemsTable()).
		Where("dynasty_id = ?", dynastyID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&poems).Error
	if err != nil {
		return nil, err
	}

	r.loadPoemRelations(poems)
	return poems, nil
}

// GetPoetryTypesWithStats 返回体裁列表及各自的作品数量。
func (r *Repository) GetPoetryTypesWithStats() ([]PoetryTypeWithStats, error) {
	typeTable := r.poetryTypesTable()
	poemTable := r.poemsTable()

	var types []PoetryTypeWithStats

	// 数据量大时子查询比 JOIN 更快
	err := r.db.Table(typeTable).
		Select(typeTable + ".*, (SELECT COUNT(*) FROM " + poemTable + " WHERE " + poemTable + ".type_id = " + typeTable + ".id) as poem_count").
		Order("poem_count DESC, " + typeTable + ".id ASC").
		Find(&types).Error

	return types, err
}

// GetPoetryTypeByID 按 ID 查询体裁。
func (r *Repository) GetPoetryTypeByID(id int64) (*PoetryType, error) {
	var poetryType PoetryType
	err := r.db.Table(r.poetryTypesTable()).First(&poetryType, id).Error
	return &poetryType, err
}

// GetPoemsByType 查询指定体裁的诗词。
func (r *Repository) GetPoemsByType(typeID int64, limit, offset int) ([]Poem, error) {
	var poems []Poem
	err := r.db.Table(r.poemsTable()).
		Where("type_id = ?", typeID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&poems).Error
	if err != nil {
		return nil, err
	}

	r.loadPoemRelations(poems)
	return poems, nil
}
