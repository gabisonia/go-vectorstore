package conformance

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func binaryBits(t *testing.T, text string) vectordata.BinaryVector {
	t.Helper()
	v, err := vectordata.ParseBinaryVector(text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func runBinaryVectors(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !slices.Contains(caps.VectorKinds, vectordata.VectorBinary) {
		t.Skip("binary vectors not advertised")
	}
	ctx := context.Background()
	store := factory(t)
	calls := 0
	embedder := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return [][]float32{{1, 0}}, nil })
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}, {Name: "Group", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "Bits", StorageName: `bits";x`, Kind: vectordata.VectorBinary, Dimensions: 9}, {Name: "Other", Kind: vectordata.VectorBinary, Dimensions: 3}, {Name: "Dense", Dimensions: 2, Metric: vectordata.DistanceL2}}}
	c, err := store.EnsureModelCollection(ctx, "binary_vectors", d, vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	b, ok := c.(vectordata.BinarySearcher)
	if !ok {
		t.Fatal("advertised binary search is missing")
	}
	record := func(key, text, other string, group int) vectordata.ModelRecord {
		return vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": "alpha", "Group": group}, Vectors: map[string][]float32{"Dense": {1, 0}}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": binaryBits(t, text), "Other": binaryBits(t, other)}}
	}
	input := record("a", "000000000", "000", 1)
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{input, record("b", "100000000", "000", 2), record("c", "110000000", "000", 1), record("d", "111111111", "101", 2)}); err != nil {
		t.Fatal(err)
	}
	input.BinaryVectors["Bits"].Bytes[0] = 0xff
	row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || row.Vectors != nil || row.BinaryVectors != nil {
		t.Fatalf("default binary projection: %v, %v", row, err)
	}
	row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !reflect.DeepEqual(row.BinaryVectors["Bits"], binaryBits(t, "000000000")) || !reflect.DeepEqual(row.Vectors["Dense"], []float32{1, 0}) {
		t.Fatalf("mixed binary round trip: %v, %v", row, err)
	}
	row.BinaryVectors["Bits"].Bytes[0] = 0xff
	row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || row.BinaryVectors["Bits"].Bytes[0] != 0 {
		t.Fatal("binary reads alias storage")
	}
	query := binaryBits(t, "000000000")
	opts := vectordata.ModelSearchOptions{VectorProperty: "Bits", IncludeVectors: true}
	hits, err := b.SearchBinary(ctx, query, 4, opts)
	if err != nil || len(hits) != 4 {
		t.Fatalf("binary search: %v, %v", hits, err)
	}
	for i, want := range []float64{0, 1, 2, 9} {
		if hits[i].Distance != want || hits[i].Score != 1/(1+want) || hits[i].ScoreKind != vectordata.ScoreSimilarity || hits[i].Record.Key != []string{"a", "b", "c", "d"}[i] {
			t.Fatalf("Hamming ranking: %v", hits)
		}
	}
	for _, tt := range []struct {
		opts vectordata.ModelSearchOptions
		top  int
		want []string
	}{
		{vectordata.ModelSearchOptions{VectorProperty: "Bits", Skip: 1}, 2, []string{"b", "c"}},
		{vectordata.ModelSearchOptions{VectorProperty: "Bits", DistanceThreshold: floatPointer(1)}, 4, []string{"a", "b"}},
		{vectordata.ModelSearchOptions{VectorProperty: "Bits", ScoreThreshold: floatPointer(0.25)}, 4, []string{"a", "b", "c"}},
		{vectordata.ModelSearchOptions{VectorProperty: "Bits", Filter: vectordata.Eq(vectordata.Column("Group"), 1)}, 4, []string{"a", "c"}},
	} {
		hits, err := b.SearchBinary(ctx, query, tt.top, tt.opts)
		if err != nil || len(hits) != len(tt.want) {
			t.Fatalf("binary options: %v, %v", hits, err)
		}
		for i, hit := range hits {
			if hit.Record.Key != tt.want[i] || hit.Record.Vectors != nil || hit.Record.BinaryVectors != nil {
				t.Fatalf("binary filter/projection: %v", hits)
			}
		}
	}
	hits, err = b.SearchBinary(ctx, binaryBits(t, "101"), 1, vectordata.ModelSearchOptions{VectorProperty: "Other"})
	if err != nil || len(hits) != 1 || hits[0].Record.Key != "d" {
		t.Fatalf("named binary vector: %v, %v", hits, err)
	}
	for _, err := range b.SearchBinaryIter(ctx, query, 4, opts) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 4 {
		t.Fatalf("iterator cleanup: %d, %v", n, err)
	}
	if _, err := c.Search(ctx, []float32{1, 0}, 1, opts); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatalf("float query on binary accepted: %v", err)
	}
	if _, err := b.SearchBinary(ctx, query, 1, vectordata.ModelSearchOptions{VectorProperty: "Dense"}); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatalf("binary query on dense accepted: %v", err)
	}
	if _, err := c.SearchText(ctx, "alpha", 1, opts); !errors.Is(err, vectordata.ErrUnsupported) || calls != 0 {
		t.Fatalf("binary text query invoked float embedder: %d, %v", calls, err)
	}
	if _, err := b.SearchBinary(ctx, binaryBits(t, "00000000"), 1, opts); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("same-byte different-length query accepted: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.SearchBinary(canceled, query, 1, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("binary cancellation: %v", err)
	}
	bad := record("a", "000000000", "000", 1)
	bad.BinaryVectors["Bits"] = vectordata.BinaryVector{Bytes: []byte{0, 1}, Dimensions: 9}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{record("new", "000000000", "000", 1), bad}); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatalf("invalid binary batch: %v", err)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 4 {
		t.Fatalf("partial binary write: %d, %v", n, err)
	}
	for _, bad := range []vectordata.ModelRecord{{Key: "bad", Data: map[string]any{"Body": "alpha", "Group": 1}, Vectors: map[string][]float32{"Bits": {1, 0}}}, record("bad", "000000000", "000", 1)} {
		if bad.BinaryVectors != nil {
			bad.BinaryVectors["Dense"] = query
		}
		if _, err := c.Insert(ctx, []vectordata.ModelRecord{bad}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("wrong vector map accepted: %v", err)
		}
	}
	if caps.HybridSearch {
		if _, err := c.(vectordata.HybridSearcher).HybridSearchText(ctx, "alpha", "alpha", 1, vectordata.HybridSearchOptions{ModelSearchOptions: opts}); !errors.Is(err, vectordata.ErrUnsupported) || calls != 0 {
			t.Fatalf("binary hybrid text invoked float embedder: %d, %v", calls, err)
		}
		h, ok := c.(vectordata.BinaryHybridSearcher)
		if !ok {
			t.Fatal("binary hybrid interface missing")
		}
		hits, err := h.HybridSearchBinary(ctx, query, "alpha", 4, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{VectorProperty: "Bits", IncludeVectors: true, Filter: vectordata.Eq(vectordata.Column("Group"), 1)}})
		if err != nil || len(hits) != 2 || hits[0].Record.Key != "a" || hits[0].ScoreKind != vectordata.ScoreRRF || hits[0].Record.BinaryVectors == nil {
			t.Fatalf("binary hybrid: %v, %v", hits, err)
		}
	}
	t.Run("typed", func(t *testing.T) { runTypedBinary(t, store, caps) })
}

