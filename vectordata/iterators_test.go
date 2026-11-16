package vectordata_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type iteratorItem struct {
	ID string `vectordata:"key"`
}
type iteratorCodec struct {
	*vectordata.StructCodec[iteratorItem]
	calls  int
	failAt int
	cause  error
}

func (c *iteratorCodec) Decode(r vectordata.ModelRecord) (iteratorItem, error) {
	c.calls++
	if c.calls == c.failAt {
		return iteratorItem{}, c.cause
	}
	return c.StructCodec.Decode(r)
}

// Embedding only the original interfaces hides newer optional methods, as for
// a provider compiled against the older API. Extension methods return slices.
type sliceIteratorProvider struct {
	vectordata.ModelCollection
	loads int
}

func (s *sliceIteratorProvider) results() ([]vectordata.ModelSearchResult, error) {
	s.loads++
	return []vectordata.ModelSearchResult{{Record: vectordata.ModelRecord{Key: "a"}, Distance: 7, Score: 8, ScoreKind: vectordata.ScoreRRF}, {Record: vectordata.ModelRecord{Key: "b"}, Distance: 9, Score: 10, ScoreKind: vectordata.ScoreRRF}}, nil
}
func (s *sliceIteratorProvider) GetMany(context.Context, []any, vectordata.RetrievalOptions) ([]vectordata.ModelRecord, error) {
	s.loads++
	return []vectordata.ModelRecord{{Key: "a"}, {Key: "b"}}, nil
}
func (s *sliceIteratorProvider) SearchText(context.Context, string, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return s.results()
}
func (s *sliceIteratorProvider) SearchInput(context.Context, any, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return s.results()
}
func (s *sliceIteratorProvider) HybridSearch(context.Context, []float32, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return s.results()
}
func (s *sliceIteratorProvider) HybridSearchText(context.Context, string, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return s.results()
}
func (s *sliceIteratorProvider) HybridSearchInput(context.Context, any, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return s.results()
}
func (s *sliceIteratorProvider) HybridSearchBinary(context.Context, vectordata.BinaryVector, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return s.results()
}
func (s *sliceIteratorProvider) HybridSearchSparse(context.Context, vectordata.SparseVector, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return s.results()
}

func TestTypedIteratorFallbacksAreLazyAndStopDecoding(t *testing.T) {
	ctx := context.Background()
	base, err := memory.New().EnsureModelCollection(ctx, "fallback", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := &sliceIteratorProvider{ModelCollection: base}
	codec, err := vectordata.NewStructCodec[iteratorItem](nil)
	if err != nil {
		t.Fatal(err)
	}
	counting := &iteratorCodec{StructCodec: codec}
	typed, err := vectordata.NewTypedModelCollection[string, iteratorItem](p, counting)
	if err != nil {
		t.Fatal(err)
	}
	ops := []struct {
		name string
		run  func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error]
	}{
		{"text", func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error] {
			return typed.SearchTextIter(ctx, "query", 2, vectordata.ModelSearchOptions{})
		}},
		{"input", func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error] {
			return typed.SearchInputIter(ctx, []byte{1}, 2, vectordata.ModelSearchOptions{})
		}},
		{"hybrid", func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error] {
			return typed.HybridSearchIter(ctx, []float32{1}, "key", 2, vectordata.HybridSearchOptions{})
		}},
		{"hybrid_text", func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error] {
			return typed.HybridSearchTextIter(ctx, "query", "key", 2, vectordata.HybridSearchOptions{})
		}},
		{"hybrid_input", func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error] {
			return typed.HybridSearchInputIter(ctx, []byte{1}, "key", 2, vectordata.HybridSearchOptions{})
		}},
		{"hybrid_binary", func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error] {
			return typed.HybridSearchBinaryIter(ctx, vectordata.BinaryVector{}, "key", 2, vectordata.HybridSearchOptions{})
		}},
		{"hybrid_sparse", func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorItem], error] {
			return typed.HybridSearchSparseIter(ctx, vectordata.SparseVector{}, "key", 2, vectordata.HybridSearchOptions{})
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			beforeLoads, beforeDecodes := p.loads, counting.calls
			seq := op.run()
			if p.loads != beforeLoads || counting.calls != beforeDecodes {
				t.Fatal("eager load/decode")
			}
			for hit, err := range seq {
				if err != nil {
					t.Fatal(err)
				}
				if hit.Item.ID != "a" || hit.Distance != 7 || hit.Score != 8 || hit.ScoreKind != vectordata.ScoreRRF {
					t.Fatalf("mapping lost metadata: %+v", hit)
				}
				break
			}
			if p.loads != beforeLoads+1 || counting.calls != beforeDecodes+1 {
				t.Fatal("early termination decoded later records")
			}
			cause := errors.New("caller codec failed")
			counting.failAt, counting.cause = counting.calls+2, cause
			values, failures := 0, 0
			for _, err := range seq {
				if err != nil {
					if !errors.Is(err, cause) {
						t.Fatal(err)
					}
					failures++
					continue
				}
				values++
			}
			if values != 1 || failures != 1 || counting.calls != beforeDecodes+3 {
				t.Fatalf("decode failure: %d/%d", values, failures)
			}
			counting.failAt = 0
		})
	}
	beforeLoads, beforeDecodes := p.loads, counting.calls
	seq := typed.GetManyIter(ctx, []string{"a", "b"}, vectordata.RetrievalOptions{})
	if p.loads != beforeLoads {
		t.Fatal("batch iterator loaded eagerly")
	}
	for item, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		if item.ID != "a" {
			t.Fatal(item)
		}
		break
	}
	if counting.calls != beforeDecodes+1 {
		t.Fatal("batch iterator decoded after break")
	}
}

