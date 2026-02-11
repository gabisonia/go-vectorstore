package vectordata

import "errors"

var (
	ErrNotFound          = errors.New("vectordata: record not found")
	ErrDimensionMismatch = errors.New("vectordata: vector dimension mismatch")
	ErrSchemaMismatch    = errors.New("vectordata: schema mismatch")
	ErrInvalidFilter     = errors.New("vectordata: invalid filter")
)
