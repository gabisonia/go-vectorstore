package embeddingsearch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/embeddingsearch"
)

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
