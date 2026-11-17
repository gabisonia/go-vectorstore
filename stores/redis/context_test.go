package redis

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Model a socket deadline firing before the context timer callback has updated
// Err/Done. This ordering must not depend on scheduler timing in a regression.
type pendingDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c pendingDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestResolveContextPreservesSocketDeadlineRace(t *testing.T) {
	native := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	ctx := pendingDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	if ctx.Err() != nil {
		t.Fatal("regression requires a pending context timer")
	}
	s, err := New(StoreOptions{Namespace: "deadline-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &Collection{store: s, name: "records"}
	got := c.operation(ctx, "count", native)
	if !errors.Is(got, context.DeadlineExceeded) || !errors.Is(got, os.ErrDeadlineExceeded) {
		t.Fatalf("deadline/native cause lost: %v", got)
	}
	var socket *net.OpError
	var operation *vectordata.OperationError
	if !errors.As(got, &socket) || socket != native || !errors.As(got, &operation) || operation.Namespace != "deadline-test" {
		t.Fatalf("native error/metadata lost: %v", got)
	}
}

func TestResolveContextDoesNotMislabelOtherResults(t *testing.T) {
	timeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	ordinary := errors.New("Redis command rejected")
	past := pendingDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	future := pendingDeadlineContext{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
	for _, tt := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"socket timeout without context deadline", context.Background(), timeout},
		{"socket timeout before context deadline", future, timeout},
		{"unrelated failure after deadline", past, ordinary},
		{"successful result after deadline", past, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveContext(tt.ctx, tt.err); got != tt.err {
				t.Fatalf("result was reclassified: %v", got)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	got := resolveContext(pendingDeadlineContext{Context: canceled, deadline: past.deadline}, timeout)
	if !errors.Is(got, context.Canceled) || errors.Is(got, context.DeadlineExceeded) || !errors.Is(got, timeout) {
		t.Fatalf("cancellation cause changed: %v", got)
	}
}
