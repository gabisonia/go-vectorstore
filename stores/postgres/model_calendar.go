package postgres

import (
	"fmt"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgtype"
)

// Decode native values instead of casting dates to text: DateStyle and TimeZone
// must not affect calendar values, and array dimensions must remain available.
func decodeCalendarValue(value any, kind vectordata.DataType) (any, error) {
	if value == nil {
		return nil, nil
	}
	var text string
	switch kind {
	case vectordata.TypeDateOnly:
		date, ok := value.(time.Time)
		if !ok || date.Year() < 1 || date.Year() > 9999 {
			return nil, fmt.Errorf("%w: date_only requires a finite date with year 1–9999", vectordata.ErrInvalidRecord)
		}
		text = date.Format(time.DateOnly)
	case vectordata.TypeTimeOnly:
		clock, ok := value.(pgtype.Time)
		if !ok || !clock.Valid || clock.Microseconds < 0 || clock.Microseconds >= int64(24*time.Hour/time.Microsecond) {
			return nil, fmt.Errorf("%w: time_only requires a clock time before 24:00:00", vectordata.ErrInvalidRecord)
		}
		us := clock.Microseconds
		text = fmt.Sprintf("%02d:%02d:%02d.%06d", us/3600000000, us/60000000%60, us/1000000%60, us%1000000)
	}
	return vectordata.NormalizeModelData(text, vectordata.DataProperty{Type: kind})
}
