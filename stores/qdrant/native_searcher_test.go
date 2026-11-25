package qdrant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/parity/rankedguides"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/google/uuid"
)

func TestNativeModelPayloadMapping(t *testing.T) {
	type document struct {
		ID        uuid.UUID `vectordata:"key,type=uuid"`
		Revision  int64     `vectordata:"data,name=revision_no"`
		Direction []float32 `vectordata:"vector,dimensions=2,name=content_vector"`
	}
	id := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"result":{"points":[{"id":"%s","score":1,"payload":{"revision_no":9007199254740993},"vector":{"content_vector":[1,0]}}]}}`, id)
	}))
	defer server.Close()
	store, err := New(StoreOptions{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	points := &NativePoints[uuid.UUID]{store: store, path: "/collections/docs", dimensions: map[string]int{"content_vector": 2}}
	searcher, err := NewNativeModelSearcher[document](points, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for hit, err := range searcher.SearchIter(t.Context(), []float32{1, 0}, 1, vectordata.ModelSearchOptions{IncludeVectors: true}) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if hit.Item.ID != id || hit.Item.Revision != 9007199254740993 || len(hit.Item.Direction) != 2 {
			t.Fatalf("decoded %+v", hit.Item)
		}
	}
	if count != 1 {
		t.Fatalf("%d hits", count)
	}
	points.dimensions["content_vector"] = 3
	if _, err := NewNativeModelSearcher[document](points, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("dimension mismatch: %v", err)
	}
}

func TestMemoryRankedGuides(t *testing.T) {
	c, err := vectordata.EnsureTypedModelCollection[uint64, rankedguides.Article](t.Context(), memory.New(), "articles", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(t.Context(), rankedguides.Articles()); err != nil {
		t.Fatal(err)
	}
	rankedguides.Check(t, c)
}

func TestNativeSearcherIteration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"points":[{"id":1,"score":1,"vector":{"Direction":[1,0]}},{"id":2,"score":0}]}}`)
	}))
	defer server.Close()
	s, err := New(StoreOptions{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &NativePoints[uint64]{store: s, path: "/collections/articles", dimensions: map[string]int{"Direction": 2}}
	calls := 0
	bad := errors.New("decoder failed")
	searcher, err := NewNativeVectorSearcher(c, "Direction", func(p NamedPoint[uint64]) (uint64, error) {
		calls++
		if p.ID == 2 {
			return 0, bad
		}
		if len(p.Vectors["Direction"]) != 2 {
			t.Fatal("missing projected vector")
		}
		return p.ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seq := searcher.SearchIter(t.Context(), []float32{1, 0}, 2, vectordata.ModelSearchOptions{IncludeVectors: true})
	for hit, err := range seq {
		if err != nil || hit.Item != 1 {
			t.Fatalf("first hit: %+v %v", hit, err)
		}
		break
	}
	if calls != 1 {
		t.Fatalf("decoded %d hits", calls)
	}
	var last error
	for _, err := range seq {
		last = err
	}
	if !errors.Is(last, bad) {
		t.Fatalf("decode error: %v", last)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, err := range searcher.SearchIter(ctx, nil, 0, vectordata.ModelSearchOptions{}) {
		last = err
	}
	if !errors.Is(last, context.Canceled) {
		t.Fatalf("cancellation: %v", last)
	}
	for _, err := range searcher.SearchIter(t.Context(), []float32{1, 0}, 1, vectordata.ModelSearchOptions{ProviderOptions: true}) {
		last = err
	}
	if !errors.Is(last, vectordata.ErrUnsupported) {
		t.Fatalf("unsupported options: %v", last)
	}
}
