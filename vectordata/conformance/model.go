// Package conformance provides shared model-provider tests. Run these in addition
// to provider-specific integration tests; it does not substitute for real databases.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Factory func(*testing.T) vectordata.ModelStore

func definition() vectordata.CollectionDefinition {
	return vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID", StorageName: "record_id", Type: vectordata.KeyString},
		Data:    []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString, IsFullTextIndexed: true}, {Name: "Rank", Type: vectordata.TypeInt64, IsIndexed: true}, {Name: "Tags", Type: vectordata.TypeStringArray}, {Name: "Details", Type: vectordata.TypeJSON, Optional: true}, {Name: "Price", Type: vectordata.TypeFloat64, Optional: true}, {Name: "Active", Type: vectordata.TypeBool, Optional: true, IsIndexed: true}, {Name: "Created", Type: vectordata.TypeTime, Optional: true, IsIndexed: true}},
		Vectors: []vectordata.VectorProperty{{Name: "Text", Dimensions: 3, Metric: vectordata.DistanceCosine, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}, {Name: "Image", Dimensions: 2, Metric: vectordata.DistanceL2}},
	}
}
func records() []vectordata.ModelRecord {
	return []vectordata.ModelRecord{
		{Key: "a", Data: map[string]any{"Title": "quiet mountain hotel", "Rank": 1, "Price": float32(0.1), "Active": true, "Created": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "Tags": []string{"news", "quiet"}, "Details": map[string]any{"price": int64(9007199254740993), "tags": []any{"quiet", nil}}}, Vectors: map[string][]float32{"Text": {1, 0, 0}, "Image": {0, 0}}},
		{Key: "b", Data: map[string]any{"Title": "city hotel", "Rank": 2, "Price": float64(2.5), "Active": false, "Created": time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "Tags": []string{"news"}, "Details": map[string]any{"price": "2", "nil": nil}}, Vectors: map[string][]float32{"Text": {0.8, 0.2, 0}, "Image": {1, 1}}},
		{Key: "c", Data: map[string]any{"Title": "ocean hotel", "Rank": 3, "Tags": []string{"city"}, "Details": nil}, Vectors: map[string][]float32{"Text": {0, 1, 0}, "Image": {3, 3}}},
	}
}
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("capabilities", func(t *testing.T) { runCapabilities(t, factory) })
	t.Run("array_sets", func(t *testing.T) { runArraySets(t, factory) })
	t.Run("named_string_arrays", func(t *testing.T) { runNamedStringArrays(t, factory) })
	t.Run("typed_codec_context", func(t *testing.T) { runTypedCodecContext(t, factory) })
	t.Run("normalization_errors", func(t *testing.T) { runNormalizationErrors(t, factory) })
	t.Run("json_scalars", func(t *testing.T) { runJSONScalars(t, factory) })
	t.Run("json_decode", func(t *testing.T) { runJSONDecode(t, factory) })
	t.Run("native_decode", func(t *testing.T) { runNativeDecode(t, factory) })
	t.Run("filter_edges", func(t *testing.T) { runFilterEdges(t, factory) })
	t.Run("provider_annotations", func(t *testing.T) { runProviderAnnotations(t, factory) })
	t.Run("single_record_operations", func(t *testing.T) { runSingleRecordOperations(t, factory) })
	t.Run("generated_key_collision", func(t *testing.T) { runGeneratedKeyCollision(t, factory) })
	t.Run("lifecycle_crud_filters_search", func(t *testing.T) {
		ctx := context.Background()
		store := factory(t)
		d := definition()
		collection, err := store.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := collection.EnsureExists(ctx); err != nil {
			t.Fatal(err)
		}
		input := records()
		keys, err := collection.Insert(ctx, input)
		if err != nil || len(keys) != 3 {
			t.Fatalf("insert: %v, %v", keys, err)
		}
		input[0].Vectors["Text"][0] = 100
		input[0].Data["Title"] = "mutated"
		r, err := collection.Get(ctx, "a", vectordata.RetrievalOptions{})
		if err != nil || r.Data["Title"] != "quiet mountain hotel" || r.Vectors != nil {
			t.Fatalf("get: %+v, %v", r, err)
		}
		if r.Data["Details"].(map[string]any)["price"] != json.Number("9007199254740993") {
			t.Fatalf("precision lost: %#v", r.Data)
		}
		r, err = collection.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || r.Vectors["Text"][0] != 1 {
			t.Fatalf("vector alias: %v, %v", r, err)
		}
		r.Vectors["Text"][0] = 100
		r.Data["Tags"].([]string)[0] = "changed"
		r, err = collection.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || r.Vectors["Text"][0] != 1 || r.Data["Tags"].([]string)[0] != "news" {
			t.Fatal("read aliases storage")
		}
		if _, err := collection.Get(ctx, "missing", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrNotFound) {
			t.Fatalf("missing get: %v", err)
		}
		many, err := collection.GetMany(ctx, []any{"a", "missing", "c"}, vectordata.RetrievalOptions{})
		if err != nil || len(many) != 2 {
			t.Fatalf("get many: %v, %v", many, err)
		}
		tests := []struct {
			filter vectordata.Filter
			want   int64
		}{
			{vectordata.Contains(vectordata.Column("Tags"), "news"), 2}, {vectordata.Eq(vectordata.Column("Price"), float32(0.1)), 1}, {vectordata.Eq(vectordata.Column("Active"), true), 1}, {vectordata.Gt(vectordata.Column("Created"), time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)), 1}, {vectordata.Gte(vectordata.Column("Rank"), 2), 2}, {vectordata.Lte(vectordata.Column("Rank"), 2), 2},
			{vectordata.Gte(vectordata.DataPath("Details", "price"), 2), 2}, {vectordata.Lte(vectordata.DataPath("Details", "price"), 2), 1},
			{vectordata.Eq(vectordata.DataPath("Details", "price"), 2), 0}, {vectordata.Eq(vectordata.DataPath("Details", "price"), "2"), 1},
			{vectordata.Eq(vectordata.DataPath("Details", "nil"), nil), 1}, {vectordata.Not(vectordata.Eq(vectordata.DataPath("Details", "price"), 2)), 2},
			{vectordata.Contains(vectordata.DataPath("Details", "tags"), nil), 1}, {vectordata.Eq(vectordata.Column("Details"), nil), 1},
			{vectordata.Exists(vectordata.DataPath("Details", "price")), 2}, {vectordata.Ne(vectordata.Column("Rank"), 2), 2},
		}
		for _, test := range tests {
			count, err := collection.Count(ctx, test.filter)
			if err != nil || count != test.want {
				t.Fatalf("filter %#v: count %d, want %d, error %v", test.filter, count, test.want, err)
			}
		}
		ordered, err := collection.Query(ctx, nil, 1, vectordata.QueryOptions{Skip: 1, OrderBy: []vectordata.SortField{{Property: "Rank", Descending: true}}})
		if err != nil || len(ordered) != 1 || ordered[0].Key != "b" {
			t.Fatalf("paging: %v, %v", ordered, err)
		}
		results, err := collection.Search(ctx, []float32{1, 0, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: "Text", Skip: 1})
		if err != nil || len(results) != 1 || results[0].Record.Key != "b" || results[0].Record.Vectors != nil {
			t.Fatalf("search: %v, %v", results, err)
		}
		threshold := 0.1
		results, err = collection.Search(ctx, []float32{1, 0, 0}, 10, vectordata.ModelSearchOptions{VectorProperty: "Text", DistanceThreshold: &threshold})
		if err != nil || len(results) != 2 {
			t.Fatalf("distance threshold: %v, %v", results, err)
		}
		score := 0.99
		results, err = collection.Search(ctx, []float32{1, 0, 0}, 10, vectordata.ModelSearchOptions{VectorProperty: "Text", ScoreThreshold: &score, IncludeVectors: true})
		if err != nil || len(results) != 1 || results[0].ScoreKind != vectordata.ScoreSimilarity || len(results[0].Record.Vectors) != 2 {
			t.Fatalf("score threshold: %v, %v", results, err)
		}
		results, err = collection.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: "Image"})
		if err != nil || len(results) != 1 || results[0].Record.Key != "a" {
			t.Fatalf("second vector: %v, %v", results, err)
		}
		if _, err := collection.Search(ctx, []float32{1, 0, 0}, 1, vectordata.ModelSearchOptions{}); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
			t.Fatalf("ambiguous search: %v", err)
		}
		if _, err := collection.Count(ctx, vectordata.Eq(vectordata.Column("unknown"), 1)); !errors.Is(err, vectordata.ErrInvalidFilter) {
			t.Fatalf("invalid filter: %v", err)
		}
		for _, err := range []error{func() error { _, err := collection.Insert(ctx, records()); return err }(), func() error {
			bad := records()
			bad[1].Vectors["Text"] = []float32{0, 0, 0}
			_, err := collection.Upsert(ctx, bad)
			return err
		}()} {
			if err == nil {
				t.Fatal("invalid batch succeeded")
			}
		}
		if count, err := collection.Count(ctx, nil); err != nil || count != 3 {
			t.Fatalf("batch atomicity: %d, %v", count, err)
		}
		for _, err := range []error{func() error {
			for _, err := range collection.QueryIter(ctx, nil, 2, vectordata.QueryOptions{}) {
				return err
			}
			return nil
		}(), func() error {
			for _, err := range collection.SearchIter(ctx, []float32{1, 0, 0}, 2, vectordata.ModelSearchOptions{VectorProperty: "Text"}) {
				return err
			}
			return nil
		}()} {
			if err != nil {
				t.Fatal(err)
			}
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := collection.Count(canceled, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
		if _, err := collection.Upsert(ctx, []vectordata.ModelRecord{records()[0]}); err != nil {
			t.Fatal(err)
		}
		if deleted, err := collection.Delete(ctx, []any{"b", "missing"}); err != nil || deleted != 1 {
			t.Fatalf("delete: %d, %v", deleted, err)
		}
		names, err := store.ListCollectionNames(ctx)
		if err != nil || len(names) != 1 || names[0] != "records" {
			t.Fatalf("listing: %v, %v", names, err)
		}
		incompatible := d.Clone()
		incompatible.Vectors[0].Dimensions = 4
		if _, err := store.EnsureModelCollection(ctx, "records", incompatible, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("schema mismatch: %v", err)
		}
		if err := collection.EnsureDeleted(ctx); err != nil {
			t.Fatal(err)
		}
		if err := collection.EnsureDeleted(ctx); err != nil {
			t.Fatal(err)
		}
		if exists, err := collection.Exists(ctx); err != nil || exists {
			t.Fatalf("exists after delete: %v, %v", exists, err)
		}
	})
	t.Run("native_and_generated_keys", func(t *testing.T) {
		ctx := context.Background()
		store := factory(t)
		for _, key := range []struct {
			kind  vectordata.KeyType
			value any
		}{{vectordata.KeyString, "id"}, {vectordata.KeyInt64, int64(42)}, {vectordata.KeyUUID, "8f516db6-38ca-4e8f-ad79-d71b015f5419"}} {
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: key.kind}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}}}
			c, err := store.EnsureModelCollection(ctx, string(key.kind), d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Upsert(ctx, []vectordata.ModelRecord{{Key: key.value, Data: map[string]any{"Title": "value"}}}); err != nil {
				t.Fatal(err)
			}
			if r, err := c.Get(ctx, key.value, vectordata.RetrievalOptions{}); err != nil || r.Key != key.value {
				t.Fatalf("key %v: %v, %v", key.value, r, err)
			}
			if key.kind == vectordata.KeyUUID {
				if count, err := c.Count(ctx, vectordata.Eq(vectordata.Column("ID"), strings.ToUpper(key.value.(string)))); err != nil || count != 1 {
					t.Fatalf("UUID canonicalization: %d, %v", count, err)
				}
			}
			if names, err := store.ListCollectionNames(ctx); err != nil || len(names) == 0 {
				t.Fatalf("non-vector model listing: %v, %v", names, err)
			}
		}
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64, AutoGenerated: true}}
		c, err := store.EnsureModelCollection(ctx, "generated", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		keys, err := c.Insert(ctx, []vectordata.ModelRecord{{}, {}})
		if err != nil || len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("generated: %v, %v", keys, err)
		}
	})
	t.Run("json_null", func(t *testing.T) {
		ctx := context.Background()
		store := factory(t)
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString}, Data: []vectordata.DataProperty{{Name: "JSON", Type: vectordata.TypeJSON, Optional: true}}}
		c, err := store.EnsureModelCollection(ctx, "jsonnull", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "sql", Data: map[string]any{"JSON": nil}}, {Key: "json", Data: map[string]any{"JSON": vectordata.JSONNull{}}}}); err != nil {
			t.Fatal(err)
		}
		for _, f := range []vectordata.Filter{vectordata.Eq(vectordata.Column("JSON"), nil), vectordata.Eq(vectordata.DataPath("JSON"), nil), vectordata.Eq(vectordata.Column("JSON"), vectordata.JSONNull{})} {
			if count, err := c.Count(ctx, f); err != nil || count != 1 {
				t.Fatalf("JSON null %#v: %d, %v", f, count, err)
			}
		}
	})
}

// runGeneratedKeyCollision checks that generated-key requests cannot overwrite a
// manually assigned key when a database sequence has not caught up with it.
func runGeneratedKeyCollision(t *testing.T, factory Factory) {
	ctx := context.Background()
	store := factory(t)
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64, AutoGenerated: true}, Data: []vectordata.DataProperty{{Name: "Name", Type: vectordata.TypeString}}}
	c, err := store.EnsureModelCollection(ctx, "key_collision", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: int64(1), Data: map[string]any{"Name": "original"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Name": "replacement"}}}); err == nil {
		t.Fatal("generated-key request overwrote an existing record")
	}
	record, err := c.Get(ctx, int64(1), vectordata.RetrievalOptions{})
	if err != nil || record.Data["Name"] != "original" {
		t.Fatalf("existing record changed: %v, %v", record, err)
	}
}
