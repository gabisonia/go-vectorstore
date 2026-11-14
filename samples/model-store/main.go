// The model-store sample needs no API key. Its tiny deterministic embedder is
// instructional; configure a real embedding model for semantic search.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Hotel struct {
	ID          int64     `vectordata:"key,generated,name=hotel_id"`
	Name        string    `vectordata:"data,index"`
	Description string    `vectordata:"data,fulltext"`
	Tags        []string  `vectordata:"data,index"`
	Embedding   []float32 `vectordata:"vector,dimensions=3,index,source=Description"`
}

func run() error {
	dsn := flag.String("dsn", "", "Optional PostgreSQL connection string; defaults to in-memory storage")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var store vectordata.ModelStore = memory.New()
	if *dsn != "" {
		pool, err := pgxpool.New(ctx, *dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		provider, err := postgres.NewVectorStore(pool, postgres.DefaultStoreOptions())
		if err != nil {
			return err
		}
		store = provider
	}
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, inputs []string) ([][]float32, error) {
		vectors := make([][]float32, len(inputs))
		for i, text := range inputs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			text = strings.ToLower(text)
			vector := []float32{0.1, 0.1, 0.1}
			for j, word := range []string{"quiet", "city", "ocean"} {
				if strings.Contains(text, word) {
					vector[j]++
				}
			}
			vectors[i] = vector
		}
		return vectors, nil
	})
	hotels, err := vectordata.EnsureTypedModelCollection[int64, Hotel](ctx, store, "model_hotels", nil, vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		return err
	}
	keys, err := hotels.Upsert(ctx, []Hotel{{Name: "Mountain Lodge", Description: "A quiet hotel in the mountains", Tags: []string{"quiet", "nature"}}, {Name: "City Inn", Description: "A city hotel near the station", Tags: []string{"city"}}, {Name: "Ocean House", Description: "An ocean hotel by the beach", Tags: []string{"ocean", "nature"}}})
	if err != nil {
		return err
	}
	fmt.Printf("Inserted hotel keys: %v\n", keys)
	matches, err := hotels.SearchText(ctx, "quiet", 2, vectordata.ModelSearchOptions{Filter: vectordata.Contains(vectordata.Column("Tags"), "nature")})
	if err != nil {
		return err
	}
	for _, match := range matches {
		fmt.Printf("%s: score %.3f\n", match.Item.Name, match.Score)
	}
	rows, err := hotels.GetMany(ctx, keys, vectordata.RetrievalOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("Read %d typed hotels without loading vectors\n", len(rows))
	// Remove just this run's generated records so the sample is safe to rerun.
	if _, err := hotels.Delete(ctx, keys); err != nil {
		return err
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
