package vectordata_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type articleModel struct {
	ID        int64          `vectordata:"key,generated,name=article_id"`
	Title     string         `vectordata:"data,index"`
	Body      string         `vectordata:"data,fulltext"`
	Rank      int            `vectordata:"data"`
	Extra     map[string]any `vectordata:"data"`
	Optional  *string        `vectordata:"data"`
	Embedding []float32      `vectordata:"vector,dimensions=3,index,source=Body"`
}

func TestTypedModelsEmbeddingAndProjection(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	var calls atomic.Int64
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, inputs []string) ([][]float32, error) {
		calls.Add(1)
		vectors := make([][]float32, len(inputs))
		for i := range inputs {
			vectors[i] = []float32{1, 0, 0}
		}
		return vectors, nil
	})
	collection, err := vectordata.EnsureTypedModelCollection[int64, articleModel](ctx, store, "articles", nil, vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := collection.Upsert(ctx, []articleModel{{Title: "A", Body: "first", Rank: 1, Extra: map[string]any{"n": json.Number("9007199254740993")}}, {Title: "B", Body: "second", Rank: 2, Extra: map[string]any{}}})
	if err != nil || len(keys) != 2 || calls.Load() != 1 {
		t.Fatalf("upsert: %v, %v, calls=%d", keys, err, calls.Load())
	}
	first, err := collection.Get(ctx, keys[0], vectordata.RetrievalOptions{})
	if err != nil || first.ID != keys[0] || first.Embedding != nil || first.Extra["n"] != json.Number("9007199254740993") {
		t.Fatalf("get: %+v, %v", first, err)
	}
	first, err = collection.Get(ctx, keys[0], vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || len(first.Embedding) != 3 {
		t.Fatalf("vectors: %+v, %v", first, err)
	}
	results, err := collection.SearchText(ctx, "find", 2, vectordata.ModelSearchOptions{})
	if err != nil || len(results) != 2 || calls.Load() != 2 {
		t.Fatalf("text search: %+v, %v", results, err)
	}
	for _, err := range collection.QueryIter(ctx, nil, 2, vectordata.QueryOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	for _, err := range collection.SearchIter(ctx, []float32{1, 0, 0}, 2, vectordata.ModelSearchOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if _, err := collection.HybridSearch(ctx, []float32{1, 0, 0}, "word", 1, vectordata.HybridSearchOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("optional hybrid capability: %v", err)
	}
	if deleted, err := collection.Delete(ctx, []int64{keys[0]}); err != nil || deleted != 1 {
		t.Fatalf("delete: %d, %v", deleted, err)
	}
	if count, err := collection.Count(ctx, nil); err != nil || count != 1 {
		t.Fatalf("count: %d, %v", count, err)
	}
}
func TestEmbeddingErrorsDoNotWrite(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	failure := errors.New("embedding failed")
	for _, embedder := range []vectordata.Embedder{nil, vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { return nil, failure }), vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{{1}}, nil })} {
		c, err := vectordata.EnsureTypedModelCollection[int64, articleModel](ctx, store, "articles", nil, vectordata.ModelCollectionOptions{Embedder: embedder})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Upsert(ctx, []articleModel{{Title: "A", Body: "body", Extra: map[string]any{}}}); err == nil {
			t.Fatal("invalid embedding succeeded")
		}
		if count, err := c.Count(ctx, nil); err != nil || count != 0 {
			t.Fatalf("partial embedding write: %d, %v", count, err)
		}
	}
}
func TestDefinitionsAndCodecsRejectBadModels(t *testing.T) {
	if _, err := vectordata.DefinitionFor[int](); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatal(err)
	}
	type noKey struct {
		Title string `vectordata:"data"`
	}
	if _, err := vectordata.DefinitionFor[noKey](); err == nil {
		t.Fatal("missing key accepted")
	}
	type badRole struct {
		ID string `vectordata:"key,dimensions=3"`
	}
	if _, err := vectordata.DefinitionFor[badRole](); err == nil {
		t.Fatal("invalid tag option accepted")
	}
	type badField struct {
		ID     string    `vectordata:"key"`
		Vector []float64 `vectordata:"vector,dimensions=3"`
	}
	if _, err := vectordata.NewStructCodec[badField](nil); err == nil {
		t.Fatal("invalid vector field accepted")
	}
	type wrongKey struct{ ID string }
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}}
	if _, err := vectordata.NewStructCodec[wrongKey](&definition); err == nil {
		t.Fatal("invalid key field accepted")
	}
	d, err := vectordata.DefinitionFor[articleModel]()
	if err != nil {
		t.Fatal(err)
	}
	clone := d.Clone()
	clone.Data[0].Name = "mutated"
	clone.Vectors[0].Index.HNSW.M = 99
	if d.Data[0].Name == "mutated" || d.Vectors[0].Index.HNSW.M == 99 {
		t.Fatal("definition clone aliases input")
	}
	codec, err := vectordata.NewStructCodec[*articleModel](nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Encode(nil); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	model := &articleModel{ID: 5, Title: "value", Body: "body", Rank: 4, Extra: map[string]any{"n": json.Number("9007199254740993")}, Embedding: []float32{1, 0, 0}}
	record, err := codec.Encode(model)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(record)
	if err != nil || !reflect.DeepEqual(model, decoded) {
		t.Fatalf("pointer codec: %+v, %v", decoded, err)
	}
}
func TestMemoryConcurrentReadsAndWrites(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2}}}
	collection, err := store.EnsureModelCollection(ctx, "concurrent", definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := collection.Upsert(ctx, []vectordata.ModelRecord{{Key: int64(worker*20 + i), Vectors: map[string][]float32{"Vector": {1, 0}}}}); err != nil {
					t.Error(err)
				}
				if _, err := collection.Search(ctx, []float32{0, 0}, 3, vectordata.ModelSearchOptions{}); err != nil {
					t.Error(err)
				}
			}
		}(worker)
	}
	wg.Wait()
	if count, err := collection.Count(ctx, nil); err != nil || count != 160 {
		t.Fatalf("count: %d, %v", count, err)
	}
}

