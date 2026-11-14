package vectordata

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var modelNumericPattern = regexp.MustCompile(numericTextPattern)

type truth uint8

const (
	falseValue truth = iota
	trueValue
	unknownValue
)

func boolTruth(v bool) truth {
	if v {
		return trueValue
	}
	return falseValue
}

// ValidateModelFilter checks field references and supported operators even for empty collections.
func ValidateModelFilter(filter Filter, d CollectionDefinition) error {
	if filter == nil {
		return nil
	}
	field := func(ref FieldRef, contains bool) error {
		ref, err := NormalizeFieldRef(ref)
		if err != nil {
			return err
		}
		if ref.Kind == FieldColumn {
			if ref.Name == d.Key.Name && !contains {
				return nil
			}
			for _, p := range d.Data {
				if p.Name == ref.Name {
					if contains && p.Type != TypeStringArray {
						return fmt.Errorf("%w: Contains requires an array property", ErrInvalidFilter)
					}
					return nil
				}
			}
		}
		if ref.Kind == FieldDataPath {
			for _, p := range d.Data {
				if p.Name == ref.Name && p.Type == TypeJSON {
					return nil
				}
			}
		}
		return fmt.Errorf("%w: unknown model property %q", ErrInvalidFilter, ref.Name)
	}
	valueCheck := func(ref FieldRef, value any, contains, ordered bool) error {
		if err := field(ref, contains); err != nil {
			return err
		}
		invalid := func(err error) error {
			return fmt.Errorf("%w: invalid value for %q: %v", ErrInvalidFilter, ref.Name, err)
		}
		ref, _ = NormalizeFieldRef(ref)
		if ordered && value == nil {
			return invalid(fmt.Errorf("ordered comparisons require a value"))
		}
		if ref.Kind == FieldDataPath {
			if ordered {
				_, numeric, err := numericLiteral(value)
				if err != nil {
					return invalid(err)
				}
				if !numeric && reflect.TypeOf(value).Kind() != reflect.String {
					return invalid(fmt.Errorf("ordered JSON comparisons require a number or string"))
				}
			}
			if _, err := json.Marshal(value); err != nil {
				return invalid(err)
			}
			return nil
		}
		if value == nil {
			return nil
		}
		if ref.Name == d.Key.Name {
			_, err := d.NormalizeKey(value)
			if err != nil {
				return invalid(err)
			}
			return nil
		}
		for _, property := range d.Data {
			if property.Name != ref.Name {
				continue
			}
			if ordered && (property.Type == TypeJSON || property.Type == TypeStringArray || property.Type == TypeBool) {
				return invalid(fmt.Errorf("property is not an ordered scalar"))
			}
			if contains {
				if _, ok := value.(string); !ok {
					return invalid(fmt.Errorf("string array membership requires a string"))
				}
				return nil
			}
			if _, err := normalizeData(value, property); err != nil {
				return invalid(err)
			}
			return nil
		}
		return nil
	}

	switch f := filter.(type) {
	case EqFilter:
		return valueCheck(f.Field, f.Value, false, false)
	case InFilter:
		if len(f.Values) == 0 {
			return fmt.Errorf("%w: empty In", ErrInvalidFilter)
		}
		for _, value := range f.Values {
			if err := valueCheck(f.Field, value, false, false); err != nil {
				return err
			}
		}
		return nil
	case GtFilter:
		return valueCheck(f.Field, f.Value, false, true)
	case LtFilter:
		return valueCheck(f.Field, f.Value, false, true)
	case GteFilter:
		return valueCheck(f.Field, f.Value, false, true)
	case LteFilter:
		return valueCheck(f.Field, f.Value, false, true)
	case ExistsFilter:
		return field(f.Field, false)
	case ContainsFilter:
		return valueCheck(f.Field, f.Value, true, false)
	case AndFilter:
		if len(f.Children) == 0 {
			return fmt.Errorf("%w: empty And", ErrInvalidFilter)
		}
		for _, child := range f.Children {
			if child == nil {
				return fmt.Errorf("%w: nil child", ErrInvalidFilter)
			}
			if err := ValidateModelFilter(child, d); err != nil {
				return err
			}
		}
		return nil
	case OrFilter:
		if len(f.Children) == 0 {
			return fmt.Errorf("%w: empty Or", ErrInvalidFilter)
		}
		for _, child := range f.Children {
			if child == nil {
				return fmt.Errorf("%w: nil child", ErrInvalidFilter)
			}
			if err := ValidateModelFilter(child, d); err != nil {
				return err
			}
		}
		return nil
	case NotFilter:
		if f.Child == nil {
			return fmt.Errorf("%w: nil Not", ErrInvalidFilter)
		}
		return ValidateModelFilter(f.Child, d)
	default:
		return fmt.Errorf("%w: unsupported filter %T", ErrInvalidFilter, filter)
	}
}
func modelField(r ModelRecord, ref FieldRef) (any, bool, bool) {
	if ref.Kind == FieldColumn {
		return r.Data[ref.Name], true, false
	}
	value, present := r.Data[ref.Name]
	if value == nil {
		return nil, false, true
	}
	if _, ok := value.(JSONNull); ok {
		value = nil
	}
	for _, segment := range ref.Path {
		if !present {
			return nil, false, true
		}
		switch node := value.(type) {
		case map[string]any:
			value, present = node[segment]
		case []any:
			// PostgreSQL accepts leading ASCII whitespace in array indexes, but
			// treats trailing whitespace as an invalid index. Object keys stay literal.
			i, err := strconv.ParseInt(strings.TrimLeft(segment, " \t\n\r\v\f"), 10, 32)
			if err != nil {
				return nil, false, true
			}
			if i < 0 {
				i += int64(len(node))
			}
			if i < 0 || i >= int64(len(node)) {
				return nil, false, true
			}
			value = node[i]
		default:
			return nil, false, true
		}
	}
	return value, present, true
}
func modelNumber(value any) (*big.Rat, bool) {
	var s string
	switch v := value.(type) {
	case json.Number:
		s = v.String()
	case string:
		s = v
	default:
		literal, numeric, err := numericLiteral(v)
		if !numeric {
			rv := reflect.ValueOf(v)
			if rv.IsValid() {
				switch rv.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					literal = strconv.FormatInt(rv.Int(), 10)
					numeric = true
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					literal = strconv.FormatUint(rv.Uint(), 10)
					numeric = true
				case reflect.Float32, reflect.Float64:
					literal = strconv.FormatFloat(rv.Float(), 'g', -1, rv.Type().Bits())
					numeric = true
				}
			}
		}
		if !numeric || err != nil {
			return nil, false
		}
		s = literal
	}
	if len(s) > 200000 || !modelNumericPattern.MatchString(s) {
		return nil, false
	}
	if index := strings.IndexAny(s, "eE"); index >= 0 {
		exponent, err := strconv.ParseInt(s[index+1:], 10, 32)
		if err != nil || exponent > 131071 || exponent < -16383 {
			return nil, false
		}
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}
func modelEqual(a, b any) bool {
	if _, ok := a.(JSONNull); ok {
		a = nil
	}
	if _, ok := b.(JSONNull); ok {
		b = nil
	}
	// Strings stay strings for equality; numeric coercion only applies to numbers.
	_, aString := a.(string)
	_, bString := b.(string)
	if aString || bString {
		return aString && bString && a == b
	}
	if x, ok := modelNumber(a); ok {
		if y, ok := modelNumber(b); ok {
			return x.Cmp(y) == 0
		}
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, br := reflect.ValueOf(a), reflect.ValueOf(b)
	if ar.Kind() == reflect.String && br.Kind() == reflect.String {
		return ar.String() == br.String()
	}
	if ar.Kind() == reflect.Bool && br.Kind() == reflect.Bool {
		return ar.Bool() == br.Bool()
	}
	if (ar.Kind() == reflect.Slice || ar.Kind() == reflect.Array) && (br.Kind() == reflect.Slice || br.Kind() == reflect.Array) {
		if ar.Len() != br.Len() {
			return false
		}
		for i := 0; i < ar.Len(); i++ {
			if !modelEqual(ar.Index(i).Interface(), br.Index(i).Interface()) {
				return false
			}
		}
		return true
	}
	if am, ok := a.(map[string]any); ok {
		bm, ok := b.(map[string]any)
		if !ok || len(am) != len(bm) {
			return false
		}
		for key, value := range am {
			other, exists := bm[key]
			if !exists || !modelEqual(value, other) {
				return false
			}
		}
		return true
	}
	if at, ok := a.(time.Time); ok {
		bt, ok := b.(time.Time)
		return ok && at.Equal(bt)
	}
	return reflect.DeepEqual(a, b)
}
func compareModelValues(a, b any) (int, bool) {
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), true
		}
	}
	if x, ok := a.(bool); ok {
		if y, ok := b.(bool); ok {
			if x == y {
				return 0, true
			}
			if !x {
				return -1, true
			}
			return 1, true
		}
	}

	if x, ok := modelNumber(a); ok {
		if y, ok := modelNumber(b); ok {
			return x.Cmp(y), true
		}
	}
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), true
		}
	}
	if x, ok := a.(time.Time); ok {
		if y, ok := b.(time.Time); ok {
			return x.Compare(y), true
		}
	}
	return 0, false
}

