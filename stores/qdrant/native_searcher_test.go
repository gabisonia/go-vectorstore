package qdrant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type article struct {
	ID        uint64    `vectordata:"key"`
	Category  string    `vectordata:"data,index"`
	Direction []float32 `vectordata:"vector,dimensions=2"`
}

func articles() []article {
	return []article{{1, "guide", []float32{1, 0}}, {2, "guide", []float32{0, 1}}, {3, "news", []float32{1, 0}}}
}

// rankedGuides is application code shared by both backend fixtures.
func rankedGuides(ctx context.Context, searcher vectordata.VectorSearcher[article]) ([]vectordata.TypedModelSearchResult[article], error) {
	opts := vectordata.ModelSearchOptions{VectorProperty: "Direction", Filter: vectordata.Eq(vectordata.Column("Category"), "guide")}
	var results []vectordata.TypedModelSearchResult[article]
	for hit, err := range searcher.SearchIter(ctx, []float32{1, 0}, 2, opts) {
		if err != nil {
			return nil, err
		}
		results = append(results, hit)
	}
	return results, nil
}

func checkRankedGuides(t *testing.T, searcher vectordata.VectorSearcher[article]) {
	t.Helper()
	hits, err := rankedGuides(t.Context(), searcher)
	if err != nil || len(hits) != 2 {
		t.Fatalf("ranked guides: %+v, %v", hits, err)
	}
	for i, hit := range hits {
		if hit.Item.ID != uint64(i+1) || hit.Item.Category != "guide" || len(hit.Item.Direction) != 0 || hit.Distance != float64(i) || hit.Score != float64(1-i) || hit.ScoreKind != vectordata.ScoreSimilarity {
			t.Fatalf("rank %d: %+v", i, hit)
		}
	}
}

func TestMemoryRankedGuides(t *testing.T) {
	c, err := vectordata.EnsureTypedModelCollection[uint64, article](t.Context(), memory.New(), "articles", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(t.Context(), articles()); err != nil {
		t.Fatal(err)
	}
	checkRankedGuides(t, c)
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
