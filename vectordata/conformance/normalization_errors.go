package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type marshalingFailure struct{}

func (*marshalingFailure) Error() string { return "application marshaling failed" }

type marshalingUUID struct {
	text string
	err  error
}

func (u marshalingUUID) MarshalText() ([]byte, error)     { return []byte(u.text), u.err }
func (u *marshalingUUID) UnmarshalText(text []byte) error { u.text = string(text); return nil }

type marshalingJSON struct{ err error }

func (j marshalingJSON) MarshalJSON() ([]byte, error) { return nil, j.err }

type marshalingModel struct {
	ID     int64            `vectordata:"key,generated"`
	Body   string           `vectordata:"data"`
	Owner  marshalingUUID   `vectordata:"data,type=uuid"`
	Owners []marshalingUUID `vectordata:"data,type=uuid_array"`
	Extra  any              `vectordata:"data,type=json"`
	Vector []float32        `vectordata:"vector,dimensions=2,metric=l2,source=Body"`
}

func runNormalizationErrors(t *testing.T, factory Factory) {
	ctx := t.Context()
	embeds := 0
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		embeds++
		vectors := make([][]float32, len(texts))
		for i := range vectors {
			vectors[i] = []float32{1, 0}
		}
		return vectors, ctx.Err()
	})
	c, err := vectordata.EnsureTypedModelCollection[int64, marshalingModel](ctx, factory(t), "marshal_errors", nil, vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	valid := func(id int64, body string) marshalingModel {
		return marshalingModel{ID: id, Body: body, Owner: marshalingUUID{text: nativeUUIDA}, Owners: []marshalingUUID{}, Extra: map[string]any{"body": body}}
	}
	if _, err := c.InsertOne(ctx, valid(0, "seed")); err != nil {
		t.Fatal(err)
	}
	cause := &marshalingFailure{}
	check := func(err, classification error) {
		t.Helper()
		var typed *marshalingFailure
		if !errors.Is(err, classification) || !errors.Is(err, cause) || !errors.As(err, &typed) || typed != cause {
			t.Fatalf("lost classification/cause: %v", err)
		}
	}
	for _, property := range []string{"Owner", "Owners", "Extra"} {
		bad := valid(0, "invalid")
		switch property {
		case "Owner":
			bad.Owner.err = cause
		case "Owners":
			bad.Owners = []marshalingUUID{{err: cause}}
		case "Extra":
			bad.Extra = marshalingJSON{cause}
		}
		for _, upsert := range []bool{false, true} {
			embeds = 0
			first := valid(0, "new")
			write := c.Insert
			if upsert {
				write = c.Upsert
				first = valid(1, "changed")
			}
			keys, err := write(ctx, []marshalingModel{first, bad})
			check(err, vectordata.ErrInvalidRecord)
			if keys != nil || embeds != 0 {
				t.Fatalf("failed marshaling produced keys/embedding: %v, %d", keys, embeds)
			}
			if count, err := c.Count(ctx, nil); err != nil || count != 1 {
				t.Fatalf("partial failed batch: %d, %v", count, err)
			}
			seed, err := c.Get(ctx, 1, vectordata.RetrievalOptions{})
			if err != nil || seed.Body != "seed" {
				t.Fatalf("failed batch changed seed: %+v, %v", seed, err)
			}
		}
	}
	for _, filter := range []vectordata.Filter{
		vectordata.Eq(vectordata.Column("Owner"), marshalingUUID{err: cause}),
		vectordata.Contains(vectordata.Column("Owners"), marshalingUUID{err: cause}),
		vectordata.Eq(vectordata.Column("Extra"), marshalingJSON{cause}),
		vectordata.Eq(vectordata.DataPath("Extra", "body"), marshalingJSON{cause}),
	} {
		embeds = 0
		rows, err := c.Query(ctx, filter, 1, vectordata.QueryOptions{})
		check(err, vectordata.ErrInvalidFilter)
		if rows != nil {
			t.Fatal("invalid filter returned rows")
		}
		_, err = c.Count(ctx, filter)
		check(err, vectordata.ErrInvalidFilter)
		hits, err := c.SearchText(ctx, "query", 1, vectordata.ModelSearchOptions{Filter: filter})
		check(err, vectordata.ErrInvalidFilter)
		if hits != nil || embeds != 0 {
			t.Fatalf("invalid filter produced hits/embedding: %v, %d", hits, embeds)
		}
		failures := 0
		for row, err := range c.QueryIter(ctx, filter, 1, vectordata.QueryOptions{}) {
			check(err, vectordata.ErrInvalidFilter)
			if row.ID != 0 {
				t.Fatal("invalid filter yielded record")
			}
			failures++
		}
		if failures != 1 {
			t.Fatalf("invalid-filter iterator errors: %d", failures)
		}
	}
	key, err := c.InsertOne(ctx, valid(0, "after"))
	if err != nil || key != 2 {
		t.Fatalf("failed batch consumed generated keys: %v, %v", key, err)
	}
	// JSON's standard wrapper must survive provider diagnostics as well.
	_, err = c.Upsert(ctx, []marshalingModel{{Extra: marshalingJSON{cause}, Owner: marshalingUUID{text: nativeUUIDA}, Owners: []marshalingUUID{}, Body: "bad"}})
	var marshaler *json.MarshalerError
	if !errors.As(err, &marshaler) || !errors.Is(marshaler, cause) {
		t.Fatalf("lost JSON marshaler wrapper: %v", err)
	}
}
