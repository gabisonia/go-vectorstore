package vectordata_test

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestProviderAnnotationsOwnershipAndTypedAccess(t *testing.T) {
	values := map[string]any{"vendor.options": map[string]any{"values": []any{"first", json.Number("9007199254740993")}}}
	a, err := vectordata.NewProviderAnnotations(values)
	if err != nil {
		t.Fatal(err)
	}
	before := a
	values["vendor.options"].(map[string]any)["values"].([]any)[0] = "changed"
	got, found, err := vectordata.GetProviderAnnotation[map[string]json.RawMessage](a, "vendor.options")
	if err != nil || !found || string(got["values"]) != `["first",9007199254740993]` {
		t.Fatalf("snapshot: %v %v %v", got, found, err)
	}
	got["values"][0] = '!'
	if a != before {
		t.Fatal("getter aliased schema")
	}
	b, err := vectordata.WithProviderAnnotation(a, "vendor.count", 100)
	if err != nil {
		t.Fatal(err)
	}
	if a != before || a == b {
		t.Fatal("setter mutated original")
	}
	count, found, err := vectordata.GetProviderAnnotation[int](b, "vendor.count")
	if err != nil || !found || count != 100 {
		t.Fatalf("typed int: %d %v %v", count, found, err)
	}
	if _, found, err := vectordata.GetProviderAnnotation[bool](b, "vendor.count"); !errors.Is(err, vectordata.ErrSchemaMismatch) || !found {
		t.Fatalf("wrong type: %v %v", found, err)
	}
	if _, found, err := vectordata.GetProviderAnnotation[bool](b, "absent"); err != nil || found {
		t.Fatalf("missing: %v %v", found, err)
	}
	b, err = vectordata.WithoutProviderAnnotation(b, "vendor.count")
	if err != nil || b != a {
		t.Fatalf("remove: %s %v", b, err)
	}
	data := vectordata.DataProperty{Name: "Body", Type: vectordata.TypeString, ProviderAnnotations: a}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", ProviderAnnotations: a}, Data: []vectordata.DataProperty{data}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, ProviderAnnotations: a}}}
	normalized, err := d.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	clone := normalized.Clone()
	clone.Data[0].ProviderAnnotations = b
	if !reflect.DeepEqual(normalized, clone) {
		t.Fatal("immutable clone changed definition")
	}
	// All property types remain comparable, including annotated definitions.
	if normalized.Key != clone.Key || normalized.Data[0] != clone.Data[0] {
		t.Fatal("property comparison lost value semantics")
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var restored vectordata.CollectionDefinition
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized, restored) {
		t.Fatal("definition round trip changed annotations")
	}
}

func TestProviderAnnotationsCanonicalJSONAndNumbers(t *testing.T) {
	for _, group := range [][]string{
		{`{"vendor.number":100}`, `{ "vendor.number": 1.000e+02 }`},
		{`{"vendor.number":0.1000000000000000000000000001}`, `{"vendor.number":1000000000000000000000000001e-28}`},
		{`{"vendor.number":0}`, `{"vendor.number":-0e999999999999999999999}`},
		{`{"vendor.number":1e100000000000000000000}`, `{"vendor.number":10e99999999999999999999}`},
		{`{"vendor.object":{"a":1,"b":[1e2,true,null]},"vendor.z":9007199254740993}`, `{"vendor.z":9007199254740993,"vendor.object":{"b":[100.0,true,null],"a":1.0}}`},
	} {
		var previous vectordata.ProviderAnnotations
		for i, raw := range group {
			a, err := vectordata.ParseProviderAnnotations([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if i > 0 && a != previous {
				t.Fatalf("noncanonical: %s != %s", a, previous)
			}
			previous = a
		}
	}
	a, err := vectordata.ParseProviderAnnotations([]byte(`{"vendor.integer":18446744073709551615}`))
	if err != nil {
		t.Fatal(err)
	}
	n, found, err := vectordata.GetProviderAnnotation[uint64](a, "vendor.integer")
	if err != nil || !found || n != math.MaxUint64 {
		t.Fatalf("uint64 precision: %d %v %v", n, found, err)
	}
	for _, raw := range []string{`{}`, `null`, ` { } `} {
		a, err := vectordata.ParseProviderAnnotations([]byte(raw))
		if err != nil || a != "" {
			t.Fatalf("empty: %q %v", a, err)
		}
	}
}

func TestProviderAnnotationsInvalidInput(t *testing.T) {
	for _, raw := range []string{`[]`, `true`, `"object"`, `{"":1}`, `{" bad":1}`, `{"bad\u0000key":1}`, `{"a":}`, `{} {}`, "{\"a\":\"\xff\"}"} {
		if _, err := vectordata.ParseProviderAnnotations([]byte(raw)); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("accepted %q: %v", raw, err)
		}
	}
	if _, err := vectordata.NewProviderAnnotations(map[string]any{"vendor.bad": math.NaN()}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatal(err)
	}
	if _, err := vectordata.NewProviderAnnotations(map[string]any{string([]byte{255}): true}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatal("invalid UTF-8 annotation name was replaced instead of rejected")
	}
	if _, err := vectordata.WithProviderAnnotation(vectordata.ProviderAnnotations(""), "", true); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatal(err)
	}
	invalid := vectordata.ProviderAnnotations(`[]`)
	for _, d := range []vectordata.CollectionDefinition{
		{Key: vectordata.KeyProperty{Name: "ID", ProviderAnnotations: invalid}},
		{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, ProviderAnnotations: invalid}}},
		{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, ProviderAnnotations: invalid}}},
	} {
		if _, err := d.Normalize(); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatal(err)
		}
	}
}

func TestProviderAnnotationsPreserveEmptyRegistrations(t *testing.T) {
	p := vectordata.KeyProperty{Name: "ID", StorageName: "id", Type: vectordata.KeyString}
	raw, err := json.Marshal(p)
	if err != nil || string(raw) != `{"Name":"ID","StorageName":"id","Type":"string","AutoGenerated":false}` {
		t.Fatalf("key registration changed: %s %v", raw, err)
	}
	d := vectordata.DataProperty{Name: "Body", StorageName: "body", Type: vectordata.TypeString}
	raw, err = json.Marshal(d)
	if err != nil || string(raw) != `{"Name":"Body","StorageName":"body","Type":"string","Optional":false,"IsIndexed":false,"IsFullTextIndexed":false}` {
		t.Fatalf("data registration changed: %s %v", raw, err)
	}
	v := vectordata.VectorProperty{Name: "Vector", StorageName: "vector", Dimensions: 2, Metric: vectordata.DistanceCosine}
	raw, err = json.Marshal(v)
	if err != nil || string(raw) != `{"Name":"Vector","StorageName":"vector","Dimensions":2,"Metric":"cosine","Index":null,"SourceProperty":""}` {
		t.Fatalf("vector registration changed: %s %v", raw, err)
	}

}

func FuzzProviderAnnotations(f *testing.F) {
	for _, raw := range []string{`{}`, `{"vendor.n":1e2}`, `{"vendor.n":9007199254740993}`, `{"vendor.n":-0e9999999}`, `{"vendor.n":1e9999999999}`, `{"vendor.a":[0.1,{"n":true}]}`} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 8192 {
			return
		}
		a, err := vectordata.ParseProviderAnnotations([]byte(raw))
		if err != nil {
			return
		}
		encoded, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := vectordata.ParseProviderAnnotations(encoded)
		if err != nil || a != b {
			t.Fatalf("unstable canonical annotations: %s -> %s %v", a, b, err)
		}
	})
}
