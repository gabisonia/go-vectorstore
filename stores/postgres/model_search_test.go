package postgres

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestHybridSearchTextValidatesBeforeEmbedding(t *testing.T) {
	definition, err := (vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID"},
		Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}, {Name: "Title", Type: vectordata.TypeString, IsFullTextIndexed: true}},
		Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	nan, inf := math.NaN(), math.Inf(1)
	tests := []struct {
		name     string
		top      int
		keywords string
		change   func(*vectordata.HybridSearchOptions)
		want     error
	}{
		{name: "zero top", top: 0},
		{name: "negative top", top: -1},
		{name: "negative skip", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.Skip = -1 }},
		{name: "paging overflow", top: int(^uint(0) >> 1), change: func(o *vectordata.HybridSearchOptions) { o.Skip = 1 }},
		{name: "empty keywords", top: 2, keywords: " "},
		{name: "unknown text property", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.TextProperty = "Missing" }},
		{name: "ambiguous text property", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.TextProperty = "" }},
		{name: "unknown vector", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.VectorProperty = "Missing" }},
		{name: "negative candidates", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.CandidateLimit = -1 }},
		{name: "insufficient candidates", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.Skip = 2; o.CandidateLimit = 3 }},
		{name: "negative RRF constant", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.RRFConstant = -1 }},
		{name: "NaN threshold", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.ScoreThreshold = &nan }},
		{name: "infinite threshold", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.DistanceThreshold = &inf }},
		{name: "unknown filter property", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.Filter = vectordata.Eq(vectordata.Column("Missing"), 1) }, want: vectordata.ErrInvalidFilter},
		{name: "invalid filter value", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.Filter = vectordata.Eq(vectordata.Column("Body"), 1) }, want: vectordata.ErrInvalidFilter},
		{name: "unknown provider options", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.ProviderOptions = struct{}{} }, want: vectordata.ErrUnsupported},
		{name: "invalid tuning", top: 2, change: func(o *vectordata.HybridSearchOptions) {
			o.ProviderOptions = SearchTuning{HNSW: &vectordata.HNSWSearchOptions{EfSearch: -1}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := &ModelCollection{name: "test", definition: definition, options: vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
				calls++
				return nil, errors.New("unexpected embedding")
			})}}
			opts := vectordata.HybridSearchOptions{TextProperty: "Body"}
			if tt.change != nil {
				tt.change(&opts)
			}
			keywords := tt.keywords
			if keywords == "" {
				keywords = "query"
			}
			want := tt.want
			if want == nil {
				want = vectordata.ErrInvalidSearchOptions
			}
			_, err := c.HybridSearchText(context.Background(), "query", keywords, tt.top, opts)
			if !errors.Is(err, want) {
				t.Fatalf("error %v, want %v", err, want)
			}
			if calls != 0 {
				t.Fatalf("invalid request invoked embedder %d times", calls)
			}
			// Vector input must reject the same options without needing a connection.
			_, err = c.HybridSearch(context.Background(), []float32{1, 0}, keywords, tt.top, opts)
			if !errors.Is(err, want) {
				t.Fatalf("vector search error %v, want %v", err, want)
			}
		})
	}
	t.Run("valid and canceled requests", func(t *testing.T) {
		calls := 0
		failure := errors.New("provider unavailable")
		c := &ModelCollection{name: "test", definition: definition, options: vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return nil, failure })}}
		opts := vectordata.HybridSearchOptions{TextProperty: "Body"}
		_, err := c.HybridSearchText(context.Background(), "query", "query", 2, opts)
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("valid request: calls=%d, error=%v", calls, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = c.HybridSearchText(ctx, "query", "query", 2, opts)
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("canceled request: calls=%d, error=%v", calls, err)
		}
	})
}
