package conformance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func sparseValue(t *testing.T, dim int, indices []int, values []float32) vectordata.SparseVector {
	t.Helper()
	v, err := vectordata.NewSparseVector(dim, indices, values)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func runSparseVectors(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !slices.Contains(caps.VectorKinds, vectordata.VectorSparse) {
		t.Skip("sparse vectors not advertised")
	}
	for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct, vectordata.DistanceL1} {
		t.Run(string(metric), func(t *testing.T) {
			if !slices.Contains(caps.DistanceMetrics, metric) {
				t.Skip("metric not advertised")
			}
			ctx := context.Background()
			store := factory(t)
			calls := 0
			embedder := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return [][]float32{{1, 0}}, nil })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}, {Name: "Group", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "Sparse", StorageName: `sparse";x`, Kind: vectordata.VectorSparse, Dimensions: 1_000_000_000, Metric: metric}, {Name: "Other", Kind: vectordata.VectorSparse, Dimensions: 4, Metric: vectordata.DistanceL2}, {Name: "Dense", Dimensions: 2}}}
			c, err := store.EnsureModelCollection(ctx, "sparse_vectors", d, vectordata.ModelCollectionOptions{Embedder: embedder})
			if err != nil {
				t.Fatal(err)
			}
			s, ok := c.(vectordata.SparseSearcher)
			if !ok {
				t.Fatal("advertised sparse interface missing")
			}
			query := sparseValue(t, 1_000_000_000, []int{0, 999999999}, []float32{1, 2})
			record := func(key string, v vectordata.SparseVector, group int) vectordata.ModelRecord {
				other := sparseValue(t, 4, nil, nil)
				if key == "c" {
					other = sparseValue(t, 4, []int{2}, []float32{1})
				}
				return vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": "alpha", "Group": group}, Vectors: map[string][]float32{"Dense": {1, 0}}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": v, "Other": other}}
			}
			input := record("a", vectordata.SparseVector{Dimensions: query.Dimensions, Indices: []int{0, 5, 999999999}, Values: []float32{1, 0, 2}}, 1)
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{input, record("b", sparseValue(t, query.Dimensions, []int{0, 999999999}, []float32{2, 1}), 2), record("c", sparseValue(t, query.Dimensions, []int{10}, []float32{1}), 1)}); err != nil {
				t.Fatal(err)
			}
			input.SparseVectors["Sparse"].Indices[0] = 3
			input.SparseVectors["Sparse"].Values[0] = 99
			row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
			if err != nil || row.Vectors != nil || row.SparseVectors != nil {
				t.Fatalf("default sparse projection: %v, %v", row, err)
			}
			row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || !reflect.DeepEqual(row.SparseVectors["Sparse"], query) || !reflect.DeepEqual(row.Vectors["Dense"], []float32{1, 0}) {
				t.Fatalf("mixed sparse round trip: %v, %v", row, err)
			}
			row.SparseVectors["Sparse"].Indices[0] = 3
			row.SparseVectors["Sparse"].Values[0] = 99
			row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || !reflect.DeepEqual(row.SparseVectors["Sparse"], query) {
				t.Fatal("sparse read aliases storage")
			}
			many, err := c.GetMany(ctx, []any{"a", "b", "missing"}, vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || len(many) != 2 || len(many[0].SparseVectors) != 2 {
				t.Fatalf("sparse batch retrieval: %v, %v", many, err)
			}
			opts := vectordata.ModelSearchOptions{VectorProperty: "Sparse", IncludeVectors: true}
			hits, err := s.SearchSparse(ctx, query, 3, opts)
			if err != nil || len(hits) != 3 {
				t.Fatalf("sparse search: %v, %v", hits, err)
			}
			want := map[vectordata.DistanceMetric][]float64{vectordata.DistanceCosine: {0, 0.2, 1}, vectordata.DistanceL2: {0, math.Sqrt(2), math.Sqrt(6)}, vectordata.DistanceL2Squared: {0, 2, 6}, vectordata.DistanceInnerProduct: {-5, -4, 0}, vectordata.DistanceL1: {0, 2, 4}}[metric]
			for i, hit := range hits {
				if hit.Record.Key != []string{"a", "b", "c"}[i] || math.Abs(hit.Distance-want[i]) > 1e-6 || math.Abs(hit.Score-vectordata.ScoreFromDistance(metric, want[i])) > 1e-6 || hit.ScoreKind != vectordata.ScoreSimilarity {
					t.Fatalf("sparse %s ranking: %v", metric, hits)
				}
			}
			for _, tt := range []struct {
				opts vectordata.ModelSearchOptions
				top  int
				want []string
			}{
				{vectordata.ModelSearchOptions{VectorProperty: "Sparse", Skip: 1}, 1, []string{"b"}},
				{vectordata.ModelSearchOptions{VectorProperty: "Sparse", DistanceThreshold: floatPointer((want[0] + want[1]) / 2)}, 3, []string{"a"}},
				{vectordata.ModelSearchOptions{VectorProperty: "Sparse", Filter: vectordata.Eq(vectordata.Column("Group"), 1)}, 3, []string{"a", "c"}},
				{vectordata.ModelSearchOptions{VectorProperty: "Sparse", ScoreThreshold: floatPointer((vectordata.ScoreFromDistance(metric, want[1]) + vectordata.ScoreFromDistance(metric, want[2])) / 2)}, 3, []string{"a", "b"}},
			} {
				hits, err := s.SearchSparse(ctx, query, tt.top, tt.opts)
				if err != nil || len(hits) != len(tt.want) {
					t.Fatalf("sparse options: %v, %v", hits, err)
				}
				for i, hit := range hits {
					if hit.Record.Key != tt.want[i] || hit.Record.Vectors != nil || hit.Record.SparseVectors != nil {
						t.Fatalf("sparse projection/paging: %v", hits)
					}
				}
			}
			hits, err = s.SearchSparse(ctx, sparseValue(t, 4, []int{2}, []float32{1}), 1, vectordata.ModelSearchOptions{VectorProperty: "Other"})
			if err != nil || len(hits) != 1 || hits[0].Record.Key != "c" {
				t.Fatalf("named sparse search: %v, %v", hits, err)
			}
			for _, err := range s.SearchSparseIter(ctx, query, 3, opts) {
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			if n, err := c.Count(ctx, nil); err != nil || n != 3 {
				t.Fatalf("sparse iterator cleanup: %d, %v", n, err)
			}
			if _, err := c.Search(ctx, []float32{1, 0}, 1, opts); !errors.Is(err, vectordata.ErrInvalidVector) {
				t.Fatalf("float query on sparse accepted: %v", err)
			}
			if _, err := s.SearchSparse(ctx, query, 1, vectordata.ModelSearchOptions{VectorProperty: "Dense"}); !errors.Is(err, vectordata.ErrInvalidVector) {
				t.Fatalf("sparse query on dense accepted: %v", err)
			}
			if _, err := c.SearchText(ctx, "alpha", 1, opts); !errors.Is(err, vectordata.ErrUnsupported) || calls != 0 {
				t.Fatalf("sparse text query invoked float embedder: %d, %v", calls, err)
			}
			badQuery := query
			badQuery.Dimensions--
			if _, err := s.SearchSparse(ctx, badQuery, 1, opts); !errors.Is(err, vectordata.ErrDimensionMismatch) {
				t.Fatalf("sparse query dimensions accepted: %v", err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := s.SearchSparse(canceled, query, 1, opts); !errors.Is(err, context.Canceled) {
				t.Fatalf("sparse cancellation: %v", err)
			}
			bad := record("a", vectordata.SparseVector{Dimensions: query.Dimensions, Indices: []int{1, 1}, Values: []float32{1, 2}}, 1)
			if _, err := c.Upsert(ctx, []vectordata.ModelRecord{record("new", query, 1), bad}); !errors.Is(err, vectordata.ErrInvalidVector) {
				t.Fatalf("bad sparse batch: %v", err)
			}
			if n, err := c.Count(ctx, nil); err != nil || n != 3 {
				t.Fatalf("partial sparse write: %d, %v", n, err)
			}
			row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || !reflect.DeepEqual(row.SparseVectors["Sparse"], query) {
				t.Fatalf("invalid batch overwrote sparse data: %v, %v", row, err)
			}
			wrong := record("bad", query, 1)
			wrong.SparseVectors["Dense"] = query
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{wrong}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("wrong sparse map accepted: %v", err)
			}
			zero := sparseValue(t, query.Dimensions, nil, nil)
			if metric == vectordata.DistanceCosine {
				if _, err := s.SearchSparse(ctx, zero, 1, opts); !errors.Is(err, vectordata.ErrInvalidVector) {
					t.Fatalf("zero sparse cosine query: %v", err)
				}
			} else {
				if _, err := c.Insert(ctx, []vectordata.ModelRecord{record("zero", zero, 1)}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SearchSparse(ctx, zero, 4, opts); err != nil {
					t.Fatal(err)
				}
			}
			if caps.HybridSearch {
				if _, err := c.(vectordata.HybridSearcher).HybridSearchText(ctx, "alpha", "alpha", 1, vectordata.HybridSearchOptions{ModelSearchOptions: opts}); !errors.Is(err, vectordata.ErrUnsupported) || calls != 0 {
					t.Fatalf("sparse hybrid text invoked embedder: %d, %v", calls, err)
				}
				h, ok := c.(vectordata.SparseHybridSearcher)
				if !ok {
					t.Fatal("sparse hybrid interface missing")
				}
				hits, err := h.HybridSearchSparse(ctx, query, "alpha", 1, vectordata.HybridSearchOptions{ModelSearchOptions: opts})
				if err != nil || len(hits) != 1 || hits[0].Record.Key != "a" || hits[0].ScoreKind != vectordata.ScoreRRF || hits[0].Record.SparseVectors == nil {
					t.Fatalf("sparse hybrid search: %v, %v", hits, err)
				}
			}
		})
	}
	t.Run("typed", func(t *testing.T) { runTypedSparse(t, factory(t), caps) })
}

