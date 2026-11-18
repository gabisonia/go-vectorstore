// output-embeddings demonstrates a deterministic float16-output fixture.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Document struct {
	ID     string    `vectordata:"key"`
	Body   string    `vectordata:"data"`
	Vector []float32 `vectordata:"vector,dimensions=2,source=Body,metric=l2"`
}

func run(ctx context.Context) error {
	// Replace this instructional fixture with an application generator.
	// These are IEEE binary16 bits: 0x3c00 = 1 and 0x3800 = 0.5.
	source := vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits](func(ctx context.Context, texts []string) ([][]vectordata.Float16Bits, error) {
		out := make([][]vectordata.Float16Bits, len(texts))
		for i, text := range texts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			out[i] = []vectordata.Float16Bits{0x3c00, 0x3800}
			if text == "ocean" {
				out[i] = []vectordata.Float16Bits{0x4000, 0x3c00}
			}
		}
		return out, nil
	})
	adapter, err := vectordata.AdaptOutputEmbedder(source)
	if err != nil {
		return err
	}
	store, err := memory.NewWithOptions(memory.StoreOptions{InputEmbedder: adapter, EmbeddingBatchSize: 1})
	if err != nil {
		return err
	}
	collection, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, store, "outputs", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if _, err := collection.Upsert(ctx, []Document{{ID: "guide", Body: "guide"}, {ID: "ocean", Body: "ocean"}}); err != nil {
		return err
	}
	stored, err := collection.Get(ctx, "guide", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil {
		return err
	}
	fmt.Printf("Generated float16; canonical float32 vector: %v\n", stored.Vector)
	hits, err := collection.SearchText(ctx, "guide", 1, vectordata.ModelSearchOptions{})
	if err != nil {
		return err
	}
	if len(hits) != 1 {
		return fmt.Errorf("expected one result")
	}
	fmt.Printf("Nearest: %s (distance %.3f)\n", hits[0].Item.ID, hits[0].Distance)
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
