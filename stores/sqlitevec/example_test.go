package sqlitevec_test

import (
	"context"
	"fmt"

	"github.com/gabisonia/go-vectorstore/stores/sqlitevec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func ExampleOpen() {
	ctx := context.Background()
	store, err := sqlitevec.Open(ctx, ":memory:", sqlitevec.StoreOptions{})
	if err != nil {
		panic(err)
	}
	defer store.Close()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceL1}}}
	c, err := store.EnsureModelCollection(ctx, "documents", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		panic(err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "near", Vectors: map[string][]float32{"V": {1, 2}}}, {Key: "far", Vectors: map[string][]float32{"V": {9, 9}}}}); err != nil {
		panic(err)
	}
	hits, err := c.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].Record.Key, hits[0].Distance)
	// Output: near 3
}
