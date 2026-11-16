package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func keywordTestCollection(t *testing.T, embedder vectordata.Embedder) *ModelCollection {
	t.Helper()
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	return &ModelCollection{store: &PostgresVectorStore{opts: DefaultStoreOptions()}, name: "keywords", definition: d, options: vectordata.ModelCollectionOptions{Embedder: embedder}}
}

func TestHybridKeywordTermsBindData(t *testing.T) {
	c := keywordTestCollection(t, nil)
	terms := []string{"cloud costs", "cache'); DROP TABLE keywords; --"}
	want := append([]string(nil), terms...)
	sql, args, _, err := c.hybridPlan([]float32{1, 0}, "", 2, vectordata.HybridSearchOptions{KeywordTerms: terms})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, terms[1]) || !strings.Contains(sql, "unnest($2::text[])") || !reflect.DeepEqual(args[1], want) {
		t.Fatalf("terms were not bound data: %s, %v", sql, args)
	}
	terms[0] = "changed"
	if !reflect.DeepEqual(args[1], want) {
		t.Fatal("query arguments retain caller terms")
	}
	oldSQL, oldArgs, _, err := c.hybridPlan([]float32{1, 0}, "cloud costs", 2, vectordata.HybridSearchOptions{})
	if err != nil || !strings.Contains(oldSQL, "plainto_tsquery('simple'::regconfig,$2)") || oldArgs[1] != "cloud costs" {
		t.Fatalf("existing string query changed: %s, %v, %v", oldSQL, oldArgs, err)
	}
}

func TestHybridKeywordTermsValidateBeforeEmbedding(t *testing.T) {
	calls := 0
	c := keywordTestCollection(t, vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return [][]float32{{1, 0}}, nil }))
	for _, tt := range []struct {
		keywords string
		terms    []string
	}{
		{"", []string{}}, {"", []string{" "}}, {"", []string{"valid", "\t"}},
		{"both", []string{"valid"}}, {"", []string{"bad\x00term"}}, {"", []string{string([]byte{0xff})}},
	} {
		_, err := c.HybridSearchText(context.Background(), "query", tt.keywords, 2, vectordata.HybridSearchOptions{KeywordTerms: tt.terms})
		if !errors.Is(err, vectordata.ErrInvalidSearchOptions) || calls != 0 {
			t.Fatalf("invalid terms reached embedding: %v, calls=%d", err, calls)
		}
	}
}
