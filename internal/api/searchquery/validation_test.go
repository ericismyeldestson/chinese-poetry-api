package searchquery

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	t.Run("trims Unicode whitespace", func(t *testing.T) {
		query, err := Normalize(" \t静夜思\u3000")
		require.NoError(t, err)
		assert.Equal(t, "静夜思", query)
	})

	t.Run("rejects whitespace only", func(t *testing.T) {
		_, err := Normalize(" \t\u3000")
		assert.ErrorIs(t, err, ErrEmpty)
	})

	t.Run("indexed validation rejects queries below the trigram index floor", func(t *testing.T) {
		for _, raw := range []string{"诗", "诗词"} {
			query, err := Normalize(raw)
			require.NoError(t, err)
			err = ValidateIndexedLength(query)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "at least 3 characters")
		}
		assert.NoError(t, ValidateIndexedLength("李太白"))
	})

	t.Run("indexed validation rejects LIKE syntax slow paths", func(t *testing.T) {
		for _, query := range []string{"%%%", "___", `诗词\\`, "明月%"} {
			assert.ErrorIs(t, ValidateIndexedLength(query), ErrLikeSyntax)
		}
		assert.NoError(t, ValidateIndexedLength("明月光"))
	})

	t.Run("author substring validation permits short names but rejects LIKE syntax", func(t *testing.T) {
		assert.NoError(t, ValidateLiteralSubstring("李白"))
		for _, query := range []string{"%", "_", `\`, "李%"} {
			assert.ErrorIs(t, ValidateLiteralSubstring(query), ErrLikeSyntax)
		}
	})

	t.Run("counts Unicode code points", func(t *testing.T) {
		query, err := Normalize(strings.Repeat("诗", MaxRunes))
		require.NoError(t, err)
		assert.Len(t, []rune(query), MaxRunes)

		_, err = Normalize(strings.Repeat("诗", MaxRunes+1))
		assert.Error(t, err)
	})

	t.Run("rejects invalid UTF-8", func(t *testing.T) {
		_, err := Normalize(string([]byte{0xff}))
		assert.ErrorIs(t, err, ErrInvalidUTF8)
	})
}
