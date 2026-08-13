package graph

import (
	"context"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
)

// prefetchPoemRelationCounts collapses the count resolvers reachable from a
// Poem collection into at most three GROUP BY queries, independent of page
// size. Without this, requesting author/dynasty/type count fields under every
// poem would issue one SQL query per object.
func (r *Resolver) prefetchPoemRelationCounts(ctx context.Context, lang database.Lang, poems []database.Poem) error {
	authorIDs := make(map[int64]struct{})
	dynastyIDs := make(map[int64]struct{})
	typeIDs := make(map[int64]struct{})
	for i := range poems {
		if poems[i].AuthorID != nil {
			authorIDs[*poems[i].AuthorID] = struct{}{}
		}
		if poems[i].DynastyID != nil {
			dynastyIDs[*poems[i].DynastyID] = struct{}{}
		}
		if poems[i].TypeID != nil {
			typeIDs[*poems[i].TypeID] = struct{}{}
		}
	}

	poemTable := database.PoemsTable(lang)
	if len(authorIDs) > 0 {
		var rows []struct {
			ID    int64 `gorm:"column:id"`
			Count int   `gorm:"column:count"`
		}
		if err := r.DB.Table(poemTable).
			Select("author_id AS id, COUNT(*) AS count").
			Where("author_id IN ?", mapKeys(authorIDs)).
			Group("author_id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			cacheAuthorCount(ctx, lang, row.ID, row.Count)
		}
	}

	if len(dynastyIDs) > 0 {
		var rows []struct {
			ID          int64 `gorm:"column:id"`
			PoemCount   int   `gorm:"column:poem_count"`
			AuthorCount int   `gorm:"column:author_count"`
		}
		if err := r.DB.Table(poemTable).
			Select("dynasty_id AS id, COUNT(*) AS poem_count, COUNT(DISTINCT author_id) AS author_count").
			Where("dynasty_id IN ?", mapKeys(dynastyIDs)).
			Group("dynasty_id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			cacheDynastyStats(ctx, lang, row.ID, row.PoemCount, row.AuthorCount)
		}
	}

	if len(typeIDs) > 0 {
		var rows []struct {
			ID    int64 `gorm:"column:id"`
			Count int   `gorm:"column:count"`
		}
		if err := r.DB.Table(poemTable).
			Select("type_id AS id, COUNT(*) AS count").
			Where("type_id IN ?", mapKeys(typeIDs)).
			Group("type_id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			cacheTypeCount(ctx, lang, row.ID, row.Count)
		}
	}

	return nil
}

// prefetchAuthorDynastyStats collapses the dynasty count fields reachable from
// an Author collection into one bounded query. ListAuthorsWithFilter already
// loads the related Dynasty records in one query; without this companion
// prefetch, asking for dynasty.poemCount or dynasty.authorCount would still
// execute the two fallback count resolvers once per author.
func (r *Resolver) prefetchAuthorDynastyStats(ctx context.Context, lang database.Lang, authors []database.AuthorWithStats) error {
	dynastyIDs := make(map[int64]struct{})
	for i := range authors {
		if authors[i].DynastyID != nil {
			dynastyIDs[*authors[i].DynastyID] = struct{}{}
		}
	}
	if len(dynastyIDs) == 0 {
		return nil
	}

	dynastyTable := database.DynastiesTable(lang)
	poemTable := database.PoemsTable(lang)
	authorTable := database.AuthorsTable(lang)
	var rows []struct {
		ID          int64 `gorm:"column:id"`
		PoemCount   int   `gorm:"column:poem_count"`
		AuthorCount int   `gorm:"column:author_count"`
	}
	selectClause := dynastyTable + ".id AS id, " +
		"(SELECT COUNT(*) FROM " + poemTable + " WHERE " + poemTable + ".dynasty_id = " + dynastyTable + ".id) AS poem_count, " +
		"(SELECT COUNT(*) FROM " + authorTable + " WHERE " + authorTable + ".dynasty_id = " + dynastyTable + ".id) AS author_count"
	if err := r.DB.Table(dynastyTable).
		Select(selectClause).
		Where(dynastyTable+".id IN ?", mapKeys(dynastyIDs)).
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		cacheDynastyStats(ctx, lang, row.ID, row.PoemCount, row.AuthorCount)
	}
	return nil
}

func mapKeys(values map[int64]struct{}) []int64 {
	keys := make([]int64, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	return keys
}

func mapKeysForCounts(values map[int64]int) []int64 {
	keys := make([]int64, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	return keys
}
