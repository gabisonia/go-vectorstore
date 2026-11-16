package vectordata_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func Example_typedSearch() {
	type Document struct {
		ID       string    `vectordata:"key"`
		Category string    `vectordata:"data,index"`
		Vector   []float32 `vectordata:"vector,dimensions=2"`
	}
	ctx := context.Background()
	documents, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, memory.New(), "docs", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		panic(err)
	}
	_, err = documents.Upsert(ctx, []Document{{ID: "guide", Category: "travel", Vector: []float32{1, 0}}, {ID: "news", Category: "news", Vector: []float32{0, 1}}})
	if err != nil {
		panic(err)
	}
	hits, err := documents.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("Category"), "travel")})
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].Item.ID, hits[0].Distance, len(hits[0].Item.Vector))
	// Output: guide 0 0
}

func ExampleEmbeddingFunc() {
	type Document struct {
		ID     string    `vectordata:"key"`
		Body   string    `vectordata:"data"`
		Vector []float32 `vectordata:"vector,dimensions=2,source=Body"`
	}
	// A deterministic fixture; real applications supply a semantic embedder.
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range texts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			out[i] = []float32{1, 0}
		}
		return out, nil
	})
	ctx := context.Background()
	documents, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, memory.New(), "docs", nil, vectordata.ModelCollectionOptions{Embedder: embedder, EmbeddingBatchSize: 2})
	if err != nil {
		panic(err)
	}
	if _, err := documents.Upsert(ctx, []Document{{ID: "guide", Body: "Walking in the mountains"}}); err != nil {
		panic(err)
	}
	hits, err := documents.SearchText(ctx, "mountains", 1, vectordata.ModelSearchOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].Item.ID, hits[0].ScoreKind)
	// Output: guide similarity
}

func ExampleInputEmbeddingFunc() {
	type Photo struct {
		ID    string `vectordata:"key"`
		Image []byte `vectordata:"vector,input,dimensions=2,metric=l2"`
	}
	// A deterministic fixture; supply an image model in a real application.
	embedder := vectordata.InputEmbeddingFunc[[]byte](func(ctx context.Context, images [][]byte) ([][]float32, error) {
		out := make([][]float32, len(images))
		for i, image := range images {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			out[i] = []float32{float32(len(image)), 1}
		}
		return out, nil
	})
	ctx := context.Background()
	photos, err := vectordata.EnsureTypedModelCollection[string, Photo](ctx, memory.New(), "photos", nil, vectordata.ModelCollectionOptions{InputEmbedder: embedder})
	if err != nil {
		panic(err)
	}
	if _, err := photos.Upsert(ctx, []Photo{{ID: "cat", Image: []byte{1, 2, 3}}}); err != nil {
		panic(err)
	}
	hits, err := photos.SearchInput(ctx, []byte{4, 5, 6}, 1, vectordata.ModelSearchOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].Item.ID, hits[0].Distance, len(hits[0].Item.Image))
	// Output: cat 0 0
}

func Example_namedVectors() {
	type Document struct {
		ID    string    `vectordata:"key"`
		Title []float32 `vectordata:"vector,dimensions=2"`
		Body  []float32 `vectordata:"vector,dimensions=2"`
	}
	ctx := context.Background()
	documents, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, memory.New(), "docs", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		panic(err)
	}
	if _, err := documents.Upsert(ctx, []Document{{ID: "guide", Title: []float32{1, 0}, Body: []float32{0, 1}}}); err != nil {
		panic(err)
	}
	hits, err := documents.Search(ctx, []float32{0, 1}, 1, vectordata.ModelSearchOptions{VectorProperty: "Body", IncludeVectors: true})
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].Item.ID, hits[0].Distance, len(hits[0].Item.Body))
	// Output: guide 0 2
}

func Example_errorHandling() {
	ctx := context.Background()
	collection, err := memory.New().EnsureModelCollection(ctx, "docs", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		panic(err)
	}
	_, err = collection.Get(ctx, "missing", vectordata.RetrievalOptions{})
	fmt.Println(errors.Is(err, vectordata.ErrNotFound))
	_, err = collection.Query(ctx, vectordata.Eq(vectordata.Column("Typo"), "x"), 1, vectordata.QueryOptions{})
	fmt.Println(errors.Is(err, vectordata.ErrInvalidFilter))
	// Output:
	// true
	// true
}
