package sqlite_test

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type iteratorDocument struct {
	ID     string    `vectordata:"key"`
	Body   string    `vectordata:"data"`
	Vector []float32 `vectordata:"vector,dimensions=2,metric=l2"`
}
type reentrantIteratorCodec struct {
	*vectordata.StructCodec[iteratorDocument]
	base  vectordata.ModelCollection
	calls int
}

func (c *reentrantIteratorCodec) Decode(row vectordata.ModelRecord) (iteratorDocument, error) {
	c.calls++
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Writing through the same store would deadlock if the iterator held its
	// one SQLite connection while running caller code.
	if _, err := c.base.Upsert(ctx, []vectordata.ModelRecord{{Key: "callback", Data: map[string]any{"Body": "callback"}, Vectors: map[string][]float32{"Vector": {99, 0}}}}); err != nil {
		return iteratorDocument{}, err
	}
	return c.StructCodec.Decode(row)
}

func TestReadIteratorsReleaseConnectionBeforeCallerCode(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ":memory:", sqlite.StoreOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil })})
	codec, err := vectordata.NewStructCodec[iteratorDocument](nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.EnsureModelCollection(ctx, "documents", codec.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Body": "one"}, Vectors: map[string][]float32{"Vector": {1, 0}}}, {Key: "b", Data: map[string]any{"Body": "two"}, Vectors: map[string][]float32{"Vector": {2, 0}}}}); err != nil {
		t.Fatal(err)
	}
	checking := &reentrantIteratorCodec{StructCodec: codec, base: base}
	typed, err := vectordata.NewTypedModelCollection[string, iteratorDocument](base, checking)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorDocument], error]{
		func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorDocument], error] {
			return typed.SearchTextIter(ctx, "query", 2, vectordata.ModelSearchOptions{})
		},
		func() iter.Seq2[vectordata.TypedModelSearchResult[iteratorDocument], error] {
			return typed.SearchInputIter(ctx, "query", 2, vectordata.ModelSearchOptions{})
		},
	} {
		before := checking.calls
		for _, err := range run() {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if checking.calls != before+1 {
			t.Fatal("decoded after early termination")
		}
	}
	for _, err := range typed.GetManyIter(ctx, []string{"a", "b"}, vectordata.RetrievalOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	for name, err := range store.ListCollectionNamesIter(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		check, cancel := context.WithTimeout(ctx, time.Second)
		_, err = store.CollectionExists(check, name)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	for _, filter := range []vectordata.Filter{vectordata.Eq(vectordata.Column("ID"), "a"), vectordata.In(vectordata.Column("ID"), "b", "a")} {
		before := checking.calls
		for _, err := range typed.QueryIter(ctx, filter, 2, vectordata.QueryOptions{}) {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if checking.calls != before+1 {
			t.Fatal("key query decoded after early termination")
		}
	}
}
