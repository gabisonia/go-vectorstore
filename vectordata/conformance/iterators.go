package conformance

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runReadIterators(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !caps.ReadIterators {
		t.Skip("native read iterators not advertised")
	}
	ctx := context.Background()
	store := factory(t)
	embeddingCalls := 0
	embedder := vectordata.InputEmbeddingFunc[any](func(ctx context.Context, _ []any) ([][]float32, error) {
		embeddingCalls++
		return [][]float32{{1, 0}}, ctx.Err()
	})
	d := vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID"},
		Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}},
		Vectors: []vectordata.VectorProperty{{Name: "Dense", Dimensions: 2, Metric: vectordata.DistanceL2}},
	}
	if slices.Contains(caps.VectorKinds, vectordata.VectorBinary) {
		d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 8})
	}
	if slices.Contains(caps.VectorKinds, vectordata.VectorSparse) {
		d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 2, Metric: vectordata.DistanceL2})
	}
	embeddingOptions := vectordata.ModelCollectionOptions{InputEmbedder: embedder}
	if !caps.InputEmbedding {
		embeddingOptions = vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
			embeddingCalls++
			return [][]float32{{1, 0}}, ctx.Err()
		})}
	}
	c, err := store.EnsureModelCollection(ctx, "iterator_records", d, embeddingOptions)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]vectordata.ModelRecord, 3)
	for i, key := range []string{"a", "b", "c"} {
		rows[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": "guide " + key}, Vectors: map[string][]float32{"Dense": {float32(i + 1), 0}}}
		if slices.Contains(caps.VectorKinds, vectordata.VectorBinary) {
			rows[i].BinaryVectors = map[string]vectordata.BinaryVector{"Bits": {Bytes: []byte{byte(1 << i)}, Dimensions: 8}}
		}
		if slices.Contains(caps.VectorKinds, vectordata.VectorSparse) {
			rows[i].SparseVectors = map[string]vectordata.SparseVector{"Sparse": {Dimensions: 2, Indices: []int{0}, Values: []float32{float32(i + 1)}}}
		}
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(vectordata.CollectionNameIterator); !ok {
		t.Fatal("native listing iterator missing")
	}
	if _, ok := c.(vectordata.ModelBatchIterator); !ok {
		t.Fatal("native batch iterator missing")
	}
	if _, ok := c.(vectordata.TextSearchIterator); !ok {
		t.Fatal("native text iterator missing")
	}
	if caps.InputEmbedding {
		if _, ok := c.(vectordata.InputSearchIterator); !ok {
			t.Fatal("native input iterator missing")
		}
	}
	t.Run("listing", func(t *testing.T) {
		seq := vectordata.ListCollectionNamesIter(ctx, store)
		if _, err := store.EnsureModelCollection(ctx, "created_after_iterator", d, vectordata.ModelCollectionOptions{}); err != nil {
			t.Fatal(err)
		}
		var names []string
		for name, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if !slices.Contains(names, "created_after_iterator") || !slices.Contains(names, c.Name()) {
			t.Fatalf("listing was eager: %v", names)
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		values, failures := 0, 0
		for _, err := range vectordata.ListCollectionNamesIter(cancelCtx, store) {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				failures++
				continue
			}
			values++
			cancel()
		}
		if values != 1 || failures != 1 {
			t.Fatalf("listing cancellation: values=%d failures=%d", values, failures)
		}
	})
	t.Run("batch", func(t *testing.T) {
		var found []string
		for row, err := range vectordata.GetManyIter(ctx, c, []any{"a", "missing", "a", "b"}, vectordata.RetrievalOptions{}) {
			if err != nil {
				t.Fatal(err)
			}
			if row.Vectors != nil {
				t.Fatal("default projection included vectors")
			}
			found = append(found, row.Key.(string))
		}
		slices.Sort(found)
		if !reflect.DeepEqual(found, []string{"a", "b"}) {
			t.Fatalf("missing/duplicate keys: %v", found)
		}
		values := 0
		for _, err := range vectordata.GetManyIter(ctx, c, []any{"a", 17}, vectordata.RetrievalOptions{}) {
			if !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("invalid late key: %v", err)
			}
			values++
		}
		if values != 1 {
			t.Fatalf("invalid batch emitted partial rows: %d", values)
		}
		for row, err := range vectordata.GetManyIter(ctx, c, []any{"a", "b"}, vectordata.RetrievalOptions{IncludeVectors: true}) {
			if err != nil {
				t.Fatal(err)
			}
			if len(row.Vectors["Dense"]) != 2 {
				t.Fatal("vectors omitted")
			}
			break
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		values, failures := 0, 0
		for _, err := range vectordata.GetManyIter(cancelCtx, c, []any{"a", "b", "c"}, vectordata.RetrievalOptions{}) {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				failures++
				continue
			}
			values++
			cancel()
		}
		if values != 1 || failures != 1 {
			t.Fatalf("batch cancellation: %d/%d", values, failures)
		}
	})
	t.Run("query", func(t *testing.T) {
		checkIteratorCancellation(t, ctx, len(rows), func(ctx context.Context) iter.Seq2[vectordata.ModelRecord, error] {
			return c.QueryIter(ctx, nil, len(rows), vectordata.QueryOptions{})
		})
	})
	t.Run("dense", func(t *testing.T) {
		checkIteratorCancellation(t, ctx, len(rows), func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return c.SearchIter(ctx, []float32{1, 0}, len(rows), vectordata.ModelSearchOptions{VectorProperty: "Dense"})
		})
	})
	if slices.Contains(caps.VectorKinds, vectordata.VectorBinary) {
		t.Run("binary", func(t *testing.T) {
			checkIteratorCancellation(t, ctx, len(rows), func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
				return c.(vectordata.BinarySearcher).SearchBinaryIter(ctx, rows[0].BinaryVectors["Bits"], len(rows), vectordata.ModelSearchOptions{VectorProperty: "Bits"})
			})
		})
	}
	if slices.Contains(caps.VectorKinds, vectordata.VectorSparse) {
		t.Run("sparse", func(t *testing.T) {
			checkIteratorCancellation(t, ctx, len(rows), func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
				return c.(vectordata.SparseSearcher).SearchSparseIter(ctx, rows[0].SparseVectors["Sparse"], len(rows), vectordata.ModelSearchOptions{VectorProperty: "Sparse"})
			})
		})
	}
	type operation struct {
		name   string
		embeds bool
		run    func(context.Context) iter.Seq2[vectordata.ModelSearchResult, error]
		load   func() ([]vectordata.ModelSearchResult, error)
	}
	opts := vectordata.ModelSearchOptions{VectorProperty: "Dense", IncludeVectors: true}
	ops := []operation{{"text", true, func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
		return vectordata.SearchTextIter(ctx, c, "guide", 3, opts)
	}, func() ([]vectordata.ModelSearchResult, error) { return c.SearchText(ctx, "guide", 3, opts) }}}
	if caps.InputEmbedding {
		ops = append(ops, operation{"input", true, func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
			return vectordata.SearchInputIter(ctx, c, []byte{1}, 3, opts)
		}, func() ([]vectordata.ModelSearchResult, error) {
			return c.(vectordata.InputSearcher).SearchInput(ctx, []byte{1}, 3, opts)
		}})
	}
	if caps.HybridSearch {
		if _, ok := c.(vectordata.HybridSearchIterator); !ok {
			t.Fatal("hybrid iterators missing")
		}
		h := vectordata.HybridSearchOptions{ModelSearchOptions: opts}
		ops = append(ops,
			operation{"hybrid", false, func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
				return vectordata.HybridSearchIter(ctx, c, []float32{1, 0}, "guide", 3, h)
			}, func() ([]vectordata.ModelSearchResult, error) {
				return c.(vectordata.HybridSearcher).HybridSearch(ctx, []float32{1, 0}, "guide", 3, h)
			}},
			operation{"hybrid_text", true, func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
				return vectordata.HybridSearchTextIter(ctx, c, "guide", "guide", 3, h)
			}, func() ([]vectordata.ModelSearchResult, error) {
				return c.(vectordata.HybridSearcher).HybridSearchText(ctx, "guide", "guide", 3, h)
			}},
		)
		if caps.InputEmbedding {
			if _, ok := c.(vectordata.InputHybridSearchIterator); !ok {
				t.Fatal("hybrid input iterator missing")
			}
			ops = append(ops, operation{"hybrid_input", true, func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
				return vectordata.HybridSearchInputIter(ctx, c, []byte{1}, "guide", 3, h)
			}, func() ([]vectordata.ModelSearchResult, error) {
				return c.(vectordata.InputHybridSearcher).HybridSearchInput(ctx, []byte{1}, "guide", 3, h)
			}})
		}
		if slices.Contains(caps.VectorKinds, vectordata.VectorBinary) {
			if _, ok := c.(vectordata.BinaryHybridSearchIterator); !ok {
				t.Fatal("binary hybrid iterator missing")
			}
			hb := h
			hb.VectorProperty = "Bits"
			v := rows[0].BinaryVectors["Bits"]
			ops = append(ops, operation{"hybrid_binary", false, func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
				return vectordata.HybridSearchBinaryIter(ctx, c, v, "guide", 3, hb)
			}, func() ([]vectordata.ModelSearchResult, error) {
				return c.(vectordata.BinaryHybridSearcher).HybridSearchBinary(ctx, v, "guide", 3, hb)
			}})
		}
		if slices.Contains(caps.VectorKinds, vectordata.VectorSparse) {
			if _, ok := c.(vectordata.SparseHybridSearchIterator); !ok {
				t.Fatal("sparse hybrid iterator missing")
			}
			hs := h
			hs.VectorProperty = "Sparse"
			v := rows[0].SparseVectors["Sparse"]
			ops = append(ops, operation{"hybrid_sparse", false, func(ctx context.Context) iter.Seq2[vectordata.ModelSearchResult, error] {
				return vectordata.HybridSearchSparseIter(ctx, c, v, "guide", 3, hs)
			}, func() ([]vectordata.ModelSearchResult, error) {
				return c.(vectordata.SparseHybridSearcher).HybridSearchSparse(ctx, v, "guide", 3, hs)
			}})
		}
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			want, err := op.load()
			if err != nil {
				t.Fatal(err)
			}
			before := embeddingCalls
			seq := op.run(ctx)
			if embeddingCalls != before {
				t.Fatal("iterator embedded eagerly")
			}
			for repeat := range 2 {
				got := []vectordata.ModelSearchResult{}
				for hit, err := range seq {
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, hit)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("slice/iterator mismatch: got=%v want=%v", got, want)
				}
				if op.embeds && embeddingCalls != before+repeat+1 {
					t.Fatalf("embedding not once per enumeration: %d", embeddingCalls-before)
				}
			}
			checkIteratorCancellation(t, ctx, len(want), op.run)
			check, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if n, err := c.Count(check, nil); err != nil || n != 3 {
				t.Fatalf("iterator leaked resources: %d, %v", n, err)
			}
		})
	}
}

func checkIteratorCancellation[T any](t *testing.T, ctx context.Context, total int, run func(context.Context) iter.Seq2[T, error]) {
	t.Helper()
	for _, cancelAt := range []int{0, 1, total} {
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		if cancelAt == 0 {
			cancel()
		}
		values, failures := 0, 0
		for _, err := range run(cancelCtx) {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				failures++
				continue
			}
			values++
			if values == cancelAt {
				cancel()
			}
		}
		cancel()
		if values != cancelAt || failures != 1 {
			t.Fatalf("cancelAt=%d values=%d failures=%d", cancelAt, values, failures)
		}
	}
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()
	for _, err := range run(stopCtx) {
		if err != nil {
			t.Fatal(err)
		}
		stop()
		break
	}
}
