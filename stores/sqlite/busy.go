package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const busyWaitTimeout = 5 * time.Second

// retryBusy waits in Go rather than inside SQLite's busy handler, which can
// block past a context deadline. Callers may only retry transaction acquisition
// or side-effect-free reads; write statements/commits must never be replayed.
func retryBusy[T any](ctx context.Context, run func() (T, error)) (T, error) {
	var zero T
	start := time.Now()
	delay := time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := run()
		if err == nil {
			return value, nil
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return zero, errors.Join(contextErr, err)
		}
		var busy *driver.Error
		if !errors.As(err, &busy) || busy.Code()&255 != sqlite3.SQLITE_BUSY {
			return zero, err
		}
		remaining := busyWaitTimeout - time.Since(start)
		if remaining <= 0 {
			return zero, err
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
		if time.Since(start) >= busyWaitTimeout {
			return zero, err
		}
		delay = min(delay*2, 50*time.Millisecond)
	}
}

func (s *Store) beginTx(ctx context.Context) (*sql.Tx, error) {
	return retryBusy(ctx, func() (*sql.Tx, error) { return s.db.BeginTx(ctx, nil) })
}
