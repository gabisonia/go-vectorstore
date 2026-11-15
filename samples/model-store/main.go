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
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
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
	sqlitePath := flag.String("sqlite", "", "Optional SQLite database file; mutually exclusive with -dsn")
	batchSize := flag.Int("embedding-batch-size", 2, "Maximum texts per embedding request (0 uses one request per property)")
	flag.Parse()
	if *dsn != "" && *sqlitePath != "" {
		return fmt.Errorf("use either -dsn or -sqlite")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
	// Configure once on the store; collections can override either setting.
	var store vectordata.ModelStore
	if *dsn != "" {
		pool, err := pgxpool.New(ctx, *dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		opts := postgres.DefaultStoreOptions()
		opts.Embedder = embedder
		opts.EmbeddingBatchSize = *batchSize
		provider, err := postgres.NewVectorStore(pool, opts)
		if err != nil {
			return err
		}
		store = provider
	} else if *sqlitePath != "" {
		provider, err := sqlite.Open(ctx, *sqlitePath, sqlite.StoreOptions{Embedder: embedder, EmbeddingBatchSize: *batchSize})
		if err != nil {
			return err
		}
		defer provider.Close()
		store = provider
	} else {
		provider, err := memory.NewWithOptions(memory.StoreOptions{Embedder: embedder, EmbeddingBatchSize: *batchSize})
		if err != nil {
			return err
		}
		store = provider
	}
	hotels, err := vectordata.EnsureTypedModelCollection[int64, Hotel](ctx, store, "model_hotels", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	keys, err := hotels.Upsert(ctx, []Hotel{{Name: "Mountain Lodge", Description: "A quiet hotel in the mountains", Tags: []string{"quiet", "nature"}}, {Name: "City Inn", Description: "A city hotel near the station", Tags: []string{"city"}}, {Name: "Ocean House", Description: "An ocean hotel by the beach", Tags: []string{"ocean", "nature"}}})
	if err != nil {
		return err
	}
	fmt.Printf("Inserted hotel keys: %v\n", keys)
	matches, err := hotels.SearchText(ctx, "quiet", 2, vectordata.ModelSearchOptions{Filter: vectordata.ContainsAny(vectordata.Column("Tags"), "nature", "quiet")})
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
