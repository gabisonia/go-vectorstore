//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationBufferedIteratorCancellation(t *testing.T) {
	for _, size := range []int{1, 32} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg, err := pgxpool.ParseConfig(integrationDSN)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			store := newTestStore(t, pool)
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceL2}}}
			model, err := store.EnsureModelCollection(ctx, "canceled_model", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			records := make([]vectordata.ModelRecord, size)
			legacyRows := make([]vectordata.Record, size)
			for i := range records {
				key := fmt.Sprintf("%03d", i)
				records[i] = vectordata.ModelRecord{Key: key, Vectors: map[string][]float32{"V": {1, 0}}}
				legacyRows[i] = vectordata.Record{ID: key, Vector: []float32{1, 0}}
			}
			if _, err := model.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			legacy, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "canceled_record", Dimension: 2, Metric: vectordata.DistanceL2})
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Upsert(ctx, legacyRows); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"query", "search", "record_query"} {
				t.Run(method, func(t *testing.T) {
					request, stop := context.WithCancel(ctx)
					defer stop()
					hits, failures := 0, 0
					check := func(err error) bool {
						if err != nil {
							if !errors.Is(err, context.Canceled) {
								t.Errorf("cancellation cause: %v", err)
							}
							failures++
							return true
						}
						hits++
						stop()
						return true
					}
					switch method {
					case "query":
						for _, err := range model.QueryIter(request, nil, size, vectordata.QueryOptions{}) {
							check(err)
						}
					case "search":
						for _, err := range model.SearchIter(request, []float32{1, 0}, size, vectordata.ModelSearchOptions{}) {
							check(err)
						}
					case "record_query":
						for _, err := range legacy.(vectordata.RecordQueryer).QueryIter(request, nil, size, vectordata.QueryOptions{}) {
							check(err)
						}
					}
					if hits != 1 || failures != 1 {
						t.Fatalf("canceled iterator yielded %d records and %d errors", hits, failures)
					}
					// The cursor and any search transaction must be gone before reuse of the
					// only pooled connection. A leaked lease would hit this deadline.
					if _, err := model.Count(ctx, nil); err != nil {
						t.Fatalf("pool reuse after cancellation: %v", err)
					}
				})
			}
		})
	}
}
