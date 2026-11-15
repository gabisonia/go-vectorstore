package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestL1RejectsIVFFlatBeforeDatabaseAccess(t *testing.T) {
	store := &PostgresVectorStore{opts: DefaultStoreOptions()}
	for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 3, Metric: vectordata.DistanceL1, Precision: precision, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodIVFFlat}}}}
		if _, err := store.GetModelCollection("l1", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("IVFFlat definition: %v", err)
		}
		if _, err := store.PlanMigration(context.Background(), "l1", d); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("IVFFlat migration: %v", err)
		}
	}
	legacy := testCollection()
	legacy.metric = vectordata.DistanceL1
	if err := legacy.EnsureIndexes(context.Background(), vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodIVFFlat}}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("legacy IVFFlat index: %v", err)
	}
	if _, err := legacy.SearchByVector(context.Background(), []float32{0, 0, 0}, 1, vectordata.SearchOptions{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 1}}); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
		t.Fatalf("legacy IVFFlat tuning: %v", err)
	}
	plan, err := legacy.buildSearchPlan([]float32{0, 0, 0}, 1, vectordata.SearchOptions{})
	if err != nil || !strings.Contains(plan.query, "<+>") {
		t.Fatalf("legacy L1 plan: %s, %v", plan.query, err)
	}
}

func TestL1TuningRejectedBeforeEmbedding(t *testing.T) {
	store := &PostgresVectorStore{opts: DefaultStoreOptions()}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL1, SourceProperty: "Body"}}}
	calls := 0
	handle, err := store.GetModelCollection("l1", d, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return [][]float32{{0, 0}}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	for _, tuning := range []any{SearchTuning{IVFFlat: &vectordata.IVFFlatSearchOptions{}}, &SearchTuning{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 1}}} {
		opts := vectordata.ModelSearchOptions{ProviderOptions: tuning}
		if _, err := c.Search(context.Background(), []float32{0, 0}, 1, opts); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
			t.Fatalf("L1 vector tuning: %v", err)
		}
		if _, err := c.SearchText(context.Background(), "query", 1, opts); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
			t.Fatalf("L1 text tuning: %v", err)
		}
		if _, err := c.HybridSearchText(context.Background(), "query", "keyword", 1, vectordata.HybridSearchOptions{ModelSearchOptions: opts}); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
			t.Fatalf("L1 hybrid tuning: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid tuning invoked embedder %d times", calls)
	}
}

func TestIndexCapabilityLimitsMatchValidation(t *testing.T) {
	store := &PostgresVectorStore{opts: DefaultStoreOptions()}
	for _, index := range store.Capabilities().VectorIndexCapabilities {
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Metric: index.Metric, Precision: index.Precision, Dimensions: index.MaxDimensions, Index: &vectordata.VectorIndexOptions{Method: index.Method}}}}
		if _, err := store.GetModelCollection("capability", d, vectordata.ModelCollectionOptions{}); err != nil {
			t.Fatalf("advertised limit rejected: %+v: %v", index, err)
		}
		d.Vectors[0].Dimensions++
		if _, err := store.GetModelCollection("capability", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("index limit exceeded: %+v: %v", index, err)
		}
	}
}
