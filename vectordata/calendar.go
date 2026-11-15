package vectordata

import (
	"fmt"
	"reflect"
	"regexp"
	"time"
)

// DateOnly is an ISO calendar date without a time or timezone. Use ParseDateOnly
// to validate it. Native storage is opt-in with type=date_only.
type DateOnly string

// TimeOnly is a clock time without a date or timezone. ParseTimeOnly truncates
// fractional seconds to microseconds. Native storage is opt-in with type=time_only.
type TimeOnly string

var clockPattern = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?$`)

// ParseDateOnly accepts YYYY-MM-DD in the Gregorian calendar, with years 1–9999.
func ParseDateOnly(text string) (DateOnly, error) {
	date, err := time.Parse(time.DateOnly, text)
	if err != nil || len(text) != 10 || date.Year() < 1 || date.Year() > 9999 {
		return "", fmt.Errorf("%w: date_only requires YYYY-MM-DD with year 1–9999", ErrInvalidRecord)
	}
	return DateOnly(text), nil
}

// ParseTimeOnly accepts HH:MM:SS with an optional 1–9 digit fractional second.
// It returns HH:MM:SS.ffffff, truncating to microseconds. Hours must be 00–23;
// timezone suffixes, leap seconds and 24:00:00 are invalid.
func ParseTimeOnly(text string) (TimeOnly, error) {
	if !clockPattern.MatchString(text) {
		return "", fmt.Errorf("%w: time_only requires HH:MM:SS with optional fractional seconds", ErrInvalidRecord)
	}
	clock, err := time.Parse(time.TimeOnly, text)
	if err != nil {
		return "", fmt.Errorf("%w: invalid time_only: %v", ErrInvalidRecord, err)
	}
	return TimeOnly(clock.Truncate(time.Microsecond).Format("15:04:05.000000")), nil
}

func (d DateOnly) String() string { return string(d) }
func (t TimeOnly) String() string { return string(t) }

func (d DateOnly) MarshalText() ([]byte, error) {
	value, err := ParseDateOnly(string(d))
	return []byte(value), err
}
func (d *DateOnly) UnmarshalText(text []byte) error {
	value, err := ParseDateOnly(string(text))
	if err == nil {
		*d = value
	}
	return err
}
func (t TimeOnly) MarshalText() ([]byte, error) {
	value, err := ParseTimeOnly(string(t))
	return []byte(value), err
}
func (t *TimeOnly) UnmarshalText(text []byte) error {
	value, err := ParseTimeOnly(string(text))
	if err == nil {
		*t = value
	}
	return err
}

func normalizeCalendar(value any, kind DataType) (any, error) {
	v := reflect.ValueOf(value)
	if !v.IsValid() || v.Kind() != reflect.String {
		return nil, fmt.Errorf("%w: %s requires ISO text", ErrInvalidRecord, kind)
	}
	if kind == TypeDateOnly {
		date, err := ParseDateOnly(v.String())
		return string(date), err
	}
	clock, err := ParseTimeOnly(v.String())
	return string(clock), err
}
