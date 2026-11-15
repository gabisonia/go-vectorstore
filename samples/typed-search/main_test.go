package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			args := []string{"-category", "architecture"}
			if backend == "sqlite" {
				args = append(args, "-sqlite", filepath.Join(t.TempDir(), "search.db"))
			}
			for i := 0; i < 2; i++ {
				var out bytes.Buffer
				if err := run(context.Background(), args, &out); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "Matches: 1\ncosts: Reducing cloud costs") || strings.Contains(out.String(), "Go cancellation") {
					t.Fatalf("unexpected filtered results: %s", &out)
				}
			}
		})
	}
}
func TestInvalidOptions(t *testing.T) {
	for _, args := range [][]string{{"-dsn", "unused", "-sqlite", "unused"}, {"-dimensions", "2"}, {"-openai", "-dimensions", "-1"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, nil, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
