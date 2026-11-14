//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
	"github.com/jackc/pgx/v5/pgxpool"
)

type modelHotel struct {
	ID          int64          `vectordata:"key,generated,name=hotel_id"`
	Name        string         `vectordata:"data,index,name=hotel_name"`
	Description string         `vectordata:"data,fulltext"`
	Tags        []string       `vectordata:"data,index"`
	Details     map[string]any `vectordata:"data"`
	Rating      int            `vectordata:"data"`
	Vector      []float32      `vectordata:"vector,dimensions=3,index,source=Description"`
	Image       []float32      `vectordata:"vector,dimensions=2,metric=l2"`
}

func TestIntegrationModelCollectionEndToEnd(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	calls := 0
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, inputs []string) ([][]float32, error) {
		calls++
		out := make([][]float32, len(inputs))
		for i := range inputs {
			out[i] = []float32{1, 0, 0}
		}
		return out, nil
	})
	hotels, err := vectordata.EnsureTypedModelCollection[int64, modelHotel](ctx, store, "hotels", nil, vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	if err := hotels.EnsureExists(ctx); err != nil {
		t.Fatalf("idempotent ensure: %v", err)
	}
	keys, err := hotels.Upsert(ctx, []modelHotel{{Name: "Alpha", Description: "quiet mountain hotel", Tags: []string{"quiet", "mountain"}, Details: map[string]any{"rank": 1}, Rating: 5, Image: []float32{0, 0}}, {Name: "Beta", Description: "city business hotel", Tags: []string{"city"}, Details: map[string]any{"rank": 2}, Rating: 3, Image: []float32{1, 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == keys[1] || calls != 1 {
		t.Fatalf("keys=%v, embedding calls=%d", keys, calls)
	}
	got, err := hotels.Get(ctx, keys[0], vectordata.RetrievalOptions{})
	if err != nil || got.Name != "Alpha" || got.Vector != nil {
		t.Fatalf("get: %+v, %v", got, err)
	}
	got, err = hotels.Get(ctx, keys[0], vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || len(got.Vector) != 3 || len(got.Image) != 2 {
		t.Fatalf("vectors: %+v, %v", got, err)
	}
	many, err := hotels.GetMany(ctx, []int64{keys[0], 99999}, vectordata.RetrievalOptions{})
	if err != nil || len(many) != 1 {
		t.Fatalf("many: %+v, %v", many, err)
	}
	rows, err := hotels.Query(ctx, vectordata.And(vectordata.Contains(vectordata.Column("Tags"), "quiet"), vectordata.Gte(vectordata.Column("Rating"), 4)), 5, vectordata.QueryOptions{})
	if err != nil || len(rows) != 1 || rows[0].Name != "Alpha" {
		t.Fatalf("query: %+v, %v", rows, err)
	}
	count, err := hotels.Count(ctx, vectordata.Gt(vectordata.DataPath("Details", "rank"), 1))
	if err != nil || count != 1 {
		t.Fatalf("JSON filter: %d, %v", count, err)
	}
	results, err := hotels.SearchText(ctx, "mountain", 2, vectordata.ModelSearchOptions{VectorProperty: "Vector"})
	if err != nil || len(results) != 2 || calls != 2 {
		t.Fatalf("text search: %+v, %v", results, err)
	}
	if _, err := hotels.Search(ctx, []float32{1, 0, 0}, 1, vectordata.ModelSearchOptions{}); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
		t.Fatalf("ambiguous vector: %v", err)
	}
	image, err := hotels.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: "Image"})
	if err != nil || len(image) != 1 || image[0].Item.Name != "Alpha" {
		t.Fatalf("image: %+v, %v", image, err)
	}
	hybrid, err := hotels.HybridSearchText(ctx, "mountain", "mountain", 2, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{VectorProperty: "Vector"}, TextProperty: "Description"})
	if err != nil || len(hybrid) == 0 || hybrid[0].Item.Name != "Alpha" || hybrid[0].ScoreKind != vectordata.ScoreRRF {
		t.Fatalf("hybrid: %+v, %v", hybrid, err)
	}
	names, err := store.ListCollectionNames(ctx)
	if err != nil || len(names) != 1 || names[0] != "hotels" {
		t.Fatalf("names: %v, %v", names, err)
	}
	if err := hotels.EnsureDeleted(ctx); err != nil {
		t.Fatal(err)
	}
	if exists, err := hotels.Exists(ctx); err != nil || exists {
		t.Fatalf("exists: %v, %v", exists, err)
	}
}

func TestIntegrationModelContract(t *testing.T) {
	conformance.Run(t, func(t *testing.T) vectordata.ModelStore { return newTestStore(t, integrationPool(t)) })
}

func TestIntegrationModelIteratorsReleaseSingleConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(integrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := newTestStore(t, pool)
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}
	collection, err := store.EnsureModelCollection(ctx, "stream", definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collection.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Vectors: map[string][]float32{"Vector": {1, 0}}}, {Key: "b", Vectors: map[string][]float32{"Vector": {0, 1}}}}); err != nil {
		t.Fatal(err)
	}
	for _, err := range collection.QueryIter(ctx, nil, 2, vectordata.QueryOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	for _, err := range collection.SearchIter(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{HNSW: &vectordata.HNSWSearchOptions{EfSearch: 100}}}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if count, err := collection.Count(ctx, nil); err != nil || count != 2 {
		t.Fatalf("iterator leaked pooled connection: %d, %v", count, err)
	}
	var ef string
	if err := pool.QueryRow(ctx, "SELECT current_setting('hnsw.ef_search')").Scan(&ef); err != nil {
		t.Fatal(err)
	}
	if ef == "100" {
		t.Fatal("request settings leaked")
	}
	if err := collection.EnsureDeleted(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestIntegrationModelSchemaConflictsAndConcurrentEnsure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Category", Type: vectordata.TypeString, IsIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.EnsureModelCollection(ctx, "concurrent", definition, vectordata.ModelCollectionOptions{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := pool.Exec(ctx, "DROP INDEX "+qualifiedTable(store.opts.Schema, "idx_concurrent_Category_data")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX "+quoteIdent("idx_concurrent_Category_data")+" ON "+qualifiedTable(store.opts.Schema, "concurrent")+" (\"ID\")"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureModelCollection(ctx, "concurrent", definition, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("index conflict accepted: %v", err)
	}
}
func TestIntegrationLegacyRetrievalExtensions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := newTestStore(t, integrationPool(t))
	base, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "legacy", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	collection := base.(*PostgresCollection)
	records := benchmarkRecords(501, 2)
	if err := collection.Upsert(ctx, records); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(records))
	for i, r := range records {
		ids[i] = r.ID
	}
	many, err := collection.GetMany(ctx, ids, vectordata.RetrievalOptions{})
	if err != nil || len(many) != 501 || many[0].Vector != nil {
		t.Fatalf("batch read: %d, %v", len(many), err)
	}
	selected, err := collection.GetWithOptions(ctx, records[0].ID, vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || len(selected.Vector) != 2 {
		t.Fatal(err)
	}
	for _, err := range collection.QueryIter(ctx, nil, 1, vectordata.QueryOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if err := collection.EnsureDeleted(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationModelFilterParity(t *testing.T) {
	conformance.RunFilterParity(t,
		func(t *testing.T) vectordata.ModelStore { return memory.New() },
		func(t *testing.T) vectordata.ModelStore { return newTestStore(t, integrationPool(t)) },
	)
}
