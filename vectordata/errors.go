package vectordata

import (
	"errors"
	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
)

var (
	ErrMigrationConflict    = errors.New("vectordata: migration review is stale or invalid")
	ErrMigrationNotAdditive = errors.New("vectordata: migration is not additive")
	ErrCollectionNotFound   = errors.New("vectordata: collection not found")
	ErrUnsupported          = errors.New("vectordata: unsupported capability")
	ErrNotFound             = errors.New("vectordata: record not found")
	ErrDimensionMismatch    = errors.New("vectordata: vector dimension mismatch")
	ErrInvalidVector        = errors.New("vectordata: invalid vector")
	ErrSchemaMismatch       = errors.New("vectordata: schema mismatch")
	ErrInvalidFilter        = filterast.ErrInvalidFilter
	ErrInvalidRecord        = errors.New("vectordata: invalid record")
	ErrInvalidSearchOptions = errors.New("vectordata: invalid search options")
)
