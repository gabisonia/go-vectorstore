package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIncompatibleCollectionHandles(t *testing.T) {
	for _, recreated := range []bool{false, true} {
		name := "different_handle"
		if recreated {
			name = "recreated_collection"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := memory.New()
			currentDefinition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Value", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}, {Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 2, Optional: true}, {Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 2, Optional: true}}}
			wrongDefinition := currentDefinition.Clone()
			wrongDefinition.Data[0].Type = vectordata.TypeString
			wrong, err := store.GetModelCollection("records", wrongDefinition, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if recreated {
				if err := wrong.EnsureExists(ctx); err != nil {
					t.Fatal(err)
				}
				if err := wrong.EnsureDeleted(ctx); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.EnsureModelCollection(ctx, "records", currentDefinition, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := current.Insert(ctx, []vectordata.ModelRecord{{Key: "kept", Data: map[string]any{"Value": int64(42)}, Vectors: map[string][]float32{"Vector": {1, 0}}}}); err != nil {
				t.Fatal(err)
			}
			record := vectordata.ModelRecord{Key: "bad", Data: map[string]any{"Value": "wrong type"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
			bits, _ := vectordata.ParseBinaryVector("10")
			sparse, _ := vectordata.NewSparseVector(2, []int{0}, []float32{1})
			for _, tt := range []struct {
				name string
				run  func() error
			}{
				{"insert", func() error { _, err := wrong.Insert(ctx, []vectordata.ModelRecord{record}); return err }},
				{"upsert", func() error { _, err := wrong.Upsert(ctx, []vectordata.ModelRecord{record}); return err }},
				{"get", func() error { _, err := wrong.Get(ctx, "kept", vectordata.RetrievalOptions{}); return err }},
				{"get_many", func() error { _, err := wrong.GetMany(ctx, []any{"kept"}, vectordata.RetrievalOptions{}); return err }},
				{"query", func() error { _, err := wrong.Query(ctx, nil, 5, vectordata.QueryOptions{}); return err }},
				{"query_iterator", func() error {
					for _, err := range wrong.QueryIter(ctx, nil, 5, vectordata.QueryOptions{}) {
						return err
					}
					return nil
				}},
				{"count", func() error { _, err := wrong.Count(ctx, nil); return err }},
				{"search", func() error {
					_, err := wrong.Search(ctx, []float32{1, 0}, 5, vectordata.ModelSearchOptions{VectorProperty: "Vector"})
					return err
				}},
				{"search_iterator", func() error {
					for _, err := range wrong.SearchIter(ctx, []float32{1, 0}, 5, vectordata.ModelSearchOptions{VectorProperty: "Vector"}) {
						return err
					}
					return nil
				}},
				{"binary_search", func() error {
					_, err := wrong.(vectordata.BinarySearcher).SearchBinary(ctx, bits, 5, vectordata.ModelSearchOptions{VectorProperty: "Bits"})
					return err
				}},
				{"sparse_search", func() error {
					_, err := wrong.(vectordata.SparseSearcher).SearchSparse(ctx, sparse, 5, vectordata.ModelSearchOptions{VectorProperty: "Sparse"})
					return err
				}},
				{"delete", func() error { _, err := wrong.Delete(ctx, []any{"kept"}); return err }},
			} {
				t.Run(tt.name, func(t *testing.T) {
					if err := tt.run(); !errors.Is(err, vectordata.ErrSchemaMismatch) {
						t.Fatalf("incompatible handle accepted: %v", err)
					}
				})
			}
			row, err := current.Get(ctx, "kept", vectordata.RetrievalOptions{})
			if err != nil || row.Data["Value"] != int64(42) {
				t.Fatalf("current record changed: %+v %v", row, err)
			}
			if n, err := current.Count(ctx, nil); err != nil || n != 1 {
				t.Fatalf("incompatible writes committed: %d %v", n, err)
			}
		})
	}
}

func TestSchemaChangesDuringEmbedding(t *testing.T) {
	for _, operation := range []string{"upsert", "search_text"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store := memory.New()
			original := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Text", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, SourceProperty: "Text"}}}
			replacement := original.Clone()
			replacement.Vectors[0].Dimensions = 3
			var current vectordata.ModelCollection
			calls := 0
			embedder := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
				calls++
				if err := store.DeleteCollection(ctx, "records"); err != nil {
					return nil, err
				}
				var err error
				current, err = store.EnsureModelCollection(ctx, "records", replacement, vectordata.ModelCollectionOptions{})
				if err != nil {
					return nil, err
				}
				return [][]float32{{1, 0}}, nil
			})
			old, err := store.EnsureModelCollection(ctx, "records", original, vectordata.ModelCollectionOptions{Embedder: embedder})
			if err != nil {
				t.Fatal(err)
			}
			input := []vectordata.ModelRecord{{Key: "bad", Data: map[string]any{"Text": "hello"}}}
			if operation == "upsert" {
				_, err = old.Upsert(ctx, input)
			} else {
				_, err = old.SearchText(ctx, "hello", 1, vectordata.ModelSearchOptions{VectorProperty: "Vector"})
			}
			if !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("schema changed during embedding: %v", err)
			}
			if _, err := old.Upsert(ctx, input); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("stale embedding write: %v", err)
			}
			if _, err := old.SearchText(ctx, "hello", 1, vectordata.ModelSearchOptions{VectorProperty: "Vector"}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("stale embedding query: %v", err)
			}
			if calls != 1 {
				t.Fatalf("stale handle called embedder: %d", calls)
			}
			if n, err := current.Count(ctx, nil); err != nil || n != 0 {
				t.Fatalf("replacement changed: %d %v", n, err)
			}
			// Runtime embedding configuration is not part of stored schema identity.
			valid, err := store.GetModelCollection("records", replacement, vectordata.ModelCollectionOptions{Embedder: embedder})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := valid.Insert(ctx, []vectordata.ModelRecord{{Key: "ok", Data: map[string]any{"Text": "hello"}, Vectors: map[string][]float32{"Vector": {1, 0, 0}}}}); err != nil {
				t.Fatal(err)
			}

		})
	}
}