type nativeTextIterator struct {
	*sliceIteratorProvider
	started bool
	closed  bool
}

func (n *nativeTextIterator) SearchText(context.Context, string, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	panic("slice fallback used instead of native iterator")
}
func (n *nativeTextIterator) SearchTextIter(context.Context, string, int, vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		n.started = true
		defer func() { n.closed = true }()
		if !yield(vectordata.ModelSearchResult{Record: vectordata.ModelRecord{Key: "native"}}, nil) {
			return
		}
		panic("enumerated past early break")
	}
}

func TestIteratorNativePreferenceAndUnsupportedExtensions(t *testing.T) {
	ctx := context.Background()
	base, err := memory.New().EnsureModelCollection(ctx, "native", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n := &nativeTextIterator{sliceIteratorProvider: &sliceIteratorProvider{ModelCollection: base}}
	seq := vectordata.SearchTextIter(ctx, n, "query", 1, vectordata.ModelSearchOptions{})
	if n.started {
		t.Fatal("native iterator started eagerly")
	}
	for row, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		if row.Record.Key != "native" {
			t.Fatal(row)
		}
		break
	}
	if !n.closed {
		t.Fatal("native source was not closed")
	}
	codec, err := vectordata.NewStructCodec[iteratorItem](nil)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("decode failed")
	typed, err := vectordata.NewTypedModelCollection[string, iteratorItem](n, &iteratorCodec{StructCodec: codec, failAt: 1, cause: cause})
	if err != nil {
		t.Fatal(err)
	}
	n.closed = false
	for _, err := range typed.SearchTextIter(ctx, "query", 1, vectordata.ModelSearchOptions{}) {
		if !errors.Is(err, cause) {
			t.Fatal(err)
		}
	}
	if !n.closed {
		t.Fatal("codec error did not close the native source")
	}
	hidden := struct{ vectordata.ModelCollection }{base}
	for _, err := range vectordata.SearchInputIter(ctx, hidden, "query", 1, vectordata.ModelSearchOptions{}) {
		if !errors.Is(err, vectordata.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	for _, err := range vectordata.HybridSearchIter(ctx, hidden, []float32{1}, "key", 1, vectordata.HybridSearchOptions{}) {
		if !errors.Is(err, vectordata.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	store := memory.New()
	listing := vectordata.ListCollectionNamesIter(ctx, struct{ vectordata.CollectionManager }{store})
	if _, err := store.EnsureModelCollection(ctx, "late", base.Definition(), vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for name, err := range listing {
		if err != nil {
			t.Fatal(err)
		}
		if name != "late" {
			t.Fatal(name)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("listing fallback: %d", count)
	}
}
