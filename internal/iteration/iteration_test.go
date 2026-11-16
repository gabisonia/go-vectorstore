package iteration

import (
	"context"
	"errors"
	"iter"
	"testing"
)

func TestLazyCancellationAndExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	seq := FromSlice(ctx, func() ([]int, error) { calls++; return []int{1}, nil })
	if calls != 0 {
		t.Fatal("loader called eagerly")
	}
	values, failures := 0, 0
	for value, err := range seq {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			failures++
			continue
		}
		if value != 1 {
			t.Fatal(value)
		}
		values++
		cancel()
	}
	if calls != 1 || values != 1 || failures != 1 {
		t.Fatalf("calls=%d values=%d failures=%d", calls, values, failures)
	}
	for _, err := range seq {
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("canceled sequence invoked loader")
	}
}

func TestStopClosesSourceAndDiscardsPartialCollections(t *testing.T) {
	cause := errors.New("read failed")
	for _, stop := range []bool{true, false} {
		closed, afterFirst, afterError := false, false, false
		seq := WithContext(context.Background(), iter.Seq2[int, error](func(yield func(int, error) bool) {
			defer func() { closed = true }()
			if !yield(1, nil) {
				return
			}
			afterFirst = true
			if !yield(0, cause) {
				return
			}
			afterError = true
		}))
		if stop {
			for range seq {
				break
			}
		} else {
			values, err := Collect(seq)
			if values != nil || !errors.Is(err, cause) {
				t.Fatalf("partial collection: %v, %v", values, err)
			}
		}
		if !closed || afterFirst == stop || afterError {
			t.Fatalf("stop=%v closed=%v afterFirst=%v afterError=%v", stop, closed, afterFirst, afterError)
		}
	}
}
