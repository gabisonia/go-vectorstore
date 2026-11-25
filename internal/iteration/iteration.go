// Package iteration shares lazy, cancelable sequence adapters between providers.
package iteration

import (
	"context"
	"iter"
)

// WithContext checks cancellation before starting, between values and at
// exhaustion. Early termination and the first error stop the source immediately.
func WithContext[T any](ctx context.Context, source iter.Seq2[T, error]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		if err := ctx.Err(); err != nil {
			yield(zero, err)
			return
		}
		for value, err := range source {
			if err != nil {
				yield(zero, err)
				return
			}
			if err := ctx.Err(); err != nil {
				yield(zero, err)
				return
			}
			if !yield(value, nil) {
				return
			}
			if ctx.Err() != nil {
				break
			}
		}
		if err := ctx.Err(); err != nil {
			yield(zero, err)
		}
	}
}

// FromSlice invokes load only when enumerated. Loading completes before the
// first value is exposed, so cursors/locks owned by load can close first.
func FromSlice[T any](ctx context.Context, load func() ([]T, error)) iter.Seq2[T, error] {
	return WithContext(ctx, func(yield func(T, error) bool) {
		values, err := load()
		if err != nil {
			var zero T
			yield(zero, err)
			return
		}
		for _, value := range values {
			if !yield(value, nil) {
				return
			}
		}
	})
}

// Collect returns a non-nil empty slice on success, or no partial values on error.
func Collect[T any](source iter.Seq2[T, error]) ([]T, error) {
	values := []T{}
	for value, err := range source {
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}
