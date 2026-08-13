package processor

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/loader"
)

const (
	canonicalInputDomain = "chinese-poetry-api/canonical-poem/v2"
)

// canonicalIdentity hashes the normalized work identity after every input field
// has been converted to the pipeline's fixed simplified canonical script.
// The encoded fields are, in order: domain/version, dynasty, author, final title,
// paragraph count, then every paragraph. Every string is uint64 length-prefixed;
// the paragraph count is encoded as uint64. Dataset and source are deliberately
// excluded so normalized logical duplicates can retain multiple source witnesses.
func canonicalIdentity(dynasty, author, finalTitle string, paragraphs []string) (string, string) {
	encoded := make([]byte, 0, len(dynasty)+len(author)+len(finalTitle)+128)
	encoded = appendIdentityString(encoded, canonicalInputDomain)
	encoded = appendIdentityString(encoded, dynasty)
	encoded = appendIdentityString(encoded, author)
	encoded = appendIdentityString(encoded, finalTitle)
	encoded = appendIdentityUint64(encoded, uint64(len(paragraphs)))
	for _, paragraph := range paragraphs {
		encoded = appendIdentityString(encoded, paragraph)
	}

	primary := sha256.Sum256(encoded)
	fingerprint := sha512.Sum512(encoded)
	return database.CanonicalIDPrefix + hex.EncodeToString(primary[:]), hex.EncodeToString(fingerprint[:])
}

// stablePoemID derives the API-facing integer ID from the canonical identity,
// so inserting, deleting, or reordering unrelated source records cannot renumber
// existing poems. REST serializes this value as a JSON number, so keep it within
// JavaScript's exact integer range (53 bits) and reserve zero. The repository
// rejects any collision between two different canonical identities and rolls
// the transaction back; a release therefore cannot silently alias two poems.
func stablePoemID(canonicalID string) (int64, error) {
	return database.PoemIDFromCanonical(canonicalID)
}

func sourceWitness(meta loader.PoemWithMeta) (database.PoemSource, error) {
	return database.NewPoemSource(
		meta.SourceID, meta.DatasetKey, meta.SourcePath, meta.SourceRecordIndex,
	)
}

func appendIdentityString(dst []byte, value string) []byte {
	dst = appendIdentityUint64(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendIdentityUint64(dst []byte, value uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	return append(dst, buf[:]...)
}
