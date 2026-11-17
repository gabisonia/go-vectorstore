package redis

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestHybridValidationBeforeIOAndEmbedding(t *testing.T) {
	s, err := New(StoreOptions{NativeIndexes: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}, {Name: "Other", Type: vectordata.TypeString, IsFullTextIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodFlat}}}}
	calls := 0
	base, err := s.GetModelCollection("validation", d, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		calls++
		return nil, errors.New("unexpected embed")
	})})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	nan := math.NaN()
	for _, tc := range []struct {
		name     string
		top      int
		keywords string
		change   func(*vectordata.HybridSearchOptions)
		want     error
	}{
		{name: "zero top", top: 0}, {name: "negative top", top: -1},
		{name: "negative skip", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.Skip = -1 }},
		{name: "overflow", top: math.MaxInt, change: func(o *vectordata.HybridSearchOptions) { o.Skip = 1 }},
		{name: "ambiguous text", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.TextProperty = "" }},
		{name: "missing text", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.TextProperty = "missing" }},
		{name: "missing vector", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.VectorProperty = "missing" }},
		{name: "small candidates", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.CandidateLimit = 1 }},
		{name: "negative candidates", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.CandidateLimit = -1 }},
		{name: "oversized candidates", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.CandidateLimit = nativeMaxDocuments + 1 }},
		{name: "negative RRF", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.RRFConstant = -1 }},
		{name: "NaN threshold", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.ScoreThreshold = &nan }},
		{name: "bad filter", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.Filter = vectordata.Eq(vectordata.Column("missing"), 1) }, want: vectordata.ErrInvalidFilter},
		{name: "provider options", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.ProviderOptions = struct{}{} }, want: vectordata.ErrUnsupported},
		{name: "FLAT tuning", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.ProviderOptions = SearchTuning{EFRuntime: 10} }},
		{name: "conflicting keyword forms", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.KeywordTerms = []string{"apple"} }},
		{name: "blank keywords", top: 1, keywords: " "}, {name: "NUL keywords", top: 1, keywords: "a\x00b"},
		{name: "invalid UTF8", top: 1, keywords: string([]byte{255})}, {name: "long keywords", top: 1, keywords: strings.Repeat("a", 16385)},
		{name: "many tokens", top: 1, keywords: strings.Repeat("word ", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := vectordata.HybridSearchOptions{TextProperty: "Body"}
			if tc.change != nil {
				tc.change(&o)
			}
			keywords := tc.keywords
			if keywords == "" {
				keywords = "apple"
			}
			want := tc.want
			if want == nil {
				want = vectordata.ErrInvalidSearchOptions
			}
			if _, err := c.HybridSearchText(context.Background(), "query", keywords, tc.top, o); !errors.Is(err, want) {
				t.Fatalf("error %v want %v", err, want)
			}
			if _, err := c.HybridSearch(context.Background(), []float32{0, 1}, keywords, tc.top, o); !errors.Is(err, want) {
				t.Fatalf("vector error %v want %v", err, want)
			}
			if calls != 0 {
				t.Fatalf("invalid request embedded %d times", calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.HybridSearchText(ctx, "query", "apple", 1, vectordata.HybridSearchOptions{TextProperty: "Body"}); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled hybrid embedded: %v calls=%d", err, calls)
	}
}
