package vectordata

import (
	"fmt"
	"strings"
)

// NormalizeFieldRef applies shared validation and canonicalization rules
// so every backend resolves fields the same way.
func NormalizeFieldRef(ref FieldRef) (FieldRef, error) {
	switch ref.Kind {
	case FieldColumn:
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			return FieldRef{}, fmt.Errorf("%w: column field name is empty", ErrInvalidFilter)
		}
		return FieldRef{Kind: FieldColumn, Name: name}, nil
	case FieldMetadata:
		if len(ref.Path) == 0 {
			return FieldRef{}, fmt.Errorf("%w: metadata path is empty", ErrInvalidFilter)
		}
		path := make([]string, len(ref.Path))
		for i, segment := range ref.Path {
			trimmed := strings.TrimSpace(segment)
			if trimmed == "" {
				return FieldRef{}, fmt.Errorf("%w: metadata path segment is empty", ErrInvalidFilter)
			}
			path[i] = trimmed
		}
		return FieldRef{Kind: FieldMetadata, Path: path}, nil
	default:
		return FieldRef{}, fmt.Errorf("%w: unsupported field kind %q", ErrInvalidFilter, ref.Kind)
	}
}
