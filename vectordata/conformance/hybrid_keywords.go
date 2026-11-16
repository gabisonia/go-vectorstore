package conformance

import (
	"context"
	"iter"
	"math"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type keywordModel struct {
	ID     string                  `vectordata:"key"`
	Body   string                  `vectordata:"data,fulltext"`
	Dense  []float32               `vectordata:"vector,dimensions=2,metric=l2"`
	Bits   vectordata.BinaryVector `vectordata:"vector,kind=binary,dimensions=8"`
	Sparse vectordata.SparseVector `vectordata:"vector,kind=sparse,dimensions=2,metric=l2"`
}

func runHybridKeywordTerms(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !caps.HybridKeywordTerms {
		t.Skip("keyword lists not advertised")
	}
	if !caps.HybridSearch || !caps.FullTextIndexing {
		t.Fatal("keyword-list support requires hybrid/full-text support")
	}
	ctx := context.Background()
	store := factory(t)
	d, err := vectordata.DefinitionFor[keywordModel]()
	if err != nil {
		t.Fatal(err)
	}
	d.Vectors = slices.DeleteFunc(d.Vectors, func(p vectordata.VectorProperty) bool {
		return p.Kind != "" && !slices.Contains(caps.VectorKinds, p.Kind)
	})
	var duringEmbedding func()
	generate := func(ctx context.Context) ([][]float32, error) {
		if duringEmbedding != nil {
			duringEmbedding()
		}
		return [][]float32{{0, 1}}, ctx.Err()
	}
	embedding := vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(ctx context.Context, _ []string) ([][]float32, error) { return generate(ctx) })}
	if caps.InputEmbedding {
		embedding = vectordata.ModelCollectionOptions{InputEmbedder: vectordata.InputEmbeddingFunc[any](func(ctx context.Context, _ []any) ([][]float32, error) { return generate(ctx) })}
	}
	c, err := vectordata.EnsureTypedModelCollection[string, keywordModel](ctx, store, "keyword_terms", &d, embedding)
	if err != nil {
		t.Fatal(err)
	}
	rows := []keywordModel{
		{ID: "a", Body: "neutral", Dense: []float32{0, 1}, Bits: vectordata.BinaryVector{Dimensions: 8, Bytes: []byte{0}}, Sparse: vectordata.SparseVector{Dimensions: 2}},
		{ID: "b", Body: "apple", Dense: []float32{3, 1}, Bits: vectordata.BinaryVector{Dimensions: 8, Bytes: []byte{1}}, Sparse: vectordata.SparseVector{Dimensions: 2, Indices: []int{0}, Values: []float32{3}}},
		{ID: "c", Body: "pear", Dense: []float32{6, 1}, Bits: vectordata.BinaryVector{Dimensions: 8, Bytes: []byte{3}}, Sparse: vectordata.SparseVector{Dimensions: 2, Indices: []int{0}, Values: []float32{6}}},
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	opts := vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{VectorProperty: "Dense"}, KeywordTerms: []string{"apple", "pear"}}
	check := func(t *testing.T, hits []vectordata.TypedModelSearchResult[keywordModel], err error, keys []string) {
		t.Helper()
		if err != nil || len(hits) != len(keys) {
			t.Fatalf("hybrid keyword results: %+v, %v", hits, err)
		}
		for i, key := range keys {
			if hits[i].Item.ID != key || hits[i].ScoreKind != vectordata.ScoreRRF {
				t.Fatalf("keyword rank: %+v", hits)
			}
		}
	}
	type read struct {
		name, property string
		slice          func(vectordata.HybridSearchOptions) ([]vectordata.TypedModelSearchResult[keywordModel], error)
		seq            func(vectordata.HybridSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[keywordModel], error]
	}
	reads := []read{
		{"dense", "Dense", func(o vectordata.HybridSearchOptions) ([]vectordata.TypedModelSearchResult[keywordModel], error) {
			return c.HybridSearch(ctx, []float32{0, 1}, "", 3, o)
		}, func(o vectordata.HybridSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[keywordModel], error] {
			return c.HybridSearchIter(ctx, []float32{0, 1}, "", 3, o)
		}},
		{"text", "Dense", func(o vectordata.HybridSearchOptions) ([]vectordata.TypedModelSearchResult[keywordModel], error) {
			return c.HybridSearchText(ctx, "query", "", 3, o)
		}, func(o vectordata.HybridSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[keywordModel], error] {
			return c.HybridSearchTextIter(ctx, "query", "", 3, o)
		}},
	}
	if caps.InputEmbedding {
		reads = append(reads, read{"input", "Dense", func(o vectordata.HybridSearchOptions) ([]vectordata.TypedModelSearchResult[keywordModel], error) {
			return c.HybridSearchInput(ctx, []byte{1}, "", 3, o)
		}, func(o vectordata.HybridSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[keywordModel], error] {
			return c.HybridSearchInputIter(ctx, []byte{1}, "", 3, o)
		}})
	}
	if slices.Contains(caps.VectorKinds, vectordata.VectorBinary) {
		reads = append(reads, read{"binary", "Bits", func(o vectordata.HybridSearchOptions) ([]vectordata.TypedModelSearchResult[keywordModel], error) {
			return c.HybridSearchBinary(ctx, rows[0].Bits, "", 3, o)
		}, func(o vectordata.HybridSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[keywordModel], error] {
			return c.HybridSearchBinaryIter(ctx, rows[0].Bits, "", 3, o)
		}})
	}
	if slices.Contains(caps.VectorKinds, vectordata.VectorSparse) {
		reads = append(reads, read{"sparse", "Sparse", func(o vectordata.HybridSearchOptions) ([]vectordata.TypedModelSearchResult[keywordModel], error) {
			return c.HybridSearchSparse(ctx, rows[0].Sparse, "", 3, o)
		}, func(o vectordata.HybridSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[keywordModel], error] {
			return c.HybridSearchSparseIter(ctx, rows[0].Sparse, "", 3, o)
		}})
	}
	for _, r := range reads {
		t.Run(r.name, func(t *testing.T) {
			o := opts
			o.VectorProperty = r.property
			hits, err := r.slice(o)
			check(t, hits, err, []string{"b", "c", "a"})
			want := []float64{1.0/62 + 1.0/61, 1.0/63 + 1.0/62, 1.0 / 61}
			for i, hit := range hits {
				if math.Abs(hit.Score-want[i]) > 1e-12 {
					t.Fatalf("RRF score: %v", hit)
				}
			}
			var iterHits []vectordata.TypedModelSearchResult[keywordModel]
			for hit, err := range r.seq(o) {
				if err != nil {
					t.Fatal(err)
				}
				iterHits = append(iterHits, hit)
			}
			check(t, iterHits, nil, []string{"b", "c", "a"})
		})
	}
	old := opts
	old.KeywordTerms = nil
	hits, err := c.HybridSearch(ctx, []float32{0, 1}, "apple pear", 3, old)
	check(t, hits, err, []string{"a", "b", "c"})
	filtered := opts
	filtered.Filter = vectordata.Ne(vectordata.Column("ID"), "b")
	hits, err = c.HybridSearch(ctx, []float32{0, 1}, "", 3, filtered)
	check(t, hits, err, []string{"c", "a"})
	threshold := opts
	threshold.ScoreThreshold = floatPointer(0.02)
	hits, err = c.HybridSearch(ctx, []float32{0, 1}, "", 3, threshold)
	check(t, hits, err, []string{"b", "c"})
	page := opts
	page.Skip = 1
	hits, err = c.HybridSearch(ctx, []float32{0, 1}, "", 1, page)
	check(t, hits, err, []string{"c"})
	// Snapshot keywords at enumeration before invoking caller embedding code.
	terms := []string{"apple", "pear"}
	snapshot := opts
	snapshot.KeywordTerms = terms
	duringEmbedding = func() { terms[0] = "absent" }
	hits, err = c.HybridSearchText(ctx, "query", "", 3, snapshot)
	check(t, hits, err, []string{"b", "c", "a"})
	duringEmbedding = nil
	if terms[0] != "absent" {
		t.Fatal("embedding callback did not run")
	}
}
