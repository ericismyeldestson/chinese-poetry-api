package database

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalAuthorIdentityGoldenVector(t *testing.T) {
	canonicalID, err := NewCanonicalAuthorID("唐", "李白")
	require.NoError(t, err)
	assert.Equal(t,
		"cpa:author:v1:sha256:fda25378e8f06037d42bd9983c055cbc26cf4a46bed8b821fa9f0293af92966f",
		canonicalID,
	)

	publicID, err := AuthorIDFromCanonical(canonicalID)
	require.NoError(t, err)
	assert.Equal(t, int64(654728722669623), publicID)
	assert.Positive(t, publicID)
	assert.LessOrEqual(t, publicID, int64((1<<53)-1))
}

func TestCanonicalAuthorIdentityIsSharedByHansAndHantProducts(t *testing.T) {
	db, hans := setupGetPoetryTypeIDsTestDB(t)
	hant := hans.WithLang(LangHant)

	var hansDynasty, hantDynasty Dynasty
	require.NoError(t, db.Table(DynastiesTable(LangHans)).Where("name = ?", "唐").First(&hansDynasty).Error)
	require.NoError(t, db.Table(DynastiesTable(LangHant)).Where("name = ?", "唐").First(&hantDynasty).Error)
	require.Equal(t, hansDynasty.ID, hantDynasty.ID)

	canonicalID, err := NewCanonicalAuthorID("唐", "张说")
	require.NoError(t, err)
	hansID, err := hans.GetOrCreateCanonicalAuthor(canonicalID, "张说", hansDynasty.ID)
	require.NoError(t, err)
	hantID, err := hant.GetOrCreateCanonicalAuthor(canonicalID, "張說", hantDynasty.ID)
	require.NoError(t, err)
	assert.Equal(t, hansID, hantID)

	for _, fixture := range []struct {
		table string
		name  string
	}{
		{AuthorsTable(LangHans), "张说"},
		{AuthorsTable(LangHant), "張說"},
	} {
		var stored Author
		require.NoError(t, db.Table(fixture.table).Where("id = ?", hansID).First(&stored).Error)
		assert.Equal(t, canonicalID, stored.CanonicalID)
		assert.Equal(t, fixture.name, stored.Name)
		assert.NotNil(t, stored.DynastyID)
		assert.Equal(t, hansDynasty.ID, *stored.DynastyID)
	}
}

func TestCanonicalAuthorWriteRejectsTruncatedPublicIDCollision(t *testing.T) {
	_, repo := setupGetPoetryTypeIDsTestDB(t)
	dynastyID, err := repo.GetOrCreateDynasty("唐")
	require.NoError(t, err)

	canonicalA := CanonicalAuthorIDPrefix + "0000000000000001" + strings.Repeat("0", 48)
	canonicalB := CanonicalAuthorIDPrefix + "0000000000000001" + strings.Repeat("0", 47) + "1"
	idA, err := AuthorIDFromCanonical(canonicalA)
	require.NoError(t, err)
	idB, err := AuthorIDFromCanonical(canonicalB)
	require.NoError(t, err)
	require.Equal(t, idA, idB)

	_, err = repo.GetOrCreateCanonicalAuthor(canonicalA, "甲", dynastyID)
	require.NoError(t, err)
	_, err = repo.GetOrCreateCanonicalAuthor(canonicalB, "乙", dynastyID)
	require.ErrorContains(t, err, "possible numeric ID conflict")

	var authors int64
	require.NoError(t, repo.db.Table(repo.authorsTable()).Count(&authors).Error)
	assert.Equal(t, int64(1), authors)
}

func TestCanonicalAuthorWriteRejectsProductDisagreement(t *testing.T) {
	_, repo := setupGetPoetryTypeIDsTestDB(t)
	dynastyID, err := repo.GetOrCreateDynasty("唐")
	require.NoError(t, err)
	canonicalID, err := NewCanonicalAuthorID("唐", "张说")
	require.NoError(t, err)

	_, err = repo.GetOrCreateCanonicalAuthor(canonicalID, "张说", dynastyID)
	require.NoError(t, err)
	_, err = repo.GetOrCreateCanonicalAuthor(canonicalID, "不同显示名", dynastyID)
	require.ErrorContains(t, err, "canonical author conflict")
}
