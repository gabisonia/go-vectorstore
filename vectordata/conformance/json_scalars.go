package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type objectJSONInteger int64

var errJSONScalar = errors.New("invalid JSON scalar")

func (v objectJSONInteger) MarshalJSON() ([]byte, error) {
	if v < 0 {
		return nil, errJSONScalar
	}
	return json.Marshal(struct {
		Value int64 `json:"value"`
	}{int64(v)})
}
func (v *objectJSONInteger) UnmarshalJSON(raw []byte) error {
	var object struct {
		Value int64 `json:"value"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	*v = objectJSONInteger(object.Value)
	return nil
}

type jsonScalarModel struct {
	ID      int64             `vectordata:"key,generated"`
	Body    string            `vectordata:"data"`
	Number  json.Number       `vectordata:"data,type=json"`
	Float   float32           `vectordata:"data,type=json"`
	Object  objectJSONInteger `vectordata:"data,type=json"`
	Pointer *json.Number      `vectordata:"data,type=json"`
	Vector  []float32         `vectordata:"vector,dimensions=2,metric=l2,source=Body"`
}

func runJSONScalars(t *testing.T, factory Factory) {
	ctx := t.Context()
	store := factory(t)
	embeds := 0
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		embeds++
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 0}
		}
		return out, ctx.Err()
	})
	c, err := vectordata.EnsureTypedModelCollection[int64, jsonScalarModel](ctx, store, "json_scalars", nil, vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	large := json.Number("9007199254740993")
	input := jsonScalarModel{Body: "seed", Number: large, Float: 0.1, Object: 7, Pointer: &large}
	key, err := c.InsertOne(ctx, input)
	if err != nil || key != 1 || input.ID != 0 {
		t.Fatalf("JSON scalar insert: %d, %v", key, err)
	}
	base, err := store.GetModelCollection(c.Name(), c.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := base.Get(ctx, key, vectordata.RetrievalOptions{})
	if err != nil || r.Data["Number"] != large || r.Data["Float"] != json.Number("0.1") || r.Data["Pointer"] != large {
		t.Fatalf("canonical JSON scalars: %#v, %v", r.Data, err)
	}
	object, ok := r.Data["Object"].(map[string]any)
	if !ok || object["value"] != json.Number("7") {
		t.Fatalf("custom scalar JSON encoding: %#v", r.Data["Object"])
	}
	got, err := c.Get(ctx, key, vectordata.RetrievalOptions{})
	if err != nil || got.Number != large || got.Float != 0.1 || got.Object != 7 || got.Pointer == nil || *got.Pointer != large {
		t.Fatalf("typed JSON round trip: %+v, %v", got, err)
	}
	for _, filter := range []vectordata.Filter{
		vectordata.Eq(vectordata.Column("Number"), large),
		vectordata.Eq(vectordata.Column("Float"), float32(0.1)),
		vectordata.Eq(vectordata.DataPath("Object", "value"), 7),
	} {
		rows, err := c.Query(ctx, filter, 2, vectordata.QueryOptions{})
		if err != nil || len(rows) != 1 || rows[0].ID != key {
			t.Fatalf("JSON scalar filter: %+v, %v", rows, err)
		}
		if count, err := c.Count(ctx, filter); err != nil || count != 1 {
			t.Fatalf("JSON scalar count: %d, %v", count, err)
		}
		hits, err := c.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{Filter: filter})
		if err != nil || len(hits) != 1 || hits[0].Item.ID != key {
			t.Fatalf("JSON scalar search: %+v, %v", hits, err)
		}
	}
	embeds = 0
	bad := input
	bad.Object = -1
	first := input
	first.ID = key
	first.Body = "changed"
	_, err = c.Upsert(ctx, []jsonScalarModel{first, bad})
	var marshaler *json.MarshalerError
	if !errors.Is(err, vectordata.ErrInvalidRecord) || !errors.Is(err, errJSONScalar) || !errors.As(err, &marshaler) || embeds != 0 {
		t.Fatalf("scalar marshaler failure: %v, embeddings=%d", err, embeds)
	}
	got, err = c.Get(ctx, key, vectordata.RetrievalOptions{})
	if err != nil || got.Body != "seed" {
		t.Fatalf("failed JSON batch overwrote stored record: %+v, %v", got, err)
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 1 {
		t.Fatalf("partial JSON write: %d, %v", count, err)
	}
	nullPointer := input
	nullPointer.Pointer = nil
	if key, err := c.InsertOne(ctx, nullPointer); err != nil || key != 2 {
		t.Fatalf("failed JSON batch consumed generated keys: %d, %v", key, err)
	}
	got, err = c.Get(ctx, 2, vectordata.RetrievalOptions{})
	if err != nil || got.Pointer != nil {
		t.Fatalf("nil JSON pointer lost SQL NULL semantics: %+v, %v", got, err)
	}
}
