package sqlite_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func ExampleOpen() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "vectorstore-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "documents.db")
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}}}
	write := func() error {
		store, err := sqlite.Open(ctx, path, sqlite.StoreOptions{})
		if err != nil {
			return err
		}
		defer store.Close()
		documents, err := store.EnsureModelCollection(ctx, "docs", definition, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		_, err = documents.Upsert(ctx, []vectordata.ModelRecord{{Key: "guide", Data: map[string]any{"Title": "Mountain guide"}}})
		return err
	}
	if err := write(); err != nil {
		panic(err)
	}
	reopened, err := sqlite.Open(ctx, path, sqlite.StoreOptions{})
	if err != nil {
		panic(err)
	}
	defer reopened.Close()
	documents, err := reopened.GetModelCollection("docs", definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		panic(err)
	}
	record, err := documents.Get(ctx, "guide", vectordata.RetrievalOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(record.Key, record.Data["Title"])
	// Output: guide Mountain guide
}
