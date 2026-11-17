// sqlite-store demonstrates durable typed collections with optional embeddings.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"os"
	"time"
)

type Document struct {
	ID        string    `vectordata:"key"`
	Title     string    `vectordata:"data"`
	Tags      []string  `vectordata:"data,index"`
	Embedding []float32 `vectordata:"vector,dimensions=2,optional"`
}

func run() error {
	path := flag.String("db", "vectors.db", "SQLite database filename")
	native := flag.Bool("native", false, "Use scalar indexes in a separate indexed_documents collection")
	rebuild := flag.Bool("rebuild-indexes", false, "Explicitly backfill scalar indexes; implies -native")
	flag.Parse()
	if *rebuild {
		*native = true
	}
	options := sqlite.StoreOptions{NativeIndexes: *native}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, *path, options)
	if err != nil {
		return err
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	definition, err := vectordata.DefinitionFor[Document]()
	if err != nil {
		return err
	}
	name := "documents"
	if *native {
		name = "indexed_documents"
		for i := range definition.Data {
			if definition.Data[i].Name == "Title" {
				definition.Data[i].IsIndexed = true
			}
		}
	}
	docs, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, store, name, &definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if *rebuild {
		base, err := store.GetModelCollection(name, definition, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if err := base.(*sqlite.Collection).RebuildScalarIndexes(ctx); err != nil {
			return err
		}
	}
	if _, err = docs.Upsert(ctx, []Document{
		{ID: "ready", Title: "Mountain guide", Tags: []string{"nature", "quiet"}, Embedding: []float32{1, 0}},
		{ID: "pending", Title: "Draft guide", Tags: []string{"nature"}},
	}); err != nil {
		return err
	}
	if *native {
		count, err := docs.Count(ctx, vectordata.Eq(vectordata.Column("Title"), "Mountain guide"))
		if err != nil {
			return err
		}
		fmt.Printf("Indexed titles: %d\n", count)
	}
	rows, err := docs.Query(ctx, vectordata.ContainsAll(vectordata.Column("Tags"), "nature"), 10, vectordata.QueryOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("Filtered documents: %d (includes the draft without a vector)\n", len(rows))
	hits, err := docs.Search(ctx, []float32{1, 0}, 10, vectordata.ModelSearchOptions{Filter: vectordata.ContainsAny(vectordata.Column("Tags"), "nature", "city")})
	if err != nil {
		return err
	}
	for _, hit := range hits {
		fmt.Printf("Search: %s (score %.3f)\n", hit.Item.Title, hit.Score)
	}
	if err = store.Close(); err != nil {
		return err
	}
	store = nil
	reopened, err := sqlite.Open(ctx, *path, options)
	if err != nil {
		return err
	}
	defer reopened.Close()
	saved, err := vectordata.GetTypedModelCollection[string, Document](reopened, name, &definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	draft, err := saved.Get(ctx, "pending", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil {
		return err
	}
	fmt.Printf("After reopen: %s (vector absent: %t)\n", draft.Title, draft.Embedding == nil)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
