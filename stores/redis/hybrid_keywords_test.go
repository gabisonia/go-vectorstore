package redis

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestHybridKeywordGroups(t *testing.T) {
	for _, tc := range []struct {
		name, positional, query string
		terms                   []string
		params                  []any
	}{
		{"positional AND", "cloud costs", "$__kw0 $__kw1", nil, []any{"__kw0", "cloud", "__kw1", "costs"}},
		{"grouped OR", "", "($__kw0 $__kw1) | ($__kw2)", []string{"cloud costs", "caching"}, []any{"__kw0", "cloud", "__kw1", "costs", "__kw2", "caching"}},
		{"literal operators", "", "($__kw0 $__kw1) | ($__kw2)", []string{"apple | -pear*", "@雪"}, []any{"__kw0", "apple", "__kw1", "pear", "__kw2", "雪"}},
		{"punctuation group", " ", "($__kw0)", []string{"@|*()", "caching"}, []any{"__kw0", "caching"}},
		{"no tokens", "", nativeNone, []string{"!!!", "@|*()"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, params, err := hybridKeywords(tc.positional, tc.terms)
			if err != nil || query != tc.query || !reflect.DeepEqual(params, tc.params) {
				t.Fatalf("query=%q params=%v err=%v", query, params, err)
			}
		})
	}
}

func TestHybridInvalidKeywordGroupsBeforeEmbedding(t *testing.T) {
	s, err := New(StoreOptions{NativeIndexes: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}
	calls := 0
	base, err := s.GetModelCollection("validation", d, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		calls++
		return nil, errors.New("unexpected embedding")
	})})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	for _, terms := range [][]string{
		{}, {" "}, {"apple", ""}, {"a\x00b"}, {string([]byte{255})},
		{strings.Repeat("a", 16385)}, {strings.Repeat("a", 8193), strings.Repeat("b", 8192)},
		{strings.Repeat("word ", 65), strings.Repeat("word ", 64)},
		strings.Fields(strings.Repeat("a ", 129)),
	} {
		o := vectordata.HybridSearchOptions{KeywordTerms: terms}
		if _, err := c.HybridSearchText(context.Background(), "query", "", 1, o); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
			t.Fatalf("terms=%v: %v", terms, err)
		}
		if _, err := c.HybridSearch(context.Background(), []float32{0, 1}, "", 1, o); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
			t.Fatalf("dense error: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("embedded invalid groups %d times", calls)
	}
	terms := []string{"cloud costs", "caching"}
	cfg, err := c.validateHybrid(context.Background(), "", 1, vectordata.HybridSearchOptions{KeywordTerms: terms})
	if err != nil {
		t.Fatal(err)
	}
	terms[0] = "changed"
	if cfg.opts.KeywordTerms[0] != "cloud costs" || cfg.keywordParams[1] != "cloud" {
		t.Fatalf("caller changed frozen request: %+v", cfg)
	}
}

func TestHybridKeywordGroupLimitBoundaries(t *testing.T) {
	for _, terms := range [][]string{
		strings.Fields(strings.Repeat("a ", 128)),
		{strings.Repeat("a", 8192), strings.Repeat("b", 8192)},
	} {
		if _, _, err := hybridKeywords("", terms); err != nil {
			t.Fatalf("valid boundary rejected: %v", err)
		}
	}
}
