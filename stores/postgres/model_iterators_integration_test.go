//go:build integration

package postgres

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationReadIteratorsStreamAndReleaseSingleConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(integrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := newTestStore(t, pool)
	d := vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID"},
		Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}, {Name: "Day", Type: vectordata.TypeDateOnly}},
		Vectors: []vectordata.VectorProperty{{Name: "Dense", Dimensions: 2, Metric: vectordata.DistanceL2}, {Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 8}, {Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 2, Metric: vectordata.DistanceL2}},
	}
	base, err := store.EnsureModelCollection(ctx, "stream", d, vectordata.ModelCollectionOptions{InputEmbedder: vectordata.InputEmbeddingFunc[any](func(context.Context, []any) ([][]float32, error) { return [][]float32{{1, 0}}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*ModelCollection)
	rows := []vectordata.ModelRecord{}
	for i, key := range []string{"a", "b"} {
		rows = append(rows, vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": "guide " + key, "Day": "2026-01-01"}, Vectors: map[string][]float32{"Dense": {float32(i + 1), 0}}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": {Bytes: []byte{byte(i + 1)}, Dimensions: 8}}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": {Dimensions: 2, Indices: []int{0}, Values: []float32{float32(i + 1)}}}})
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// A date outside the portable model's range fails decoding on the later row.
	// An iterator that buffers/decode-loads the whole page cannot return the first row.
	if _, err := pool.Exec(ctx, "UPDATE "+c.table()+" SET \"Day\"='10000-01-01'::date WHERE \"ID\"='b'"); err != nil {
		t.Fatal(err)
	}
	opts := vectordata.ModelSearchOptions{VectorProperty: "Dense", ProviderOptions: SearchTuning{HNSW: &vectordata.HNSWSearchOptions{EfSearch: 100}}}
	h := vectordata.HybridSearchOptions{ModelSearchOptions: opts}
	hb, hs := h, h
	hb.VectorProperty = "Bits"
	hs.VectorProperty = "Sparse"
	ops := []struct {
		name string
		run  func(context.Context) iter.Seq2[vectordata.ModelSearchResult, error]
	}{
		{"batch", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return func(yield func(vectordata.ModelSearchResult, error) bool) {
				for row, err := range c.GetManyIter(ctx, []any{"a", "b"}, vectordata.RetrievalOptions{}) {
					if !yield(vectordata.ModelSearchResult{Record: row}, err) || err != nil {
						return
					}
				}
			}
		}},
		{"text", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.SearchTextIter(ctx, "guide", 2, opts)
		}},
		{"input", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.SearchInputIter(ctx, []byte{1}, 2, opts)
		}},
		{"hybrid", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.HybridSearchIter(ctx, []float32{1, 0}, "guide", 2, h)
		}},
		{"hybrid_text", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.HybridSearchTextIter(ctx, "guide", "guide", 2, h)
		}},
		{"hybrid_input", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.HybridSearchInputIter(ctx, []byte{1}, "guide", 2, h)
		}},
		{"hybrid_binary", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.HybridSearchBinaryIter(ctx, rows[0].BinaryVectors["Bits"], "guide", 2, hb)
		}},
		{"hybrid_sparse", func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.HybridSearchSparseIter(ctx, rows[0].SparseVectors["Sparse"], "guide", 2, hs)
		}},
	}
	checkReleased := func(t *testing.T) {
		t.Helper()
		check, done := context.WithTimeout(ctx, time.Second)
		defer done()
		if count, err := c.Count(check, nil); err != nil || count != 2 {
			t.Fatalf("connection leaked: %d, %v", count, err)
		}
		var ef string
		if err := pool.QueryRow(check, "SELECT current_setting('hnsw.ef_search')").Scan(&ef); err != nil || ef == "100" {
			t.Fatalf("request setting leaked: ef=%q, error=%v", ef, err)
		}
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			seen := 0
			for hit, err := range op.run(ctx) {
				if err != nil {
					t.Fatal(err)
				}
				if hit.Record.Key != "a" {
					t.Fatalf("unexpected first record: %v", hit)
				}
				seen++
				break
			}
			if seen != 1 {
				t.Fatal("first row was not streamed")
			}
			checkReleased(t)
			seen, failures := 0, 0
			for _, err := range op.run(ctx) {
				if err != nil {
					if !errors.Is(err, vectordata.ErrInvalidRecord) {
						t.Fatal(err)
					}
					failures++
					continue
				}
				seen++
			}
			if seen != 1 || failures != 1 {
				t.Fatalf("late decoder error: %d/%d", seen, failures)
			}
			checkReleased(t)
		})
	}
	for _, err := range store.ListCollectionNamesIter(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	checkReleased(t)
	if got, err := c.GetMany(ctx, []any{"a", "b"}, vectordata.RetrievalOptions{}); err == nil || got != nil {
		t.Fatalf("slice API exposed partial results: %v, %v", got, err)
	}
}
