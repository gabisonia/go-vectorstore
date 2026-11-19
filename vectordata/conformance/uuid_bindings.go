package conformance

import (
	"errors"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type decodeOnlyKey struct{ text string }

func (u *decodeOnlyKey) UnmarshalText(text []byte) error { u.text = string(text); return nil }

type stringerKey struct{ text string }

func (u stringerKey) String() string                   { return u.text }
func (u *stringerKey) UnmarshalText(text []byte) error { u.text = string(text); return nil }

func runUUIDBindings(t *testing.T, factory Factory) {
	ctx := t.Context()
	store := factory(t)
	type invalidModel struct {
		ID decodeOnlyKey `vectordata:"key,type=uuid,generated"`
	}
	type stringModel struct {
		ID string `vectordata:"key,type=uuid,generated"`
	}
	for _, field := range []bool{false, true} {
		name := "invalid_uuid_key"
		if field {
			name = "invalid_uuid_field"
		}
		var err error
		if field {
			_, err = vectordata.EnsureTypedModelCollection[string, invalidModel](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		} else {
			_, err = vectordata.EnsureTypedModelCollection[decodeOnlyKey, stringModel](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		}
		if !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("incomplete UUID binding accepted: %v", err)
		}
		if exists, err := store.CollectionExists(ctx, name); err != nil || exists {
			t.Fatalf("invalid binding created storage: %v, %v", exists, err)
		}
	}
	type validModel struct {
		ID stringerKey `vectordata:"key,type=uuid,generated"`
	}
	c, err := vectordata.EnsureTypedModelCollection[stringerKey, validModel](ctx, store, "stringer_uuid", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []validModel{{ID: stringerKey{text: strings.ToUpper(nativeUUIDA)}}, {}}
	keys, err := c.Insert(ctx, inputs)
	if err != nil || len(keys) != 2 || keys[0].text != nativeUUIDA || keys[1].text == "" || keys[0] == keys[1] {
		t.Fatalf("explicit/generated Stringer UUIDs: %v, %v", keys, err)
	}
	if inputs[0].ID.text != strings.ToUpper(nativeUUIDA) || inputs[1].ID.text != "" {
		t.Fatal("write mutated caller UUIDs")
	}
	for _, key := range keys {
		got, err := c.Get(ctx, key, vectordata.RetrievalOptions{})
		if err != nil || got.ID != key {
			t.Fatalf("Stringer UUID round trip: %+v, %v", got, err)
		}
		if count, err := c.Count(ctx, vectordata.Eq(vectordata.Column("ID"), key)); err != nil || count != 1 {
			t.Fatalf("Stringer UUID filter: %d, %v", count, err)
		}
		if deleted, err := c.DeleteOne(ctx, key); err != nil || !deleted {
			t.Fatalf("Stringer UUID delete: %v, %v", deleted, err)
		}
	}
}
