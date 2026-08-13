// Package searchquery owns the public search-input contract shared by REST and
// GraphQL. Keeping normalization here prevents the two endpoints from applying
// different trimming and Unicode length rules.
package searchquery

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxRunes = 100
)

var (
	ErrEmpty       = errors.New("search query must not be empty")
	ErrInvalidUTF8 = errors.New("search query must be valid UTF-8")
	ErrLikeSyntax  = errors.New("indexed search does not accept LIKE wildcard characters: %, _, or backslash")
)

// Normalize trims surrounding Unicode whitespace and enforces a bounded,
// non-empty UTF-8 query. The bound is in runes rather than bytes so Chinese
// queries receive the same character budget as ASCII queries.
func Normalize(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidUTF8
	}

	query := strings.TrimSpace(raw)
	if query == "" {
		return "", ErrEmpty
	}
	runeCount := utf8.RuneCountInString(query)
	if runeCount > MaxRunes {
		return "", fmt.Errorf("search query must be at most %d characters, got %d", MaxRunes, runeCount)
	}

	return query, nil
}

// ValidateIndexedLength enforces the FTS5 trigram index floor. Author-only
// search intentionally does not call this function because it scans the much
// smaller author table and remains protected by the global request limiter.
func ValidateIndexedLength(query string) error {
	const minIndexedRunes = 3
	if runeCount := utf8.RuneCountInString(query); runeCount < minIndexedRunes {
		return fmt.Errorf("search query must be at least %d characters for indexed title/content search; use type=author for a short author name", minIndexedRunes)
	}
	// SQLite's FTS5 trigram LIKE optimization is not guaranteed for LIKE
	// expressions with an ESCAPE clause. Reject the three syntax characters at
	// the public boundary instead of exposing an attacker-controlled full scan.
	if strings.ContainsAny(query, `%_\`) {
		return ErrLikeSyntax
	}
	return nil
}

// ValidateLiteralSubstring rejects LIKE syntax characters for author search as
// well. Authors may use short names, but wildcard-looking input must never
// change the query grammar or depend on SQL ESCAPE dialect quirks.
func ValidateLiteralSubstring(query string) error {
	if strings.ContainsAny(query, `%_\`) {
		return ErrLikeSyntax
	}
	return nil
}
