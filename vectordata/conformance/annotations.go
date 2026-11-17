package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runProviderAnnotations(t *testing.T, factory Factory) {
	ctx := context.Background()
	store := factory(t)
	a, err := vectordata.NewProviderAnnotations(map[string]any{
		"vendor.settings": map[string]any{"integer": json.Number("9007199254740993"), "decimal": json.Number("0.1000000000000000000000000001"), "exponent": json.Number("1e20"), "values": []any{true, nil, "雪"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", ProviderAnnotations: a}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, ProviderAnnotations: a}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, ProviderAnnotations: a}}}
	c, err := store.EnsureModelCollection(ctx, "annotations", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{{Key: "one", Data: map[string]any{"Body": "canonical"}, Vectors: map[string][]float32{"Vector": {1, 0}}}}); err != nil {
		t.Fatal(err)
	}
	// Reopening validates persisted registration, including PostgreSQL JSONB's
	// rewritten numeric spellings; semantically identical hints must stay equal.
	reopened, err := store.EnsureModelCollection(ctx, "annotations", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Definition()
	if got.Key.ProviderAnnotations != a || got.Data[0].ProviderAnnotations != a || got.Vectors[0].ProviderAnnotations != a {
		t.Fatal("lost provider annotations")
	}
	got.Data[0].ProviderAnnotations, err = vectordata.WithProviderAnnotation(got.Data[0].ProviderAnnotations, "vendor.changed", true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Definition().Data[0].ProviderAnnotations != a {
		t.Fatal("returned definition changed stored handle")
	}
	row, err := reopened.Get(ctx, "one", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || row.Data["Body"] != "canonical" || len(row.Vectors["Vector"]) != 2 {
		t.Fatalf("annotations changed records: %+v %v", row, err)
	}
	if _, err := store.EnsureModelCollection(ctx, "annotations", got, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("annotation change silently altered registration: %v", err)
	}
}
