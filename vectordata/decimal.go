package vectordata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Decimal is an exact decimal value. ParseDecimal validates and canonicalizes it.
// Its value range matches .NET decimal: a 96-bit coefficient, a separate sign,
// and scale 0–28. Native storage is opt-in with type=decimal or type=decimal_array.
// Direct conversions may create invalid values; writes and marshaling validate.
type Decimal string

const maxDecimalCoefficient = "79228162514264337593543950335"

// ParseDecimal accepts signed decimal text with an optional decimal exponent.
// It removes redundant zeros and exponents without rounding. Values outside the
// supported coefficient/scale range, NaN and infinity are invalid.
func ParseDecimal(text string) (Decimal, error) {
	invalid := func() (Decimal, error) {
		return "", fmt.Errorf("%w: decimal requires a finite 96-bit coefficient and scale 0–28", ErrInvalidRecord)
	}
	if !modelNumericPattern.MatchString(text) {
		return invalid()
	}
	var exponent int64
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.ParseInt(text[i+1:], 10, 32)
		if err != nil {
			return invalid()
		}
		text = text[:i]
	}
	negative := text[0] == '-'
	if text[0] == '-' || text[0] == '+' {
		text = text[1:]
	}
	var fraction int
	if i := strings.IndexByte(text, '.'); i >= 0 {
		fraction = len(text) - i - 1
		text = text[:i] + text[i+1:]
	}
	digits := strings.TrimLeft(text, "0")
	if digits == "" {
		return Decimal("0"), nil
	}
	trimmed := strings.TrimRight(digits, "0")
	scale := int64(fraction) - exponent - int64(len(digits)-len(trimmed))
	digits = trimmed
	if scale > 28 {
		return invalid()
	}
	if scale < 0 {
		// Check before padding so a large exponent cannot allocate a huge value.
		if int64(len(digits))-scale > int64(len(maxDecimalCoefficient)) {
			return invalid()
		}
		digits += strings.Repeat("0", int(-scale))
		scale = 0
	}
	if len(digits) > len(maxDecimalCoefficient) || len(digits) == len(maxDecimalCoefficient) && digits > maxDecimalCoefficient {
		return invalid()
	}
	if scale > 0 {
		if int64(len(digits)) <= scale {
			digits = "0." + strings.Repeat("0", int(scale)-len(digits)) + digits
		} else {
			i := len(digits) - int(scale)
			digits = digits[:i] + "." + digits[i:]
		}
	}
	if negative {
		digits = "-" + digits
	}
	return Decimal(digits), nil
}

func (d Decimal) String() string { return string(d) }
func (d Decimal) MarshalText() ([]byte, error) {
	value, err := ParseDecimal(string(d))
	return []byte(value), err
}
func (d *Decimal) UnmarshalText(text []byte) error {
	value, err := ParseDecimal(string(text))
	if err == nil {
		*d = value
	}
	return err
}

// MarshalJSON emits an exact JSON number, without converting through float64.
func (d Decimal) MarshalJSON() ([]byte, error) { return d.MarshalText() }
func (d *Decimal) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	var value json.Number
	if len(data) == 0 || data[0] == '"' || string(data) == "null" || json.Unmarshal(data, &value) != nil {
		return fmt.Errorf("%w: decimal JSON must be a number", ErrInvalidRecord)
	}
	return d.UnmarshalText([]byte(value))
}

func normalizeDecimal(value any) (any, error) {
	v := reflect.ValueOf(value)
	var text string
	switch v.Kind() {
	case reflect.String:
		text = v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		text = strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		text = strconv.FormatUint(v.Uint(), 10)
	default:
		return nil, fmt.Errorf("%w: decimal requires exact text or an integer; floating-point values are not accepted", ErrInvalidRecord)
	}
	return ParseDecimal(text)
}