func floatPointer(v float64) *float64 { return &v }

type binaryModel struct {
	ID    string                  `vectordata:"key"`
	Body  string                  `vectordata:"data,fulltext"`
	Bits  vectordata.BinaryVector `vectordata:"vector,kind=binary,dimensions=9"`
	Dense []float32               `vectordata:"vector,dimensions=2"`
}

func runTypedBinary(t *testing.T, store vectordata.ModelStore, caps vectordata.ProviderCapabilities) {
	ctx := context.Background()
	c, err := vectordata.EnsureTypedModelCollection[string, binaryModel](ctx, store, "typed_binary", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := binaryModel{ID: "a", Body: "alpha", Bits: binaryBits(t, "101010101"), Dense: []float32{1, 0}}
	if _, err := c.Insert(ctx, []binaryModel{input}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !reflect.DeepEqual(got, input) {
		t.Fatalf("typed binary round trip: %+v, %v", got, err)
	}
	opts := vectordata.ModelSearchOptions{VectorProperty: "Bits", IncludeVectors: true}
	hits, err := c.SearchBinary(ctx, input.Bits, 1, opts)
	if err != nil || len(hits) != 1 || !reflect.DeepEqual(hits[0].Item, input) {
		t.Fatalf("typed binary search: %v, %v", hits, err)
	}
	for hit, err := range c.SearchBinaryIter(ctx, input.Bits, 1, opts) {
		if err != nil || !reflect.DeepEqual(hit.Item, input) {
			t.Fatalf("typed binary iterator: %v, %v", hit, err)
		}
	}
	if caps.HybridSearch {
		if hits, err := c.HybridSearchBinary(ctx, input.Bits, "alpha", 1, vectordata.HybridSearchOptions{ModelSearchOptions: opts}); err != nil || len(hits) != 1 || hits[0].Item.ID != "a" {
			t.Fatalf("typed binary hybrid: %v, %v", hits, err)
		}
	}
}
