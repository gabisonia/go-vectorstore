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
		// JSON object keys, including empty and whitespace-only strings, are
		// literal names. Only fixed-column aliases are whitespace-normalized.
		copy(path, ref.Path)
		return FieldRef{Kind: FieldMetadata, Path: path}, nil
	default:
		return FieldRef{}, fmt.Errorf("%w: unsupported field kind %q", ErrInvalidFilter, ref.Kind)
	}
}
