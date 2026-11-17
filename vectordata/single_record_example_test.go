package vectordata_test

import (
	"context"
	"fmt"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func ExampleTypedModelCollection_TryGet() {
	type Note struct {
		ID   string `vectordata:"key"`
		Text string `vectordata:"data"`
	}
	ctx := context.Background()
	c, err := vectordata.EnsureTypedModelCollection[string, Note](ctx, memory.New(), "notes", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		panic(err)
	}
	key, err := c.UpsertOne(ctx, Note{ID: "intro", Text: "Hello"})
	if err != nil {
		panic(err)
	}
	note, found, err := c.TryGet(ctx, key, vectordata.RetrievalOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(note.Text, found)
	_, found, err = c.TryGet(ctx, "absent", vectordata.RetrievalOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println("missing:", found)
	deleted, err := c.DeleteOne(ctx, key)
	if err != nil {
		panic(err)
	}
	fmt.Println("deleted:", deleted)
	// Output:
	// Hello true
	// missing: false
	// deleted: true
}
