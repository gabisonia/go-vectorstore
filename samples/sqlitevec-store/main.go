// sqlitevec-store demonstrates optional native scalar L1 candidate execution.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/sqlitevec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("db", "sqlitevec.db", "SQLite database filename")
	rebuild := flag.Bool("rebuild-vectors", false, "Explicitly backfill existing/dirty vector projections")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := sqlitevec.Open(ctx, *path, sqlitevec.StoreOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceL1, Optional: true}}}
	c, err := store.EnsureModelCollection(ctx, "sqlitevec_documents", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if *rebuild {
		if err := c.(*sqlitevec.Collection).RebuildVectorProjections(ctx); err != nil {
			return err
		}
	}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{
		{Key: "near", Data: map[string]any{"Title": "Packing light"}, Vectors: map[string][]float32{"V": {1, 2}}},
		{Key: "far", Data: map[string]any{"Title": "Long journey"}, Vectors: map[string][]float32{"V": {8, 4}}},
		{Key: "draft", Data: map[string]any{"Title": "Work in progress"}},
	}); err != nil {
		return err
	}
	hits, err := c.Search(ctx, []float32{0, 0}, 2, vectordata.ModelSearchOptions{})
	if err != nil {
		return err
	}
	for _, hit := range hits {
		fmt.Printf("%s: L1 distance %.0f\n", hit.Record.Key, hit.Distance)
	}
	if err := store.Close(); err != nil {
		return err
	}
	store = nil
	reopened, err := sqlitevec.Open(ctx, *path, sqlitevec.StoreOptions{})
	if err != nil {
		return err
	}
	defer reopened.Close()
	c, err = reopened.GetModelCollection("sqlitevec_documents", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	hits, err = c.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{IncludeVectors: true})
	if err != nil {
		return err
	}
	if len(hits) != 1 {
		return fmt.Errorf("unexpected reopened matches: %d", len(hits))
	}
	fmt.Printf("After reopen: %s (dimensions %d)\n", hits[0].Record.Key, len(hits[0].Record.Vectors["V"]))
	return nil
}
