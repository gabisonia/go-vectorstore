package vectordata

import (
	"errors"
	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
)

var (
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
