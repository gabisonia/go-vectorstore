//go:build integration

package redis

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIntegrationNativeHybridKeywordGroups(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodFlat} {
			t.Run(string(method), func(t *testing.T) {
				ctx := context.Background()
				s := integrationStore(t, StoreOptions{NativeIndexes: true, StorageMode: mode})
				base, err := s.EnsureModelCollection(ctx, "groups", hybridDefinition(method, vectordata.DistanceL2, vectordata.VectorFloat32), vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c := base.(*Collection)
				var rows []vectordata.ModelRecord
				for i, body := range []string{"cloud", "costs", "cloud costs", "caching", "cloud costs caching", "雪 2026", "neutral"} {
					rows = append(rows, vectordata.ModelRecord{Key: body, Data: map[string]any{`Body."雪`: body, "Group": "keep", "JSON": map[string]any{"keep": true}, "Generation": "one"}, Vectors: map[string][]float32{"Vector": {float32(i), 1}}})
				}
				if _, err := c.Insert(ctx, rows); err != nil {
					t.Fatal(err)
				}
				threshold := 0.02 // Every keyword match also has a vector; vector-only scores stay below this.
				for _, tc := range []struct {
					name        string
					terms, want []string
				}{
					{"OR between AND groups", []string{"cloud costs", "caching"}, []string{"cloud costs", "caching", "cloud costs caching"}},
					{"literal operators", []string{"cloud | -costs*"}, []string{"cloud costs", "cloud costs caching"}},
					{"punctuation group", []string{"!!!", "caching"}, []string{"caching", "cloud costs caching"}},
					{"stopword group", []string{"the", "caching"}, []string{"caching", "cloud costs caching"}},
					{"only punctuation", []string{"!!!", "@|*"}, nil},
					{"only stopwords", []string{"the", "and"}, nil},
					{"unicode and numbers", []string{"雪 2026"}, []string{"雪 2026"}},
					{"overlapping groups", []string{"caching", "caching", "cloud costs caching"}, []string{"caching", "cloud costs caching"}},
				} {
					t.Run(tc.name, func(t *testing.T) {
						hits, err := c.HybridSearch(ctx, []float32{0, 1}, "", 7, vectordata.HybridSearchOptions{KeywordTerms: tc.terms, ModelSearchOptions: vectordata.ModelSearchOptions{ScoreThreshold: &threshold}})
						if err != nil {
							t.Fatal(err)
						}
						var got []string
						for _, hit := range hits {
							got = append(got, hit.Record.Key.(string))
						}
						sort.Strings(got)
						sort.Strings(tc.want)
						if !reflect.DeepEqual(got, tc.want) {
							t.Fatalf("got %v want %v", got, tc.want)
						}
					})
				}
			})
		}
	})
}

func TestIntegrationNativeHybridKeywordInputEnumeration(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		_, c := hybridCollection(t, mode, vectordata.IndexMethodFlat, vectordata.DistanceL2, vectordata.VectorFloat32)
		terms := []string{"apple"}
		calls := 0
		c.options.InputEmbedder = vectordata.InputEmbeddingFunc[[]byte](func(context.Context, [][]byte) ([][]float32, error) {
			calls++
			terms[0] = "absent"
			return [][]float32{{0, 1}}, nil
		})
		seq := c.HybridSearchInputIter(context.Background(), []byte{1}, "", 3, vectordata.HybridSearchOptions{KeywordTerms: terms, CandidateLimit: 3})
		if calls != 0 {
			t.Fatal("iterator embedded eagerly")
		}
		for i, want := range [][]any{{"b", "c", "a"}, {"a", "b", "c"}} {
			var got []any
			for hit, err := range seq {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, hit.Record.Key)
			}
			if !reflect.DeepEqual(got, want) || calls != i+1 {
				t.Fatalf("enumeration %d: got %v calls=%d", i, got, calls)
			}
		}
	})
}
