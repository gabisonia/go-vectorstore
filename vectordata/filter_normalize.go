package vectordata

import "github.com/gabisonia/go-vectorstore/vectordata/filterast"

func NormalizeFieldRef(ref FieldRef) (FieldRef, error) { return filterast.NormalizeFieldRef(ref) }
