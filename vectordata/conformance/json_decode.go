package conformance

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

var errJSONDecode = errors.New("application JSON decoder rejected value")

type decodedJSONToken string

func (v decodedJSONToken) MarshalJSON() ([]byte, error) { return json.Marshal("wire:" + string(v)) }
func (v *decodedJSONToken) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	if !strings.HasPrefix(text, "wire:") {
		return errJSONDecode
	}
	*v = decodedJSONToken(strings.TrimPrefix(text, "wire:"))
	return nil
}
func (v *decodedJSONToken) UnmarshalText(raw []byte) error {
	return errors.New("JSON field used native text decoding")
}

type decodedJSONObject map[string]any

func (v *decodedJSONObject) UnmarshalJSON(raw []byte) error {
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return err
	}
	if object["ok"] != true {
		return errJSONDecode
	}
	*v = object
	return nil
}

type jsonDecodeModel struct {
	ID      string            `vectordata:"key"`
	Token   decodedJSONToken  `vectordata:"data,type=json"`
	Pointer *decodedJSONToken `vectordata:"data,type=json"`
	Object  decodedJSONObject `vectordata:"data,type=json"`
	Vector  []float32         `vectordata:"vector,dimensions=2,metric=l2"`
}

func runJSONDecode(t *testing.T, factory Factory) {
	ctx := t.Context()
	store := factory(t)
	c, err := vectordata.EnsureTypedModelCollection[string, jsonDecodeModel](ctx, store, "json_decode", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pointer := decodedJSONToken("pointer")
	valid := func(id string) jsonDecodeModel {
		return jsonDecodeModel{ID: id, Token: "hello", Object: decodedJSONObject{"ok": true, "n": json.Number("9007199254740993")}, Vector: []float32{1, 0}}
	}
	first := valid("a")
	first.Pointer = &pointer
	if _, err := c.Insert(ctx, []jsonDecodeModel{first, valid("b"), valid("zz")}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || got.Token != "hello" || got.Pointer == nil || *got.Pointer != "pointer" || got.Object["n"] != json.Number("9007199254740993") {
		t.Fatalf("JSON decoder round trip: %+v, %v", got, err)
	}
	got.Object["n"] = "changed"
	got, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || got.Object["n"] != json.Number("9007199254740993") {
		t.Fatalf("JSON decode aliases storage: %+v, %v", got, err)
	}
	if _, err := c.UpsertOne(ctx, got); err != nil {
		t.Fatal(err)
	}
	base, err := store.GetModelCollection(c.Name(), c.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := base.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || canonical.Data["Token"] != "wire:hello" || canonical.Data["Pointer"] != "wire:pointer" {
		t.Fatalf("typed reread/rewrite changed canonical JSON: %#v, %v", canonical.Data, err)
	}
	got, err = c.Get(ctx, "b", vectordata.RetrievalOptions{})
	if err != nil || got.Pointer != nil {
		t.Fatalf("SQL NULL pointer allocated a decoder: %+v, %v", got, err)
	}
	// Dynamic writers can supply valid JSON that violates an application's
	// stronger typed decoding rules. Those errors must not be bypassed.
	bad := vectordata.ModelRecord{Key: "z", Data: map[string]any{"Token": "invalid", "Pointer": nil, "Object": map[string]any{"ok": true}}, Vectors: map[string][]float32{"Vector": {1, 0}}}
	if _, err := base.Upsert(ctx, []vectordata.ModelRecord{bad}); err != nil {
		t.Fatal(err)
	}
	got, err = c.Get(ctx, "z", vectordata.RetrievalOptions{})
	if !errors.Is(err, errJSONDecode) || got.ID != "" {
		t.Fatalf("scalar JSON validation bypassed/partial record: %+v, %v", got, err)
	}
	bad.Data["Token"] = "wire:valid"
	bad.Data["Object"] = map[string]any{"ok": false}
	if _, err := base.Upsert(ctx, []vectordata.ModelRecord{bad}); err != nil {
		t.Fatal(err)
	}
	got, found, err := c.TryGet(ctx, "z", vectordata.RetrievalOptions{})
	if !errors.Is(err, errJSONDecode) || found || got.ID != "" {
		t.Fatalf("map JSON validation bypassed/partial record: %+v, %v, %v", got, found, err)
	}
	rows, err := c.Query(ctx, nil, 4, vectordata.QueryOptions{})
	if !errors.Is(err, errJSONDecode) || rows != nil {
		t.Fatalf("query returned partial decode: %+v, %v", rows, err)
	}
	rows, err = c.GetMany(ctx, []string{"a", "z"}, vectordata.RetrievalOptions{})
	if !errors.Is(err, errJSONDecode) || rows != nil {
		t.Fatalf("batch lookup returned partial decode: %+v, %v", rows, err)
	}
	hits, err := c.Search(ctx, []float32{1, 0}, 4, vectordata.ModelSearchOptions{})
	if !errors.Is(err, errJSONDecode) || hits != nil {
		t.Fatalf("search returned partial decode: %+v, %v", hits, err)
	}
	values, failures := 0, 0
	for row, err := range c.QueryIter(ctx, nil, 4, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "ID"}}}) {
		if err != nil {
			if !errors.Is(err, errJSONDecode) || row.ID != "" {
				t.Fatalf("iterator lost JSON decode error: %+v, %v", row, err)
			}
			failures++
			continue
		}
		values++
	}
	if values != 2 || failures != 1 {
		t.Fatalf("iterator did not stop at failed decoder: values=%d errors=%d", values, failures)
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 4 {
		t.Fatalf("read errors changed storage/leaked resources: %d, %v", count, err)
	}
	reopened, err := vectordata.GetTypedModelCollection[string, jsonDecodeModel](store, c.Name(), nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = reopened.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || got.Token != "hello" {
		t.Fatalf("JSON decoder reopen: %+v, %v", got, err)
	}
}
