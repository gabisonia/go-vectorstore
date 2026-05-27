package postgres

import (
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestDefaultStoreOptions(t *testing.T) {
	opts := DefaultStoreOptions()

	if opts.Schema != "public" {
		t.Fatalf("Schema = %q", opts.Schema)
	}
	if !opts.EnsureExtension {
		t.Fatal("EnsureExtension should default to true")
	}
	if !opts.StrictByDefault {
		t.Fatal("StrictByDefault should default to true")
	}
}

func TestStoreOptionsWithDefaultsAndValidate(t *testing.T) {
	if got := (StoreOptions{}).withDefaults().Schema; got != "public" {
		t.Fatalf("default schema = %q", got)
	}
	if err := (StoreOptions{Schema: "tenant"}).validate(); err != nil {
		t.Fatalf("validate tenant schema: %v", err)
	}
}

func TestNewVectorStoreRejectsNilPool(t *testing.T) {
	_, err := NewVectorStore(nil, StoreOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectionHandleAppliesDefaults(t *testing.T) {
	store := &PostgresVectorStore{opts: StoreOptions{Schema: "tenant"}}

	collection := store.Collection("docs", 3, "").(*PostgresCollection)

	if collection.Name() != "docs" {
		t.Fatalf("Name = %q", collection.Name())
	}
	if collection.Dimension() != 3 {
		t.Fatalf("Dimension = %d", collection.Dimension())
	}
	if collection.Metric() != vectordata.DistanceCosine {
		t.Fatalf("Metric = %q", collection.Metric())
	}
	if collection.tableName() != `"tenant"."docs"` {
		t.Fatalf("tableName = %q", collection.tableName())
	}
}

func TestNormalizeCollectionSpec(t *testing.T) {
	store := &PostgresVectorStore{opts: StoreOptions{StrictByDefault: false}}

	spec, mode, err := store.normalizeCollectionSpec(vectordata.CollectionSpec{
		Name:      "  docs  ",
		Dimension: 3,
	})

	if err != nil {
		t.Fatalf("normalizeCollectionSpec: %v", err)
	}
	if spec.Name != "docs" {
		t.Fatalf("Name = %q", spec.Name)
	}
	if spec.Metric != vectordata.DistanceCosine {
		t.Fatalf("Metric = %q", spec.Metric)
	}
	if mode != vectordata.EnsureAutoMigrate {
		t.Fatalf("Mode = %q", mode)
	}
}

func TestNormalizeCollectionSpecRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		spec vectordata.CollectionSpec
	}{
		{name: "empty name", spec: vectordata.CollectionSpec{Name: " ", Dimension: 3}},
		{name: "invalid dimension", spec: vectordata.CollectionSpec{Name: "docs", Dimension: 0}},
		{name: "invalid metric", spec: vectordata.CollectionSpec{Name: "docs", Dimension: 3, Metric: "bad"}},
		{name: "invalid mode", spec: vectordata.CollectionSpec{Name: "docs", Dimension: 3, Mode: "bad"}},
	}

	store := &PostgresVectorStore{opts: StoreOptions{StrictByDefault: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := store.normalizeCollectionSpec(tt.spec)
			if !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("expected ErrSchemaMismatch, got %v", err)
			}
		})
	}
}
