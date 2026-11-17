package redis

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestNativeConfigurationAndBoundFilters(t *testing.T) {
	if s, err := New(StoreOptions{NativeIndexes: true, DB: 1}); err == nil {
		s.Close()
		t.Fatal("native indexes accepted a nonzero Redis database")
	}
	if s, err := New(StoreOptions{NativeIndexes: true, StorageMode: StorageSnapshot}); err == nil {
		s.Close()
		t.Fatal("native indexes accepted snapshot creation mode")
	}
	s, err := New(StoreOptions{NativeIndexes: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.storageMode != StorageHash {
		t.Fatal("native mode did not select hash storage")
	}
	caps := s.Capabilities()
	if len(caps.VectorIndexCapabilities) != 8 || len(caps.IndexedDataTypes) != 4 || caps.HybridSearch || caps.FullTextIndexing {
		t.Fatalf("capabilities: %+v", caps)
	}
	caps.VectorIndexCapabilities[0].MaxDimensions = 1
	if s.Capabilities().VectorIndexCapabilities[0].MaxDimensions != nativeMaxDimensions {
		t.Fatal("capability alias")
	}
	name := `text."(*)雪`
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: name, Type: vectordata.TypeString, IsIndexed: true}, {Name: "Float", Type: vectordata.TypeFloat64, IsIndexed: true}, {Name: "JSON", Type: vectordata.TypeJSON}}}
	base, err := s.GetModelCollection("bind", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	value := `x} | *=>[KNN 10 @v0 $q]`
	plan, ok, err := c.compileNativeFilter(vectordata.Eq(vectordata.Column(name), value))
	if err != nil || !ok || strings.Contains(plan.query, value) || strings.Contains(plan.query, name) {
		t.Fatalf("unbound filter: %+v %v", plan, err)
	}
	for _, f := range []vectordata.Filter{vectordata.Eq(vectordata.Column(name), strings.Repeat("x", nativeTagLimit)), vectordata.Gt(vectordata.Column("Float"), 1.0), vectordata.Eq(vectordata.DataPath("JSON", "n"), 1)} {
		if _, ok, err := c.compileNativeFilter(f); err != nil || ok {
			t.Fatalf("unsupported filter did not fall back: %T %v", f, err)
		}
	}
	deep := vectordata.Filter(vectordata.Eq(vectordata.Column(name), "a"))
	for range 20 {
		deep = vectordata.Not(deep)
	}
	if _, ok, err := c.compileNativeFilter(deep); err != nil || ok {
		t.Fatal("deep filter did not fall back")
	}
	for _, tuning := range []any{struct{}{}, SearchTuning{EFRuntime: -1}, SearchTuning{EFRuntime: 10001}, SearchTuning{Exact: true, EFRuntime: 1}} {
		if _, err := c.searchTuning(vectordata.ModelSearchOptions{ProviderOptions: tuning}); err == nil {
			t.Fatalf("accepted invalid tuning %+v", tuning)
		}
	}
	if tag1, ok := nativeTag(math.Copysign(0, -1)); !ok {
		t.Fatal("negative zero tag")
	} else if tag2, _ := nativeTag(0.0); tag1 != tag2 {
		t.Fatal("signed zeros differ")
	}
	if nativeVectorSafe([]float32{1e20}) || nativeVectorSafe([]float32{1e-20}) {
		t.Fatal("unsafe vector accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Count(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
