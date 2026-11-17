package vectordata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"unicode/utf8"
)

// ProviderAnnotations is an immutable JSON object of connector-specific schema
// hints. Its zero value means no hints. Use NewProviderAnnotations or the typed
// helpers to construct it. Immutable values keep property definitions comparable
// and prevent caller maps, byte buffers or decoded values from aliasing schemas.
// Providers own the meaning and validation of their annotation names.
type ProviderAnnotations string

// NewProviderAnnotations snapshots JSON-compatible values without converting
// exact numbers through float64. Unsupported values return ErrSchemaMismatch.
func NewProviderAnnotations(values map[string]any) (ProviderAnnotations, error) {
	for name := range values {
		if !validAnnotationName(name) {
			return "", fmt.Errorf("%w: invalid annotation name %q", ErrSchemaMismatch, name)
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("%w: provider annotations: %w", ErrSchemaMismatch, err)
	}
	return ParseProviderAnnotations(raw)
}

// ParseProviderAnnotations validates and canonicalizes a JSON object. Null and
// empty objects mean no annotations. Names must be nonblank UTF-8 without NUL.
func ParseProviderAnnotations(raw []byte) (ProviderAnnotations, error) {
	invalid := func(err error) (ProviderAnnotations, error) {
		return "", fmt.Errorf("%w: provider annotations: %w", ErrSchemaMismatch, err)
	}
	if !utf8.Valid(raw) {
		return invalid(fmt.Errorf("JSON must be valid UTF-8"))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalid(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid(fmt.Errorf("expected one JSON object"))
	}
	if value == nil {
		return "", nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return invalid(fmt.Errorf("expected a JSON object"))
	}
	for name := range object {
		if !validAnnotationName(name) {
			return invalid(fmt.Errorf("invalid annotation name %q", name))
		}
	}
	if len(object) == 0 {
		return "", nil
	}
	canonicalAnnotationNumbers(object)
	canonical, err := json.Marshal(object)
	if err != nil {
		return invalid(err)
	}
	return ProviderAnnotations(canonical), nil
}

func validAnnotationName(name string) bool {
	return strings.TrimSpace(name) != "" && strings.TrimSpace(name) == name && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

// Normalize also validates values made by direct string conversion.
func (a ProviderAnnotations) Normalize() (ProviderAnnotations, error) {
	if a == "" {
		return "", nil
	}
	return ParseProviderAnnotations([]byte(a))
}

func (a ProviderAnnotations) MarshalJSON() ([]byte, error) {
	normalized, err := a.Normalize()
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return []byte("{}"), nil
	}
	return []byte(normalized), nil
}

func (a *ProviderAnnotations) UnmarshalJSON(raw []byte) error {
	value, err := ParseProviderAnnotations(raw)
	if err == nil {
		*a = value
	}
	return err
}

// WithProviderAnnotation returns a new value and never changes a or value.
func WithProviderAnnotation[T any](a ProviderAnnotations, name string, value T) (ProviderAnnotations, error) {
	object, err := annotationObject(a)
	if err != nil {
		return "", err
	}
	if !validAnnotationName(name) {
		return "", fmt.Errorf("%w: invalid annotation name %q", ErrSchemaMismatch, name)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: annotation %q: %w", ErrSchemaMismatch, name, err)
	}
	object[name] = raw
	return annotationsFromObject(object)
}

// WithoutProviderAnnotation returns a new value without name; absence is a no-op.
func WithoutProviderAnnotation(a ProviderAnnotations, name string) (ProviderAnnotations, error) {
	object, err := annotationObject(a)
	if err != nil {
		return "", err
	}
	delete(object, name)
	return annotationsFromObject(object)
}

// GetProviderAnnotation decodes a caller-owned value using JSON type rules.
// Missing names return the zero value, false, nil; wrong types are schema errors.
// JSON null follows encoding/json semantics (use a pointer type to detect null).
func GetProviderAnnotation[T any](a ProviderAnnotations, name string) (value T, found bool, err error) {
	if a == "" {
		return value, false, nil
	}
	object, err := annotationObject(a)
	if err != nil {
		return value, false, err
	}
	raw, ok := object[name]
	if !ok {
		return value, false, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		var zero T
		return zero, true, fmt.Errorf("%w: annotation %q: %w", ErrSchemaMismatch, name, err)
	}
	return value, true, nil
}

func annotationObject(a ProviderAnnotations) (map[string]json.RawMessage, error) {
	normalized, err := a.Normalize()
	if err != nil {
		return nil, err
	}
	object := map[string]json.RawMessage{}
	if normalized != "" {
		err = json.Unmarshal([]byte(normalized), &object)
	}
	return object, err
}
func annotationsFromObject(object map[string]json.RawMessage) (ProviderAnnotations, error) {
	raw, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("%w: provider annotations: %w", ErrSchemaMismatch, err)
	}
	return ParseProviderAnnotations(raw)
}

func canonicalAnnotationNumbers(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if n, ok := child.(json.Number); ok {
				v[key] = canonicalAnnotationNumber(n)
			} else {
				canonicalAnnotationNumbers(child)
			}
		}
	case []any:
		for i, child := range v {
			if n, ok := child.(json.Number); ok {
				v[i] = canonicalAnnotationNumber(n)
			} else {
				canonicalAnnotationNumbers(child)
			}
		}
	}
}

// Canonicalize JSON/JSONB numeric spellings exactly, with bounded expansion.
// Ordinary integer spellings stay integers so typed JSON getters keep working.
func canonicalAnnotationNumber(number json.Number) json.Number {
	text := string(number)
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	power := new(big.Int)
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		power.SetString(text[i+1:], 10) // decoder already checked JSON syntax
		text = text[:i]
	}
	if i := strings.IndexByte(text, '.'); i >= 0 {
		power.Sub(power, big.NewInt(int64(len(text)-i-1)))
		text = text[:i] + text[i+1:]
	}
	digits := strings.TrimLeft(text, "0")
	if digits == "" {
		return "0"
	}
	trimmed := strings.TrimRight(digits, "0")
	power.Add(power, big.NewInt(int64(len(digits)-len(trimmed))))
	digits = trimmed
	point := new(big.Int).Add(power, big.NewInt(int64(len(digits))))
	if point.IsInt64() && point.Int64() >= -5 && point.Int64() <= 21 {
		position := int(point.Int64())
		switch {
		case position <= 0:
			return json.Number(sign + "0." + strings.Repeat("0", -position) + digits)
		case position >= len(digits):
			return json.Number(sign + digits + strings.Repeat("0", position-len(digits)))
		default:
			return json.Number(sign + digits[:position] + "." + digits[position:])
		}
	}
	coefficient := digits[:1]
	if len(digits) > 1 {
		coefficient += "." + digits[1:]
	}
	point.Sub(point, big.NewInt(1))
	return json.Number(sign + coefficient + "e" + point.String())
}
