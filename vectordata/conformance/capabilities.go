package conformance

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// runCapabilities exercises advertised model features, beyond checking metadata.
func runCapabilities(t *testing.T, factory Factory) {
	store := factory(t)
	provider, ok := store.(vectordata.CapabilityProvider)
	if !ok {
		t.Skip("provider does not expose optional capability metadata")
	}
	caps := provider.Capabilities()
	t.Run("native_data", func(t *testing.T) { runNativeData(t, factory, caps) })
	t.Run("vector_precisions", func(t *testing.T) { runVectorPrecisions(t, factory, caps) })
	if caps.Backend == "" || len(caps.KeyTypes) == 0 {
		t.Fatal("capabilities must identify the backend and supported keys")
	}
	ctx := context.Background()
	examples := map[vectordata.DataType]any{
		vectordata.TypeString: "quiet hotel", vectordata.TypeInt64: int64(42), vectordata.TypeFloat64: 2.5,
		vectordata.TypeBool: true, vectordata.TypeTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		vectordata.TypeStringArray: []string{"news"}, vectordata.TypeJSON: map[string]any{"rank": 1},
		vectordata.TypeUUID: "8f516db6-38ca-4e8f-ad79-d71b015f5419", vectordata.TypeBytes: []byte{0, 1, 255},
	}
	keys := map[vectordata.KeyType]any{vectordata.KeyString: "key", vectordata.KeyInt64: int64(42), vectordata.KeyUUID: "8f516db6-38ca-4e8f-ad79-d71b015f5419"}
	for _, kind := range caps.KeyTypes {
		t.Run(string(kind), func(t *testing.T) {
			key, ok := keys[kind]
			if !ok {
				t.Fatalf("no conformance sample for key type %q", kind)
			}
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: kind}}
			record := vectordata.ModelRecord{Key: key, Data: map[string]any{}, Vectors: map[string][]float32{}}
			for _, kind := range caps.DataTypes {
				value, ok := examples[kind]
				if !ok {
					t.Fatalf("no conformance sample for data type %q", kind)
				}
				property := vectordata.DataProperty{Name: "field_" + string(kind), Type: kind, IsIndexed: slices.Contains(caps.IndexedDataTypes, kind), IsFullTextIndexed: caps.FullTextIndexing && kind == vectordata.TypeString}
				d.Data = append(d.Data, property)
				record.Data[property.Name] = value
			}
			for i, metric := range caps.DistanceMetrics {
				vector := vectordata.VectorProperty{Name: fmt.Sprintf("Vector%d", i), Dimensions: 2, Metric: metric}
				d.Vectors = append(d.Vectors, vector)
				record.Vectors[vector.Name] = []float32{1, 0}
			}
			c, err := store.EnsureModelCollection(ctx, "capabilities_"+string(kind), d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatalf("advertised definition failed: %v", err)
			}
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{record}); err != nil {
				t.Fatalf("advertised data/key types failed: %v", err)
			}
			got, err := c.Get(ctx, key, vectordata.RetrievalOptions{})
			if err != nil || got.Key != key {
				t.Fatalf("native key round trip: %+v, %v", got, err)
			}
			for _, vector := range d.Vectors {
				results, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: vector.Name})
				if err != nil || len(results) != 1 || results[0].Record.Key != key {
					t.Fatalf("advertised metric %s failed: %v, %v", vector.Metric, results, err)
				}
			}
			if caps.MigrationPlanning {
				planner, ok := store.(vectordata.MigrationPlanner)
				if !ok {
					t.Fatal("advertised migration planner is missing")
				}
				plan, err := planner.PlanMigration(ctx, c.Name(), d)
				if err != nil || plan.HasManualWork() || len(plan.Steps) != 0 || plan.Finalization != nil {
					t.Fatalf("planning the registered model should be a no-op: %+v, %v", plan, err)
				}
			}
			if caps.MigrationApplication {
				applier, ok := store.(vectordata.MigrationApplier)
				if !ok || !caps.MigrationPlanning {
					t.Fatal("advertised migration application lacks the required interfaces")
				}
				desired := c.Definition()
				desired.Data = append(desired.Data, vectordata.DataProperty{Name: "MigrationAdded", Type: vectordata.TypeString, Optional: true})
				plan, err := store.(vectordata.MigrationPlanner).PlanMigration(ctx, c.Name(), desired)
				if err != nil {
					t.Fatal(err)
				}
				if err := applier.ApplyMigration(ctx, plan); err != nil {
					t.Fatalf("advertised additive migration failed: %v", err)
				}
				updated, err := store.EnsureModelCollection(ctx, c.Name(), desired, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if row, err := updated.Get(ctx, key, vectordata.RetrievalOptions{}); err != nil || row.Data["MigrationAdded"] != nil {
					t.Fatalf("migrated row: %+v, %v", row, err)
				}
			}
			if caps.HybridSearch {
				hybrid, ok := c.(vectordata.HybridSearcher)
				if !ok {
					t.Fatal("advertised hybrid search interface is missing")
				}
				if len(d.Vectors) == 0 || !caps.FullTextIndexing {
					t.Fatal("hybrid search needs vectors and full text")
				}
				results, err := hybrid.HybridSearch(ctx, []float32{1, 0}, "quiet", 1, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{VectorProperty: d.Vectors[0].Name}})
				if err != nil || len(results) != 1 || results[0].Record.Key != key {
					t.Fatalf("advertised hybrid search failed: %v, %v", results, err)
				}
			}
		})
	}
	for _, kind := range caps.GeneratedKeyTypes {
		if !slices.Contains(caps.KeyTypes, kind) {
			t.Fatalf("generated key type %s is not a supported key", kind)
		}
		c, err := store.EnsureModelCollection(ctx, "capabilities_generated_"+string(kind), vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: kind, AutoGenerated: true}}, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		keys, err := c.Insert(ctx, []vectordata.ModelRecord{{}, {}})
		if err != nil || len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("advertised generated keys failed: %v, %v", keys, err)
		}
		if caps.MigrationPlanning {
			plan, err := store.(vectordata.MigrationPlanner).PlanMigration(ctx, c.Name(), c.Definition())
			if err != nil || plan.HasManualWork() || len(plan.Steps) != 0 || plan.Finalization != nil {
				t.Fatalf("generated-key schema planning should be a no-op: %+v, %v", plan, err)
			}
		}
	}
	for _, method := range caps.VectorIndexMethods {
		if len(caps.DistanceMetrics) == 0 {
			t.Fatal("vector indexes require a supported distance metric")
		}
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: caps.KeyTypes[0]}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: caps.DistanceMetrics[0], Index: &vectordata.VectorIndexOptions{Method: method}}}}
		if _, err := store.EnsureModelCollection(ctx, "capabilities_index_"+string(method), d, vectordata.ModelCollectionOptions{}); err != nil {
			t.Fatalf("advertised index %s failed: %v", method, err)
		}
	}
}
