package database

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
)

const (
	// CanonicalIDPrefix versions the poem identity algorithm. The hash input is
	// documented and generated in processor/identity.go after conversion to the
	// fixed simplified canonical script. v2 intentionally does not accept v1 IDs,
	// whose pre-conversion input left simplified/traditional source duplicates as
	// separate API products.
	CanonicalIDPrefix = "cpa:poem:v2:sha256:"
	// CanonicalAuthorIDPrefix identifies a dynasty-scoped author entity in the
	// fixed simplified canonical script. Both localized tables use this identity.
	CanonicalAuthorIDPrefix = "cpa:author:v1:sha256:"
	// SourceLocatorPrefix versions the source witness identity algorithm.
	SourceLocatorPrefix = "cpa:source:v1:sha256:"
	sourceInputDomain   = "chinese-poetry-api/source-locator/v1"
	authorInputDomain   = "chinese-poetry-api/canonical-author/v1"
)

var datasetKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// NewPoemSource validates a source locator and constructs its stable identity.
func NewPoemSource(sourceID, datasetKey, sourcePath string, recordIndex int) (PoemSource, error) {
	if !datasetKeyPattern.MatchString(datasetKey) {
		return PoemSource{}, fmt.Errorf("invalid dataset key %q", datasetKey)
	}
	if recordIndex < 0 {
		return PoemSource{}, fmt.Errorf("invalid source record index %d", recordIndex)
	}
	if err := validateSourcePath(sourcePath); err != nil {
		return PoemSource{}, err
	}

	encoded := make([]byte, 0, len(datasetKey)+len(sourcePath)+len(sourceID)+96)
	encoded = appendGovernanceString(encoded, sourceInputDomain)
	encoded = appendGovernanceString(encoded, datasetKey)
	encoded = appendGovernanceString(encoded, sourcePath)
	encoded = appendGovernanceUint64(encoded, uint64(recordIndex))
	encoded = appendGovernanceString(encoded, sourceID)
	digest := sha256.Sum256(encoded)

	return PoemSource{
		LocatorID:         SourceLocatorPrefix + hex.EncodeToString(digest[:]),
		SourceID:          sourceID,
		DatasetKey:        datasetKey,
		SourcePath:        sourcePath,
		SourceRecordIndex: recordIndex,
	}, nil
}

// NewSourceRejection validates and identifies a rejected source occurrence.
func NewSourceRejection(sourceID, datasetKey, sourcePath string, recordIndex int, stage, reason string) (SourceRejection, error) {
	witness, err := NewPoemSource(sourceID, datasetKey, sourcePath, recordIndex)
	if err != nil {
		return SourceRejection{}, err
	}
	if stage == "" || strings.TrimSpace(stage) != stage {
		return SourceRejection{}, fmt.Errorf("invalid rejection stage %q", stage)
	}
	if reason == "" || strings.TrimSpace(reason) != reason {
		return SourceRejection{}, fmt.Errorf("invalid rejection reason %q", reason)
	}
	return SourceRejection{
		LocatorID:         witness.LocatorID,
		SourceID:          sourceID,
		DatasetKey:        datasetKey,
		SourcePath:        sourcePath,
		SourceRecordIndex: recordIndex,
		Stage:             stage,
		Reason:            reason,
	}, nil
}

func validatePoemSource(source PoemSource) error {
	expected, err := NewPoemSource(
		source.SourceID, source.DatasetKey, source.SourcePath, source.SourceRecordIndex,
	)
	if err != nil {
		return err
	}
	if source.LocatorID != expected.LocatorID {
		return fmt.Errorf("source locator %q does not match its fields", source.LocatorID)
	}
	return nil
}

func validateCanonicalIdentity(id, fingerprint string) error {
	if !strings.HasPrefix(id, CanonicalIDPrefix) {
		return fmt.Errorf("canonical ID %q does not use %s", id, CanonicalIDPrefix)
	}
	hexID := strings.TrimPrefix(id, CanonicalIDPrefix)
	if len(hexID) != sha256.Size*2 {
		return fmt.Errorf("canonical ID %q has an invalid digest length", id)
	}
	if _, err := hex.DecodeString(hexID); err != nil {
		return fmt.Errorf("canonical ID %q has an invalid digest: %w", id, err)
	}
	if len(fingerprint) != 128 {
		return fmt.Errorf("canonical fingerprint for %q has an invalid length", id)
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return fmt.Errorf("canonical fingerprint for %q is invalid: %w", id, err)
	}
	return nil
}

// PoemIDFromCanonical derives the API-facing, JavaScript-safe integer ID from
// a validated canonical SHA-256 identity. Keeping this next to repository
// validation prevents callers from persisting an arbitrary numeric ID that
// disagrees with the versioned identity contract.
func PoemIDFromCanonical(id string) (int64, error) {
	return safeIntegerIDFromCanonical(id, CanonicalIDPrefix)
}

// NewCanonicalAuthorID hashes the canonical simplified dynasty and name using
// length-prefixed fields and a domain separated from poem/source identities.
func NewCanonicalAuthorID(dynasty, name string) (string, error) {
	if strings.TrimSpace(dynasty) == "" || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("canonical author dynasty and name must not be empty")
	}
	encoded := make([]byte, 0, len(dynasty)+len(name)+64)
	encoded = appendGovernanceString(encoded, authorInputDomain)
	encoded = appendGovernanceString(encoded, dynasty)
	encoded = appendGovernanceString(encoded, name)
	digest := sha256.Sum256(encoded)
	return CanonicalAuthorIDPrefix + hex.EncodeToString(digest[:]), nil
}

// AuthorIDFromCanonical derives a shared Hans/Hant JavaScript-safe author ID.
func AuthorIDFromCanonical(id string) (int64, error) {
	return safeIntegerIDFromCanonical(id, CanonicalAuthorIDPrefix)
}

func safeIntegerIDFromCanonical(id, prefix string) (int64, error) {
	if !strings.HasPrefix(id, prefix) {
		return 0, fmt.Errorf("canonical ID %q does not use %s", id, prefix)
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	if err != nil || len(digest) != sha256.Size {
		return 0, fmt.Errorf("canonical ID %q has an invalid SHA-256 digest", id)
	}
	poemID := int64(binary.BigEndian.Uint64(digest[:8]) & ((1 << 53) - 1))
	if poemID == 0 {
		poemID = 1
	}
	return poemID, nil
}

func validateSourcePath(sourcePath string) error {
	if sourcePath == "" || strings.TrimSpace(sourcePath) != sourcePath {
		return fmt.Errorf("invalid source path %q", sourcePath)
	}
	if strings.Contains(sourcePath, `\`) || strings.HasPrefix(sourcePath, "/") {
		return fmt.Errorf("source path must be relative and slash-normalized: %q", sourcePath)
	}
	if clean := path.Clean(sourcePath); clean != sourcePath || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("source path must stay inside the data root: %q", sourcePath)
	}
	if path.Ext(sourcePath) != ".json" {
		return fmt.Errorf("source path must identify a JSON file: %q", sourcePath)
	}
	return nil
}

func appendGovernanceString(dst []byte, value string) []byte {
	dst = appendGovernanceUint64(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendGovernanceUint64(dst []byte, value uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	return append(dst, buf[:]...)
}