type uuidModel struct {
	ID   string `vectordata:"key,type=uuid"`
	Name string `vectordata:"data"`
}

func TestTypedUUIDAndReadOnlyHandles(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	collection, err := vectordata.GetTypedModelCollection[string, uuidModel](store, "uuid", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := collection.Exists(ctx); err != nil || exists {
		t.Fatalf("handle created storage: %v, %v", exists, err)
	}
	if _, err := vectordata.GetTypedModelCollection[int64, uuidModel](store, "invalid", nil, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("typed key mismatch: %v", err)
	}
	if err := collection.EnsureExists(ctx); err != nil {
		t.Fatal(err)
	}
	keys, err := collection.Upsert(ctx, []uuidModel{{ID: "8F516DB6-38CA-4E8F-AD79-D71B015F5419", Name: "uuid"}})
	if err != nil || len(keys) != 1 || keys[0] != "8f516db6-38ca-4e8f-ad79-d71b015f5419" {
		t.Fatalf("UUID keys: %v, %v", keys, err)
	}
}
func TestInvalidTextSearchDoesNotCallEmbedder(t *testing.T) {
	var calls atomic.Int64
	embedder := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls.Add(1); return [][]float32{{1, 0, 0}}, nil })
	store := memory.New()
	ctx := context.Background()
	c, err := vectordata.EnsureTypedModelCollection[int64, articleModel](ctx, store, "invalid_search", nil, vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SearchText(ctx, "text", 0, vectordata.ModelSearchOptions{}); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
		t.Fatalf("invalid top: %v", err)
	}
	if _, err := c.SearchText(ctx, "text", 1, vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("missing"), 1)}); !errors.Is(err, vectordata.ErrInvalidFilter) {
		t.Fatalf("invalid field: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("embedder called before validating search options")
	}
}
