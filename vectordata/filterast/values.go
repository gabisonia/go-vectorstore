package filterast

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var ErrInvalidFilter = errors.New("vectordata: invalid filter")

const NumericTextPattern = `^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$`

func IsNilValue(value any) bool {
	if value == nil {
		return true
	}
	switch v := reflect.ValueOf(value); v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func NumericLiteral(v any) (string, bool, error) {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		// JSON encoding preserves integer and decimal precision and rejects
		// non-finite floats and malformed json.Number values.
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", true, fmt.Errorf("%w: invalid numeric value: %v", ErrInvalidFilter, err)
		}
		return string(encoded), true, nil
	default:
		return "", false, nil
	}
}
