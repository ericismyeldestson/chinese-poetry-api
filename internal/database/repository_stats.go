package database

// 本文件包含各类统计与计数方法。

// CountPoems 返回诗词总数。
func (r *Repository) CountPoems() (int, error) {
	var count int64
	err := r.db.Table(r.poemsTable()).Count(&count).Error
	return int(count), err
}

// CountAuthors 返回作者总数。
func (r *Repository) CountAuthors() (int, error) {
	var count int64
	err := r.db.Table(r.authorsTable()).Count(&count).Error
	return int(count), err
}

// CountPoemsByAuthor 返回某位作者名下的作品数。
func (r *Repository) CountPoemsByAuthor(authorID int64) (int, error) {
	return r.countPoemsWhere("author_id = ?", authorID)
}

// CountPoemsByDynasty 返回某个朝代的作品数。
func (r *Repository) CountPoemsByDynasty(dynastyID int64) (int, error) {
	return r.countPoemsWhere("dynasty_id = ?", dynastyID)
}

// CountPoemsByType 返回某种体裁的作品数。
func (r *Repository) CountPoemsByType(typeID int64) (int, error) {
	return r.countPoemsWhere("type_id = ?", typeID)
}

// CountAuthorsByDynasty 返回某朝代下至少有一首作品的作者数（去重）。
func (r *Repository) CountAuthorsByDynasty(dynastyID int64) (int, error) {
	var count int64
	err := r.db.Table(r.poemsTable()).
		Where("dynasty_id = ?", dynastyID).
		Distinct("author_id").
		Count(&count).Error
	return int(count), err
}

// countPoemsWhere 按单个条件统计诗词数量。
// 它的存在是为了让这类计数和其他查询一样统一走 poemsTable()：
// GraphQL resolver 早先用 db.Model(&Poem{}) 计数，会解析到 Poem.TableName()，
// 也就是没有语言后缀的旧表名 "poems"；简繁分表之后该表已不存在，
// 导致这些字段在运行时全部报错。
func (r *Repository) countPoemsWhere(query string, args ...any) (int, error) {
	var count int64
	err := r.db.Table(r.poemsTable()).Where(query, args...).Count(&count).Error
	return int(count), err
}

// GetStatistics 返回全库的整体统计数据。
func (r *Repository) GetStatistics() (*Statistics, error) {
	stats := &Statistics{}

	// 各项总数
	var err error
	stats.TotalPoems, err = r.CountPoems()
	if err != nil {
		return nil, err
	}

	stats.TotalAuthors, err = r.CountAuthors()
	if err != nil {
		return nil, err
	}

	var count int64
	err = r.db.Table(r.dynastiesTable()).Where("name != ?", "其他").Count(&count).Error
	if err != nil {
		return nil, err
	}
	stats.TotalDynasties = int(count)

	// 按朝代统计作品数。表名是动态的，故手写 SQL 片段
	dynastyTable := r.dynastiesTable()
	poemTable := r.poemsTable()

	var dynastyStats []struct {
		Dynasty
		PoemCount int `gorm:"column:poem_count"`
	}

	err = r.db.Table(dynastyTable).
		Select(dynastyTable + ".*, COUNT(" + poemTable + ".id) as poem_count").
		Joins("LEFT JOIN " + poemTable + " ON " + dynastyTable + ".id = " + poemTable + ".dynasty_id").
		Group(dynastyTable + ".id").
		Order("poem_count DESC, " + dynastyTable + ".id ASC").
		Scan(&dynastyStats).Error
	if err != nil {
		return nil, err
	}

	for _, ds := range dynastyStats {
		stats.PoemsByDynasty = append(stats.PoemsByDynasty, DynastyWithStats{
			Dynasty:   ds.Dynasty,
			PoemCount: ds.PoemCount,
		})
	}

	// 按体裁统计作品数
	typeTable := r.poetryTypesTable()

	var typeStats []struct {
		PoetryType
		PoemCount int `gorm:"column:poem_count"`
	}

	err = r.db.Table(typeTable).
		Select(typeTable + ".*, COUNT(" + poemTable + ".id) as poem_count").
		Joins("LEFT JOIN " + poemTable + " ON " + typeTable + ".id = " + poemTable + ".type_id").
		Group(typeTable + ".id").
		Order("poem_count DESC, " + typeTable + ".id ASC").
		Scan(&typeStats).Error
	if err != nil {
		return nil, err
	}

	for _, ts := range typeStats {
		stats.PoemsByType = append(stats.PoemsByType, PoetryTypeWithStats{
			PoetryType: ts.PoetryType,
			PoemCount:  ts.PoemCount,
		})
	}

	return stats, nil
}

// ListAuthorsWithFilter 分页查询作者列表，可按朝代过滤。
func (r *Repository) ListAuthorsWithFilter(limit, offset int, dynastyID *int64) ([]AuthorWithStats, int, error) {
	authorTable := r.authorsTable()
	poemTable := r.poemsTable()
	dynastyTable := r.dynastiesTable()

	query := r.db.Table(authorTable)

	// 应用朝代过滤
	if dynastyID != nil {
		query = query.Where(authorTable+".dynasty_id = ?", *dynastyID)
	}

	// 先取满足条件的总数
	var totalCount int64
	if err := query.Count(&totalCount).Error; err != nil {
		return nil, 0, err
	}

	// 再取作者及其作品数
	var results []struct {
		Author
		PoemCount int `gorm:"column:poem_count"`
	}

	err := query.
		Select(authorTable + ".*, COUNT(" + poemTable + ".id) as poem_count").
		Joins("LEFT JOIN " + poemTable + " ON " + authorTable + ".id = " + poemTable + ".author_id").
		Group(authorTable + ".id").
		Order("poem_count DESC, " + authorTable + ".id ASC").
		Limit(limit).Offset(offset).
		Scan(&results).Error
	if err != nil {
		return nil, 0, err
	}

	// 转换为对外的 AuthorWithStats
	authors := make([]AuthorWithStats, len(results))
	dynastyIDs := make([]int64, 0, len(results))
	seenDynasties := make(map[int64]struct{}, len(results))
	for i, r := range results {
		authors[i] = AuthorWithStats{
			Author:    r.Author,
			PoemCount: r.PoemCount,
		}
		if r.DynastyID != nil {
			if _, exists := seenDynasties[*r.DynastyID]; !exists {
				seenDynasties[*r.DynastyID] = struct{}{}
				dynastyIDs = append(dynastyIDs, *r.DynastyID)
			}
		}
	}

	// Populate dynasty in one bounded query so GraphQL collection fields do not
	// silently return null or regress into an author-count N+1 pattern.
	if len(dynastyIDs) > 0 {
		var dynasties []Dynasty
		if err := r.db.Table(dynastyTable).Where("id IN ?", dynastyIDs).Find(&dynasties).Error; err != nil {
			return nil, 0, err
		}
		byID := make(map[int64]*Dynasty, len(dynasties))
		for i := range dynasties {
			byID[dynasties[i].ID] = &dynasties[i]
		}
		for i := range authors {
			if authors[i].DynastyID != nil {
				authors[i].Dynasty = byID[*authors[i].DynastyID]
			}
		}
	}

	return authors, int(totalCount), nil
}