// CompareModelValues orders native data values. It is used for in-memory paging;
// text collation remains provider-specific, as with the .NET providers.
func CompareModelValues(a, b any) int {
	if a == nil {
		if b == nil {
			return 0
		}
		return 1
	}
	if b == nil {
		return -1
	}
	cmp, _ := compareModelValues(a, b)
	return cmp
}
func modelTruth(filter Filter, r ModelRecord, d CollectionDefinition) (truth, error) {
	resolve := func(ref FieldRef) (any, bool, bool) {
		if ref.Kind == FieldColumn && strings.TrimSpace(ref.Name) == d.Key.Name {
			return r.Key, true, false
		}
		normalized, _ := NormalizeFieldRef(ref)
		return modelField(r, normalized)
	}
	normalize := func(ref FieldRef, value any) any {
		if ref.Kind == FieldDataPath {
			normalized, _ := normalizeData(value, DataProperty{Type: TypeJSON, Optional: true})
			return normalized
		}
		if ref.Kind != FieldColumn {
			return value
		}
		if isNilValue(value) {
			return nil
		}
		name := strings.TrimSpace(ref.Name)
		if name == d.Key.Name {
			normalized, _ := d.NormalizeKey(value)
			return normalized
		}
		for _, property := range d.Data {
			if property.Name == name {
				normalized, _ := normalizeData(value, property)
				return normalized
			}
		}
		return value
	}
	equal := func(ref FieldRef, value any) truth {
		value = normalize(ref, value)
		actual, present, jsonPath := resolve(ref)
		if !jsonPath && isNilValue(value) {
			return boolTruth(actual == nil)
		}
		if !present || !jsonPath && actual == nil {
			return unknownValue
		}
		return boolTruth(modelEqual(actual, value))
	}
	compare := func(ref FieldRef, value any, op string) truth {
		value = normalize(ref, value)
		actual, present, jsonPath := resolve(ref)
		if !present || actual == nil {
			if _, numeric, _ := numericLiteral(value); jsonPath && numeric {
				return falseValue
			}
			return unknownValue
		}
		if jsonPath {
			if _, numeric, _ := numericLiteral(value); numeric {
				a, ok := modelNumber(actual)
				if !ok {
					return falseValue
				}
				b, ok := modelNumber(value)
				if !ok {
					return falseValue
				}
				n := a.Cmp(b)
				switch op {
				case ">":
					return boolTruth(n > 0)
				case "<":
					return boolTruth(n < 0)
				case ">=":
					return boolTruth(n >= 0)
				case "<=":
					return boolTruth(n <= 0)
				}
			}
			// JSON text comparison uses the caller's string representation.
			actual = fmt.Sprint(actual)
			value = fmt.Sprint(value)
		}
		n, ok := compareModelValues(actual, value)
		if !ok {
			return falseValue
		}
		switch op {
		case ">":
			return boolTruth(n > 0)
		case "<":
			return boolTruth(n < 0)
		case ">=":
			return boolTruth(n >= 0)
		default:
			return boolTruth(n <= 0)
		}
	}
	switch f := filter.(type) {
	case nil:
		return trueValue, nil
	case EqFilter:
		return equal(f.Field, f.Value), nil
	case InFilter:
		unknown := false
		for _, v := range f.Values {
			match := equal(f.Field, v)
			if f.Field.Kind == FieldColumn && isNilValue(v) {
				match = unknownValue
			}
			if match == trueValue {
				return trueValue, nil
			}
			unknown = unknown || match == unknownValue
		}
		if unknown {
			return unknownValue, nil
		}
		return falseValue, nil
	case GtFilter:
		return compare(f.Field, f.Value, ">"), nil
	case LtFilter:
		return compare(f.Field, f.Value, "<"), nil
	case GteFilter:
		return compare(f.Field, f.Value, ">="), nil
	case LteFilter:
		return compare(f.Field, f.Value, "<="), nil
	case ExistsFilter:
		value, present, jsonPath := resolve(f.Field)
		return boolTruth(present && (jsonPath || value != nil)), nil
	case ContainsFilter:
		value, present, jsonPath := resolve(f.Field)
		if !present || value == nil {
			if !jsonPath {
				return unknownValue, nil
			}
			return falseValue, nil
		}
		array := reflect.ValueOf(value)
		if array.Kind() != reflect.Slice && array.Kind() != reflect.Array {
			return falseValue, nil
		}
		needle := f.Value
		if jsonPath {
			needle = normalize(f.Field, needle)
		}
		for i := 0; i < array.Len(); i++ {
			if modelEqual(array.Index(i).Interface(), needle) {
				return trueValue, nil
			}
		}
		return falseValue, nil
	case NotFilter:
		match, err := modelTruth(f.Child, r, d)
		if match == unknownValue {
			return unknownValue, err
		}
		return boolTruth(match == falseValue), err
	case AndFilter:
		unknown := false
		for _, child := range f.Children {
			match, err := modelTruth(child, r, d)
			if err != nil {
				return falseValue, err
			}
			if match == falseValue {
				return falseValue, nil
			}
			unknown = unknown || match == unknownValue
		}
		if unknown {
			return unknownValue, nil
		}
		return trueValue, nil
	case OrFilter:
		unknown := false
		for _, child := range f.Children {
			match, err := modelTruth(child, r, d)
			if err != nil {
				return falseValue, err
			}
			if match == trueValue {
				return trueValue, nil
			}
			unknown = unknown || match == unknownValue
		}
		if unknown {
			return unknownValue, nil
		}
		return falseValue, nil
	}
	return falseValue, ErrInvalidFilter
}
func MatchModelFilter(filter Filter, record ModelRecord, definition CollectionDefinition) (bool, error) {
	if err := ValidateModelFilter(filter, definition); err != nil {
		return false, err
	}
	result, err := modelTruth(filter, record, definition)
	return result == trueValue, err
}

// CloneModelRecord copies mutable data so provider reads do not alias stored values.
func CloneModelRecord(r ModelRecord, d CollectionDefinition, vectors bool) ModelRecord {
	out := ModelRecord{Key: r.Key, Data: map[string]any{}}
	for _, p := range d.Data {
		value := r.Data[p.Name]
		switch p.Type {
		case TypeJSON:
			if value == nil {
				break
			}
			payload, _ := json.Marshal(value)
			decoder := json.NewDecoder(strings.NewReader(string(payload)))
			decoder.UseNumber()
			_ = decoder.Decode(&value)
			if value == nil {
				value = JSONNull{}
			}
		case TypeStringArray:
			if array, ok := value.([]string); ok {
				value = append([]string{}, array...)
			}
		}
		out.Data[p.Name] = value
	}
	if vectors {
		out.Vectors = map[string][]float32{}
		for key, vector := range r.Vectors {
			out.Vectors[key] = append([]float32(nil), vector...)
		}
	}
	return out
}
