package embeddingsearch_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/embeddingsearch"
)

// A third-party dense searcher may finish immediately after its only result.
type singleHit struct{}

func (singleHit) SearchIter(context.Context, []float32, int, vectordata.ModelSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[int], error] {
	return func(yield func(vectordata.TypedModelSearchResult[int], error) bool) {
		yield(vectordata.TypedModelSearchResult[int]{Item: 7}, nil)
	}
}

func TestTextIteratorObservesCancellationOnLastDenseHit(t *testing.T) {
	def := vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID"},
		Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2}},
	}
	s, err := embeddingsearch.New(singleHit{}, def, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hits, failures := 0, 0
	for hit, err := range s.SearchTextIter(ctx, "query", 1, vectordata.ModelSearchOptions{}) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			failures++
			continue
		}
		if hit.Item != 7 {
			t.Fatalf("unexpected hit %+v", hit)
		}
		hits++
		cancel()
	}
	if hits != 1 || failures != 1 {
		t.Fatalf("got %d hits and %d errors", hits, failures)
	}
}

func TestValidationAndEmbeddingCancellation(t *testing.T) {
	type Document struct {
		ID        string    `vectordata:"key"`
		Direction []float32 `vectordata:"vector,dimensions=2"`
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, memory.New(), "docs", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	def, err := vectordata.DefinitionFor[Document]()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	cause := errors.New("embedder stopped")
	s, err := embeddingsearch.New(c, def, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		calls++
		cancel()
		return nil, cause
	})})
	if err != nil {
		t.Fatal(err)
	}
	check := func(opts vectordata.ModelSearchOptions, want ...error) {
		t.Helper()
		count := 0
		for _, err := range s.SearchTextIter(ctx, "query", 1, opts) {
			count++
			for _, e := range want {
				if !errors.Is(err, e) {
					t.Fatalf("got %v, want %v", err, e)
				}
			}
		}
		if count != 1 {
			t.Fatalf("got %d results", count)
		}
	}
	check(vectordata.ModelSearchOptions{VectorProperty: "unknown"}, vectordata.ErrInvalidSearchOptions)
	check(vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("unknown"), 1)}, vectordata.ErrInvalidFilter)
	check(vectordata.ModelSearchOptions{}, cause, context.Canceled)
	check(vectordata.ModelSearchOptions{}, context.Canceled)
	if calls != 1 {
		t.Fatalf("%d embeddings", calls)
	}
}
