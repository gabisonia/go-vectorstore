// Package modelcodec preserves canonical model values in provider JSON payloads.
package modelcodec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// A separate null bit preserves SQL NULL versus a JSON null property value.
type storedValue struct {
	Null  bool            `json:",omitempty"`
	Value json.RawMessage `json:",omitempty"`
}
type storedRecord struct {
	Key           json.RawMessage
	Data          map[string]storedValue
	Vectors       map[string][]float32
	BinaryVectors map[string]vectordata.BinaryVector `json:",omitempty"`
	SparseVectors map[string]vectordata.SparseVector `json:",omitempty"`
}

// Encode serializes a prepared record without losing native key/data precision.
func Encode(r vectordata.ModelRecord) ([]byte, error) {
	if !ValidText(r.Key) {
		return nil, fmt.Errorf("%w: key requires UTF-8", vectordata.ErrInvalidRecord)
	}
	key, err := json.Marshal(r.Key)
	if err != nil {
		return nil, err
	}
	out := storedRecord{Key: key, Data: map[string]storedValue{}, Vectors: r.Vectors, BinaryVectors: r.BinaryVectors, SparseVectors: r.SparseVectors}
	for name, value := range r.Data {
		if !ValidText(value) {
			return nil, fmt.Errorf("%w: property %q requires UTF-8", vectordata.ErrInvalidRecord, name)
		}
		entry := storedValue{Null: value == nil}
		if !entry.Null {
			entry.Value, err = json.Marshal(value)
			if err != nil {
				return nil, err
			}
		}
		out.Data[name] = entry
	}
	return json.Marshal(out)
}

// Decode reconstructs typed fields; callers must validate records before use.
func Decode(payload []byte, d vectordata.CollectionDefinition) (vectordata.ModelRecord, error) {
	var stored storedRecord
	if err := json.Unmarshal(payload, &stored); err != nil {
		return vectordata.ModelRecord{}, err
	}
	out := vectordata.ModelRecord{Data: map[string]any{}, Vectors: stored.Vectors, BinaryVectors: stored.BinaryVectors, SparseVectors: stored.SparseVectors}
	// Unmarshaling JSON null into an integer succeeds and leaves it at zero.
	// A persisted record must contain an actual key, including for generated keys.
	if len(stored.Key) == 0 || bytes.Equal(stored.Key, []byte("null")) {
		return out, fmt.Errorf("%w: missing stored key", vectordata.ErrInvalidRecord)
	}
	var key any
	if d.Key.Type == vectordata.KeyInt64 {
		key = new(int64)
	} else if d.Key.Type == vectordata.KeyUint64 {
		key = new(uint64)
	} else {
		key = new(string)
	}
	if err := json.Unmarshal(stored.Key, key); err != nil {
		return out, err
	}
	out.Key = reflect.ValueOf(key).Elem().Interface()
	for _, p := range d.Data {
		entry, exists := stored.Data[p.Name]
		if !exists {
			return out, fmt.Errorf("%w: missing stored property %q", vectordata.ErrInvalidRecord, p.Name)
		}
		if entry.Null {
			out.Data[p.Name] = nil
			continue
		}
		value, err := DecodeProperty(entry.Value, p)
		if err != nil {
			return out, err
		}
		out.Data[p.Name] = value
	}
	return out, nil
}

// DecodeProperty reconstructs the native value of a non-SQL-NULL JSON field.
func DecodeProperty(raw json.RawMessage, p vectordata.DataProperty) (any, error) {
	var target any
	switch p.Type {
	case vectordata.TypeString, vectordata.TypeUUID, vectordata.TypeDateOnly, vectordata.TypeTimeOnly:
		target = new(string)
	case vectordata.TypeInt64:
		target = new(int64)
	case vectordata.TypeFloat64:
		target = new(float64)
	case vectordata.TypeBool:
		target = new(bool)
	case vectordata.TypeTime:
		target = new(time.Time)
	case vectordata.TypeStringArray, vectordata.TypeUUIDArray, vectordata.TypeDateOnlyArray, vectordata.TypeTimeOnlyArray:
		target = new([]string)
	case vectordata.TypeBytes:
		target = new([]byte)
	case vectordata.TypeInt64Array:
		target = new([]int64)
	case vectordata.TypeFloat64Array:
		target = new([]float64)
	case vectordata.TypeBoolArray:
		target = new([]bool)
	case vectordata.TypeTimeArray:
		target = new([]time.Time)
	case vectordata.TypeDecimal:
		target = new(vectordata.Decimal)
	case vectordata.TypeDecimalArray:
		target = new([]vectordata.Decimal)
	case vectordata.TypeJSON:
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if value == nil {
			value = vectordata.JSONNull{}
		}
		return value, nil
	default:
		return nil, fmt.Errorf("%w: unknown stored data type", vectordata.ErrSchemaMismatch)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, err
	}
	return reflect.ValueOf(target).Elem().Interface(), nil
}

// encoding/json replaces invalid UTF-8. Reject it in native text values so keys
// and data cannot silently change or collide during persistence.
func ValidText(value any) bool {
	switch v := value.(type) {
	case string:
		return utf8.ValidString(v)
	case []string:
		for _, item := range v {
			if !utf8.ValidString(item) {
				return false
			}
		}
	}
	return true
}
