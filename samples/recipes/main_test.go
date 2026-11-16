package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestPortableRecipes(t *testing.T) {
	expected := map[string]string{"key-filters": "Matched keys: 2\nPaged key: 2\nExact-key count: 1", "metadata": "Collection: recipe_demo_metadata", "named-vectors": "guide: BodyVector distance 0.000; loaded dimensions 3", "squared-l2": "near: distance 25 score 0.038462\nSquared-distance threshold 5: 1 match", "iterators": "Cancellation observed; count after iterator: 2", "embedded-iterators": "Search: costs\nInput: costs", "capabilities": "Hybrid search: false"}
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			base := []string{}
			if backend == "sqlite" {
				base = []string{"-sqlite", filepath.Join(t.TempDir(), "recipes.db")}
			}
			for recipe, want := range expected {
				var out bytes.Buffer
				args := append(append([]string{}, base...), "-recipe", recipe)
				if err := run(context.Background(), args, &out); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), want) {
					t.Fatalf("%s: %s", recipe, &out)
				}
			}
			for _, recipe := range []string{"hybrid", "migration"} {
				if err := run(context.Background(), append(append([]string{}, base...), "-recipe", recipe), &bytes.Buffer{}); !errors.Is(err, vectordata.ErrUnsupported) {
					t.Fatalf("unsupported %s: %v", recipe, err)
				}
			}
		})
	}
	if err := run(context.Background(), []string{"-recipe", "typo"}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted unknown recipe")
	}
}
