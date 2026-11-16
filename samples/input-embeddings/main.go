// This sample uses a deterministic fixture, not an image embedding model.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type ImageInput struct {
	Caption string
	Pixels  []byte
}

type Photo struct {
	ID    string     `vectordata:"key"`
	Title string     `vectordata:"data"`
	Image ImageInput `vectordata:"vector,input,dimensions=2,metric=l2"`
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	embedder := vectordata.InputEmbeddingFunc[ImageInput](func(ctx context.Context, inputs []ImageInput) ([][]float32, error) {
		vectors := make([][]float32, len(inputs))
		for i, input := range inputs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(input.Pixels) == 0 {
				return nil, fmt.Errorf("image pixels are required")
			}
			// Replace this fixture with your application's image/multimodal model.
			vectors[i] = []float32{float32(input.Pixels[0]), float32(len(input.Caption))}
		}
		return vectors, nil
	})
	store, err := memory.NewWithOptions(memory.StoreOptions{InputEmbedder: embedder, EmbeddingBatchSize: 2})
	if err != nil {
		return err
	}
	photos, err := vectordata.EnsureTypedModelCollection[string, Photo](ctx, store, "photos", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	_, err = photos.Upsert(ctx, []Photo{
		{ID: "cat", Title: "Cat", Image: ImageInput{Caption: "cat", Pixels: []byte{1}}},
		{ID: "ocean", Title: "Ocean", Image: ImageInput{Caption: "ocean", Pixels: []byte{9}}},
	})
	if err != nil {
		return err
	}
	hits, err := photos.SearchInput(ctx, ImageInput{Caption: "cat", Pixels: []byte{1}}, 1, vectordata.ModelSearchOptions{})
	if err != nil {
		return err
	}
	if len(hits) != 1 {
		return fmt.Errorf("expected one photo, got %d", len(hits))
	}
	// Only the vector and data properties are stored. Source inputs are transient.
	fmt.Printf("%s: distance=%g, input retained=%t\n", hits[0].Item.Title, hits[0].Distance, hits[0].Item.Image.Pixels != nil)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
