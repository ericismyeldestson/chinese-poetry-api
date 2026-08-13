package processor

import (
	"github.com/ericismyeldestson/chinese-poetry-api/internal/loader"
)

// PoemWork is a source record sent to a worker. SourceOrdinal records loading
// order for diagnostics only; product identity must never depend on it.
type PoemWork struct {
	loader.PoemWithMeta
	SourceOrdinal int64
}
