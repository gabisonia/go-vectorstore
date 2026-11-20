//go:build referenceparity

package memoryparity

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type article struct {
	Id       string    `vectordata:"key"`
	Category string    `vectordata:"data,index"`
	Vector   []float32 `vectordata:"vector,dimensions=2"`
}

func TestReference(t *testing.T) {
	ctx := t.Context()
	records := []article{{"orthogonal", "included", []float32{0, 1}}, {"nearest", "included", []float32{1, 0}}, {"hidden", "other", []float32{1, 0}}}
	store := memory.New()
	c, err := vectordata.EnsureTypedModelCollection[string, article](ctx, store, "articles", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Upsert(ctx, records); err != nil {
		t.Fatal(err)
	}
	hits, err := c.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("Category"), "included")})
	if err != nil || len(hits) != 2 {
		t.Fatalf("search: %+v, %v", hits, err)
	}
	type result struct {
		Ranked  []string
		Vector  []float32
		Deleted bool
	}
	got := result{Ranked: []string{hits[0].Item.Id, hits[1].Item.Id}}
	top, err := c.Get(ctx, got.Ranked[0], vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil {
		t.Fatal(err)
	}
	got.Vector = top.Vector
	if _, err := c.DeleteOne(ctx, top.Id); err != nil {
		t.Fatal(err)
	}
	_, found, err := c.TryGet(ctx, top.Id, vectordata.RetrievalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got.Deleted = !found
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	ref := exec.CommandContext(ctx, "dotnet", "Reference.dll")
	ref.Stdin = bytes.NewReader(data)
	output, err := ref.CombinedOutput()
	if err != nil {
		t.Fatalf("reference: %v: %s", err, output)
	}
	var want result
	if err = json.Unmarshal(output, &want); err != nil {
		t.Fatalf("reference output: %s: %v", output, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go: %+v; .NET: %+v", got, want)
	}
}
