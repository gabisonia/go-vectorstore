// Package rankedguides exercises one application query against typed search adapters.
package rankedguides

import (
	"context"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/embeddingsearch"
)

type Article struct {
	ID        uint64    `vectordata:"key,name=id"`
	Category  string    `vectordata:"data,index,name=category"`
	Direction []float32 `vectordata:"vector,dimensions=2,name=content_vector"`
}

func Articles() []Article {
	return []Article{{1, "guide", []float32{1, 0}}, {2, "guide", []float32{0, 1}}, {3, "news", []float32{1, 0}}, {4, "GUIDE", []float32{1, 0}}, {5, "guide ", []float32{1, 0}}}
}

// rankedGuides is application code shared by the three backend fixtures.
func rankedGuides(ctx context.Context, searcher vectordata.VectorSearcher[Article]) ([]vectordata.TypedModelSearchResult[Article], error) {
	opts := vectordata.ModelSearchOptions{VectorProperty: "Direction", Filter: vectordata.Eq(vectordata.Column("Category"), "guide")}
	var results []vectordata.TypedModelSearchResult[Article]
	for hit, err := range searcher.SearchIter(ctx, []float32{1, 0}, 2, opts) {
		if err != nil {
			return nil, err
		}
		results = append(results, hit)
	}
	return results, nil
}

func Check(t *testing.T, searcher vectordata.VectorSearcher[Article]) {
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
	def, err := vectordata.DefinitionFor[Article]()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	text, err := embeddingsearch.New(searcher, def, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(_ context.Context, inputs []string) ([][]float32, error) {
		calls++
		if len(inputs) != 1 || inputs[0] != "documentation" {
			t.Fatalf("embedding inputs %v", inputs)
		}
		return [][]float32{{1, 0}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	opts := vectordata.ModelSearchOptions{VectorProperty: "Direction", Filter: vectordata.Eq(vectordata.Column("Category"), "guide"), IncludeVectors: true, Skip: 1}
	count := 0
	for hit, err := range text.SearchTextIter(t.Context(), "documentation", 1, opts) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		v := hit.Item.Direction
		if hit.Item.ID != 2 || hit.Distance != 1 || hit.Score != 0 || len(v) != 2 || v[0] != 0 || v[1] != 1 {
			t.Fatalf("projected text hit: %+v", hit)
		}
	}
	if count != 1 || calls != 1 {
		t.Fatalf("text search: %d hits, %d embedding calls", count, calls)
	}
}
