package vectordata

import (
	"fmt"
	"reflect"
)

// ArrayElementType reports the scalar type of a native array property. JSON and
// binary byte slices are not native array properties.
func (t DataType) ArrayElementType() (DataType, bool) {
	switch t {
	case TypeStringArray:
		return TypeString, true
	case TypeInt64Array:
		return TypeInt64, true
	case TypeFloat64Array:
		return TypeFloat64, true
	case TypeBoolArray:
		return TypeBool, true
	default:
		return "", false
	}
}

func nativeArrayKind(typ reflect.Type, element DataType) bool {
	switch element {
	case TypeInt64:
		return typ.Kind() >= reflect.Int && typ.Kind() <= reflect.Uint64
	case TypeFloat64:
		return typ.Kind() >= reflect.Int && typ.Kind() <= reflect.Int64 || typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64
	case TypeBool:
		return typ.Kind() == reflect.Bool
	}
	return false
}

func normalizeNativeArray(value any, p DataProperty) (any, error) {
	invalid := func(reason string) (any, error) {
		return nil, fmt.Errorf("%w: property %q: %s", ErrInvalidRecord, p.Name, reason)
	}
	element, _ := p.Type.ArrayElementType()
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Interface && !nativeArrayKind(v.Type().Elem(), element) {
		return invalid("incompatible native array type")
	}
	if v.IsNil() {
		if p.Optional {
			return nil, nil
		}
		return invalid("required array cannot be NULL")
	}
	var out reflect.Value
	switch p.Type {
	case TypeInt64Array:
		out = reflect.MakeSlice(reflect.TypeFor[[]int64](), v.Len(), v.Len())
	case TypeFloat64Array:
		out = reflect.MakeSlice(reflect.TypeFor[[]float64](), v.Len(), v.Len())
	case TypeBoolArray:
		out = reflect.MakeSlice(reflect.TypeFor[[]bool](), v.Len(), v.Len())
	}
	for i := 0; i < v.Len(); i++ {
		item := v.Index(i).Interface()
		if isNilValue(item) {
			return invalid("native arrays cannot contain NULL elements")
		}
		normalized, err := normalizeData(item, DataProperty{Name: p.Name, Type: element})
		if err != nil {
			return nil, fmt.Errorf("property %q element %d: %w", p.Name, i, err)
		}
		out.Index(i).Set(reflect.ValueOf(normalized))
	}
	return out.Interface(), nil
}

// NormalizeModelArrayElement validates a membership operand and canonicalizes it
// to the array's scalar type. A nil operand cannot match a native array element;
// it is retained for SQL null semantics, independently of property nullability.
func NormalizeModelArrayElement(value any, arrayType DataType) (any, error) {
	element, ok := arrayType.ArrayElementType()
	if !ok {
		return nil, fmt.Errorf("%w: not a native array type", ErrInvalidRecord)
	}
	if isNilValue(value) {
		return nil, nil
	}
	return normalizeData(value, DataProperty{Type: element})
}
