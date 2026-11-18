// qdrant-store demonstrates bounded durable canonical models and exact search.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/qdrant"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Document struct {
	ID       string    `vectordata:"key"`
	Title    string    `vectordata:"data"`
	Category string    `vectordata:"data"`
	Vector   []float32 `vectordata:"vector,dimensions=3"`
}

func main() {
	url := flag.String("url", "http://localhost:63339", "Standalone Qdrant 1.20.2 URL")
	namespace := flag.String("namespace", "qdrant_demo", "Dedicated namespace for retained fixtures")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, qdrant.StoreOptions{URL: *url, APIKey: os.Getenv("QDRANT_API_KEY"), Namespace: *namespace}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts qdrant.StoreOptions) error {
	s, err := qdrant.New(opts)
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, s, "documents", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if _, err := c.Upsert(ctx, []Document{
		{ID: "architecture", Title: "System architecture", Category: "guide", Vector: []float32{1, 0, 0}},
		{ID: "storage", Title: "Storage choices", Category: "guide", Vector: []float32{0.8, 0.2, 0}},
		{ID: "release", Title: "Release notes", Category: "news", Vector: []float32{0, 1, 0}},
	}); err != nil {
		return err
	}
	filter := vectordata.Eq(vectordata.Column("Category"), "guide")
	n, err := c.Count(ctx, filter)
	if err != nil {
		return err
	}
	fmt.Printf("Guides: %d\n", n)
	hits, err := c.Search(ctx, []float32{1, 0, 0}, 1, vectordata.ModelSearchOptions{Filter: filter})
	if err != nil {
		return err
	}
	if len(hits) != 1 {
		return fmt.Errorf("expected one nearest guide")
	}
	fmt.Printf("Nearest: %s (distance %.3f)\n", hits[0].Item.Title, hits[0].Distance)
	s.Close()
	reopened, err := qdrant.New(opts)
	if err != nil {
		return err
	}
	defer reopened.Close()
	handle, err := vectordata.GetTypedModelCollection[string, Document](reopened, "documents", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	n, err = handle.Count(ctx, nil)
	if err != nil {
		return err
	}
	fmt.Printf("Records after reopen: %d\n", n)
	return nil
}