type sparseModel struct {
	ID     string                  `vectordata:"key"`
	Body   string                  `vectordata:"data,fulltext"`
	Sparse vectordata.SparseVector `vectordata:"vector,kind=sparse,dimensions=1000000000"`
	Dense  []float32               `vectordata:"vector,dimensions=2"`
}

func runTypedSparse(t *testing.T, store vectordata.ModelStore, caps vectordata.ProviderCapabilities) {
	ctx := context.Background()
	c, err := vectordata.EnsureTypedModelCollection[string, sparseModel](ctx, store, "typed_sparse", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := sparseModel{ID: "a", Body: "alpha", Sparse: sparseValue(t, 1_000_000_000, []int{1, 999999999}, []float32{1, 2}), Dense: []float32{1, 0}}
	if _, err := c.Insert(ctx, []sparseModel{input}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !reflect.DeepEqual(got, input) {
		t.Fatalf("typed sparse round trip: %+v, %v", got, err)
	}
	opts := vectordata.ModelSearchOptions{VectorProperty: "Sparse", IncludeVectors: true}
	hits, err := c.SearchSparse(ctx, input.Sparse, 1, opts)
	if err != nil || len(hits) != 1 || !reflect.DeepEqual(hits[0].Item, input) {
		t.Fatalf("typed sparse search: %v, %v", hits, err)
	}
	for hit, err := range c.SearchSparseIter(ctx, input.Sparse, 1, opts) {
		if err != nil || !reflect.DeepEqual(hit.Item, input) {
			t.Fatalf("typed sparse iterator: %v, %v", hit, err)
		}
	}
	if caps.HybridSearch {
		if hits, err := c.HybridSearchSparse(ctx, input.Sparse, "alpha", 1, vectordata.HybridSearchOptions{ModelSearchOptions: opts}); err != nil || len(hits) != 1 || hits[0].Item.ID != "a" {
			t.Fatalf("typed sparse hybrid: %v, %v", hits, err)
		}
	}
}
