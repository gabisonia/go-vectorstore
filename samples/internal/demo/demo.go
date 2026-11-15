// Package demo contains sample-only provider setup, fixtures and embedders.
// Applications should import vectordata and stores directly.
package demo

import (
	"context"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open selects memory by default. The returned cleanup always closes the owner.
func Open(ctx context.Context, dsn, path string, embedder vectordata.Embedder) (vectordata.ModelStore, func(), error) {
	if dsn != "" && path != "" {
		return nil, nil, fmt.Errorf("use either -dsn or -sqlite")
	}
	if dsn != "" {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return nil, nil, err
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			return nil, nil, err
		}
		opts := postgres.DefaultStoreOptions()
		opts.Embedder = embedder
		opts.EmbeddingBatchSize = 2
		store, err := postgres.NewVectorStore(pool, opts)
		if err != nil {
			pool.Close()
			return nil, nil, err
		}
		return store, pool.Close, nil
	}
	if path != "" {
		store, err := sqlite.Open(ctx, path, sqlite.StoreOptions{Embedder: embedder, EmbeddingBatchSize: 2})
		if err != nil {
			return nil, nil, err
		}
		return store, func() { _ = store.Close() }, nil
	}
	store, err := memory.NewWithOptions(memory.StoreOptions{Embedder: embedder, EmbeddingBatchSize: 2})
	return store, func() {}, err
}

// Article declares the typed model shared by the two retrieval samples.
// Definition overrides Dimensions for the selected embedding model.
type Article struct {
	ID       string    `vectordata:"key"`
	Title    string    `vectordata:"data"`
	Category string    `vectordata:"data,index"`
	Body     string    `vectordata:"data,fulltext"`
	Vector   []float32 `vectordata:"vector,dimensions=3,index,source=Body"`
}

func Definition(dimensions int) (vectordata.CollectionDefinition, error) {
	d, err := vectordata.DefinitionFor[Article]()
	if err != nil {
		return d, err
	}
	d.Vectors[0].Dimensions = dimensions
	return d.Normalize()
}
func Articles() []Article {
	return []Article{
		{ID: "costs", Title: "Reducing cloud costs", Category: "architecture", Body: "Reduce cloud costs with caching and batch writes."},
		{ID: "contexts", Title: "Go cancellation", Category: "backend", Body: "Use context deadlines to cancel hanging Go requests."},
		{ID: "vectors", Title: "Vector retrieval", Category: "engineering", Body: "Store embeddings and search vectors with pgvector."},
	}
}

// DeterministicEmbedder is a tiny keyword fixture, not a semantic model.
func DeterministicEmbedder() vectordata.Embedder {
	return vectordata.EmbeddingFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		vectors := make([][]float32, len(texts))
		for i, text := range texts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			text = strings.ToLower(text)
			v := []float32{0.1, 0.1, 0.1}
			for j, words := range [][]string{{"cost", "cach", "batch"}, {"context", "cancel", "deadline"}, {"vector", "embedding", "pgvector"}} {
				for _, word := range words {
					if strings.Contains(text, word) {
						v[j]++
					}
				}
			}
			vectors[i] = v
		}
		return vectors, nil
	})
}
