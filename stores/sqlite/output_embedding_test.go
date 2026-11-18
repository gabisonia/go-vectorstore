package sqlite_test

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestOutputAdapterReopensUnchangedRegistration(t *testing.T) {
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceL2, SourceProperty: "Body"}}}
	path := filepath.Join(t.TempDir(), "outputs.db")
	first, err := sqlite.Open(t.Context(), path, sqlite.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := first.EnsureModelCollection(t.Context(), "records", d, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		return [][]float32{{1.5, math.Float32frombits(0x80000000)}}, nil
	})})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	before, err := json.Marshal(legacy.Definition())
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Insert(t.Context(), []vectordata.ModelRecord{{Key: "kept", Data: map[string]any{"Body": "original"}}}); err != nil {
		first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	var calls int
	adapter, err := vectordata.AdaptOutputEmbedder(vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits](func(context.Context, []string) ([][]vectordata.Float16Bits, error) {
		calls++
		return [][]vectordata.Float16Bits{{0x3e00, 0x8000}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	d.Vectors[0].InputEmbedder = adapter
	reopened, err := sqlite.Open(t.Context(), path, sqlite.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c, err := reopened.EnsureModelCollection(t.Context(), "records", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal("runtime output adapter changed schema:", err)
	}
	after, err := json.Marshal(c.Definition())
	if err != nil || string(before) != string(after) {
		t.Fatalf("registration bytes changed: %s %s %v", before, after, err)
	}
	old, err := c.Get(t.Context(), "kept", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || old.Vectors["V"][0] != 1.5 || math.Float32bits(old.Vectors["V"][1]) != 0x80000000 || calls != 0 {
		t.Fatalf("old data changed or re-embedded: %+v %v %d", old, err, calls)
	}
	if _, err := c.Insert(t.Context(), []vectordata.ModelRecord{{Key: "next", Data: map[string]any{"Body": "new"}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := c.SearchText(t.Context(), "query", 1, vectordata.ModelSearchOptions{})
	if err != nil || len(hits) != 1 || hits[0].Distance != 0 || calls != 2 {
		t.Fatalf("reopened generation/query: %v %v %d", hits, err, calls)
	}
}
