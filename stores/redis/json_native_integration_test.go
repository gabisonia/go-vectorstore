//go:build integration

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

func TestIntegrationJSONNativePrecisionAndPaths(t *testing.T) {
	requireRedisJSON(t)
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageJSON, NativeIndexes: true})
	name := `vector."[*]雪`
	d := vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUint64},
		Data:    []vectordata.DataProperty{{Name: "JSON", Type: vectordata.TypeJSON}, {Name: "Decimal", Type: vectordata.TypeDecimal}, {Name: "Text", Type: vectordata.TypeString, IsIndexed: true, IsFullTextIndexed: true}},
		Vectors: []vectordata.VectorProperty{{Name: name, Dimensions: 2, Precision: vectordata.VectorFloat16, Metric: vectordata.DistanceL2, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodFlat}}},
	}
	base, err := s.EnsureModelCollection(ctx, "precision", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	input := vectordata.ModelRecord{Key: uint64(math.MaxUint64), Data: map[string]any{"JSON": map[string]any{"integer": json.Number("18446744073709551615"), "huge": json.Number("1e9999")}, "Decimal": "0.1000000000000000000000001", "Text": "nul\x00雪\"<>&"}, Vectors: map[string][]float32{name: {0.3333, 1}}}
	if _, err = c.Insert(ctx, []vectordata.ModelRecord{input}); err != nil {
		t.Fatal(err)
	}
	reference, err := c.engine(ctx)
	if err != nil {
		t.Fatal(err)
	}
	native, full := 0, 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, nativeJSONExecute) {
			native++
		}
		if scriptIs(cmd, readJSON) {
			full++
		}
		return next(ctx, cmd)
	}})
	options := vectordata.ModelSearchOptions{IncludeVectors: true}
	hits, err := c.Search(ctx, []float32{0.3333, 1}, 1, options)
	want, refErr := reference.Search(ctx, []float32{0.3333, 1}, 1, options)
	if err != nil || refErr != nil || !reflect.DeepEqual(hits, want) || len(hits) != 1 || native != 1 || full != 0 {
		t.Fatalf("precision native search: %+v/%v want %+v/%v native=%d full=%d", hits, err, want, refErr, native, full)
	}
	r := hits[0].Record
	if r.Key != input.Key || !reflect.DeepEqual(r.Data["JSON"], input.Data["JSON"]) || r.Data["Decimal"] != vectordata.Decimal("0.1000000000000000000000001") || r.Vectors[name][0] == input.Vectors[name][0] {
		t.Fatalf("canonical precision/half rounding: %+v", r)
	}
	m, _ := currentJSONManifest(t, c)
	fields, err := c.nativeFields(m)
	if err != nil || len(fields) != 2 || fields[1].Name != "$.__vs_vectors.v0" {
		t.Fatalf("unsafe JSON paths or scalar/text fields: %+v %v", fields, err)
	}
	raw, err := s.client.Do(ctx, "JSON.GET", c.jsonKeys(m)[2]).Text()
	if err != nil {
		t.Fatal(err)
	}
	var doc jsonDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || !reflect.DeepEqual(doc.Vectors["v0"], r.Vectors[name]) {
		t.Fatalf("rounded JSON projection: %+v %v", doc, err)
	}
	options.Filter = vectordata.Eq(vectordata.Column("Text"), input.Data["Text"])
	native, full = 0, 0
	hits, err = c.Search(ctx, []float32{0.3333, 1}, 1, options)
	if err != nil || len(hits) != 1 || native != 0 || full != 1 {
		t.Fatalf("JSON filter did not fall back: %+v %v native=%d full=%d", hits, err, native, full)
	}
	assertPersistentJSON(t, c)
	// The JSON projection must fit alongside the canonical payload, not just the
	// manifest or the original snapshot. Reject before touching Redis.
	state, _, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projected, manifest, rows, err := c.projectJSON(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	keys := c.jsonKeys(projected)
	size := len(manifest) + len(keys[1]) + len(projected.Revision)
	for i, row := range rows {
		size += len(keys[i+2]) + len(row.document)
	}
	s.maxBytes = size - 1
	if _, _, _, err = c.projectJSON(ctx, state); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("native projection budget ignored: %v", err)
	}
	s.maxBytes = 16 << 20
}

func TestIntegrationJSONNativePublicationConflict(t *testing.T) {
	requireRedisJSON(t)
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageJSON, NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace, StorageMode: StorageJSON, NativeIndexes: true})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writer, err := other.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishJSON) {
			calls++
			if calls == 1 {
				_, err := writer.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "competitor"}, Vectors: map[string][]float32{"Vector": {1, 0}}}})
				if err != nil {
					return err
				}
			}
		}
		return next(ctx, cmd)
	}})
	keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "retried"}, Vectors: map[string][]float32{"Vector": {0, 1}}}})
	if err != nil || calls != 2 || !reflect.DeepEqual(keys, []any{int64(2)}) {
		t.Fatalf("CAS retry/sequence: %v %v calls=%d", keys, err, calls)
	}
	m, _ := currentJSONManifest(t, c)
	if m.Next != 3 || len(m.Tokens) != 2 {
		t.Fatalf("conflict lost records/sequence: %+v", m)
	}
	indexes, err := s.client.Do(ctx, "FT._LIST").StringSlice()
	if err != nil {
		t.Fatal(err)
	}
	own := 0
	for _, index := range indexes {
		if strings.HasPrefix(index, s.jsonIndexPrefix(c.name)) {
			own++
			if index != m.Index {
				t.Fatalf("losing/retired index retained: %s", index)
			}
		}
	}
	if own != 1 {
		t.Fatalf("current index count: %d", own)
	}
	if len(jsonAuxiliaryKeys(t, c)) != 3 {
		t.Fatal("losing/retired JSON generation retained")
	}
	if hits, err := c.Search(ctx, []float32{0, 1}, 1, vectordata.ModelSearchOptions{}); err != nil || len(hits) != 1 || hits[0].Record.Key != int64(2) {
		t.Fatalf("winning generation search: %+v %v", hits, err)
	}
}

func TestIntegrationJSONNativePathDriftAndUnknownVersion(t *testing.T) {
	requireRedisJSON(t)
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageJSON, NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	if _, err := c.Insert(ctx, nativeLifecycleRows("kept")); err != nil {
		t.Fatal(err)
	}
	m, _ := currentJSONManifest(t, c)
	if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.client.Do(ctx, "FT.CREATE", m.Index, "ON", "JSON", "PREFIX", 1, c.jsonGenerationPrefix(m.Revision), "SCHEMA", "$.__vs_revision", "AS", "revision", "TAG", "CASESENSITIVE", "$.__vs_vectors.wrong", "AS", "v0", "VECTOR", "HNSW", 10, "TYPE", "FLOAT32", "DIM", 2, "DISTANCE_METRIC", "L2", "M", 16, "EF_CONSTRUCTION", 200).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("JSON path drift accepted: %v", err)
	}
	if err := c.RebuildNativeIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ = currentJSONManifest(t, c)
	m.IndexVersion = 999
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.client.Set(ctx, s.key(c.name), raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	native, full := 0, 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, nativeJSONExecute) {
			native++
		}
		if scriptIs(cmd, readJSON) {
			full++
		}
		return next(ctx, cmd)
	}})
	if hits, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{}); err != nil || len(hits) != 1 || native != 0 || full != 1 {
		t.Fatalf("unknown JSON index version bypassed fallback: %+v %v native=%d full=%d", hits, err, native, full)
	}
}
