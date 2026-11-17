//go:build integration

package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIntegrationNativeTextNoStem(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		for _, noStem := range []bool{false, true} {
			name := "stemmed"
			if noStem {
				name = "unstemmed"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				s := integrationStore(t, StoreOptions{NativeIndexes: true, StorageMode: mode})
				d := hybridDefinition(vectordata.IndexMethodFlat, vectordata.DistanceL2, vectordata.VectorFloat32)
				var err error
				d.Data[0], err = WithTextIndexOptions(d.Data[0], TextIndexOptions{NoStem: noStem})
				if err != nil {
					t.Fatal(err)
				}
				base, err := s.EnsureModelCollection(ctx, "nostem", d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c := base.(*Collection)
				rows := hybridRows("one")
				rows[0].Data[`Body."雪`] = "running"
				rows[1].Data[`Body."雪`] = "run"
				if _, err := c.Insert(ctx, rows); err != nil {
					t.Fatal(err)
				}
				threshold := 0.02
				opts := vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{ScoreThreshold: &threshold}}
				check := func() {
					t.Helper()
					hits, err := c.HybridSearch(ctx, []float32{0, 1}, "run", 4, opts)
					want := 2
					if noStem {
						want = 1
					}
					if err != nil || len(hits) != want {
						t.Fatalf("NoStem=%v hits=%v err=%v", noStem, hybridKeys(hits), err)
					}
					if noStem && hits[0].Record.Key != "b" {
						t.Fatalf("stemmed record matched: %v", hybridKeys(hits))
					}
				}
				check()
				if err := c.RebuildNativeIndexes(ctx); err != nil {
					t.Fatal(err)
				}
				check()
				other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace, NativeIndexes: true, StorageMode: mode})
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				reopened, err := other.EnsureModelCollection(ctx, c.name, d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if reopened.Definition().Data[0].ProviderAnnotations != c.Definition().Data[0].ProviderAnnotations {
					t.Fatal("annotation lost on reopen")
				}
				if !noStem {
					return
				}
				// Recreate the physical schema with stemming enabled: readiness must
				// reject drift before returning any vector-only hybrid result.
				m, _ := currentManifest(t, c)
				if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
					t.Fatal(err)
				}
				original := c.definition.Data[0].ProviderAnnotations
				c.definition.Data[0].ProviderAnnotations = ""
				err = c.createNativeIndex(ctx, m)
				c.definition.Data[0].ProviderAnnotations = original
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.HybridSearch(ctx, []float32{0, 1}, "run", 4, opts); !errors.Is(err, vectordata.ErrSchemaMismatch) {
					t.Fatalf("NOSTEM drift accepted: %v", err)
				}
				if err := c.RebuildNativeIndexes(ctx); err != nil {
					t.Fatal(err)
				}
				check()
			})
		}
	})
}
