package postgres

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func testCollection() *PostgresCollection {
	return &PostgresCollection{
		store: &PostgresVectorStore{
			opts: StoreOptions{Schema: "public"},
		},
		name:      "docs",
		dimension: 3,
		metric:    vectordata.DistanceCosine,
	}
}

func TestBuildSearchPlanRejectsInvalidTopK(t *testing.T) {
	// Arrange
	collection := testCollection()

	// Act
	_, err := collection.buildSearchPlan([]float32{1, 2, 3}, 0, vectordata.SearchOptions{})

	// Assert
	if !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
		t.Fatalf("expected ErrInvalidSearchOptions, got %v", err)
	}
}

func TestBuildSearchPlanHonorsProjectionThresholdAndFilter(t *testing.T) {
	// Arrange
	collection := testCollection()
	threshold := 0.3
	projection := vectordata.Projection{IncludeVector: true}

	// Act
	plan, err := collection.buildSearchPlan([]float32{1, 0, 0}, 7, vectordata.SearchOptions{
		Filter:     vectordata.Eq(vectordata.Metadata("category"), "news"),
		Projection: &projection,
		Threshold:  &threshold,
	})

	// Assert
	if err != nil {
		t.Fatalf("buildSearchPlan: %v", err)
	}
	expectedQuery := `SELECT "id", "vector"::text, ("vector" <=> $1::vector) AS distance FROM "public"."docs" WHERE (("metadata" #> ARRAY['category']) = $2::jsonb) AND (("vector" <=> $1::vector) <= $3) ORDER BY distance ASC LIMIT $4`
	if plan.query != expectedQuery {
		t.Fatalf("unexpected query\nwant: %s\n got: %s", expectedQuery, plan.query)
	}
	if !reflect.DeepEqual(plan.args, []any{"[1,0,0]", []byte(`"news"`), threshold, 7}) {
		t.Fatalf("unexpected args: %#v", plan.args)
	}
	if plan.projection != projection {
		t.Fatalf("unexpected projection: %#v", plan.projection)
	}
}

func TestBuildSearchPlanUsesDefaultProjection(t *testing.T) {
	// Arrange
	collection := testCollection()

	// Act
	plan, err := collection.buildSearchPlan([]float32{1, 0, 0}, 5, vectordata.SearchOptions{})

	// Assert
	if err != nil {
		t.Fatalf("buildSearchPlan: %v", err)
	}
	expectedQuery := `SELECT "id", "metadata", "content", ("vector" <=> $1::vector) AS distance FROM "public"."docs" ORDER BY distance ASC LIMIT $2`
	if plan.query != expectedQuery {
		t.Fatalf("unexpected query\nwant: %s\n got: %s", expectedQuery, plan.query)
	}
	if !reflect.DeepEqual(plan.args, []any{"[1,0,0]", 5}) {
		t.Fatalf("unexpected args: %#v", plan.args)
	}
	if plan.projection != vectordata.DefaultProjection() {
		t.Fatalf("unexpected projection: %#v", plan.projection)
	}
}

func TestBuildSearchPlanRejectsInvalidVectorAndMetric(t *testing.T) {
	tests := []struct {
		name       string
		collection *PostgresCollection
		vector     []float32
		want       error
	}{
		{
			name:       "dimension mismatch",
			collection: testCollection(),
			vector:     []float32{1, 2},
			want:       vectordata.ErrDimensionMismatch,
		},
		{
			name: "unsupported metric",
			collection: &PostgresCollection{
				store:     &PostgresVectorStore{opts: StoreOptions{Schema: "public"}},
				name:      "docs",
				dimension: 3,
				metric:    "bad",
			},
			vector: []float32{1, 2, 3},
			want:   vectordata.ErrSchemaMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.collection.buildSearchPlan(tt.vector, 1, vectordata.SearchOptions{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestBuildWriteBatchRejectsInvalidRecord(t *testing.T) {
	// Arrange
	collection := testCollection()

	// Act
	_, _, err := collection.buildWriteBatch([]vectordata.Record{{
		ID:     "   ",
		Vector: []float32{1, 2, 3},
	}}, writeModeInsert)

	// Assert
	if !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("expected ErrInvalidRecord, got %v", err)
	}
}

func TestBuildWriteBatchRejectsUnserializableMetadata(t *testing.T) {
	// Arrange
	collection := testCollection()

	// Act
	_, _, err := collection.buildWriteBatch([]vectordata.Record{{
		ID:       "r1",
		Vector:   []float32{1, 2, 3},
		Metadata: map[string]any{"bad": func() {}},
	}}, writeModeInsert)

	// Assert
	if !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("expected ErrInvalidRecord, got %v", err)
	}
}

func TestBuildWriteBatchBuildsInsertAndUpsertSQL(t *testing.T) {
	// Arrange
	collection := testCollection()
	content := "alpha"
	records := []vectordata.Record{
		{
			ID:       "a",
			Vector:   []float32{1, 0, 0},
			Metadata: map[string]any{"category": "news"},
			Content:  &content,
		},
		{
			ID:     "b",
			Vector: []float32{0, 1, 0},
		},
	}

	// Act
	insertSQL, insertArgs, insertErr := collection.buildWriteBatch(records, writeModeInsert)
	upsertSQL, upsertArgs, upsertErr := collection.buildWriteBatch(records[:1], writeModeUpsert)

	// Assert
	if insertErr != nil {
		t.Fatalf("buildWriteBatch insert: %v", insertErr)
	}
	expectedInsertSQL := `INSERT INTO "public"."docs" ("id", "vector", "metadata", "content") VALUES ($1, $2::vector, $3::jsonb, $4), ($5, $6::vector, $7::jsonb, $8)`
	if insertSQL != expectedInsertSQL {
		t.Fatalf("unexpected insert SQL\nwant: %s\n got: %s", expectedInsertSQL, insertSQL)
	}
	if !reflect.DeepEqual(insertArgs, []any{"a", "[1,0,0]", []byte(`{"category":"news"}`), &content, "b", "[0,1,0]", []byte(`{}`), (*string)(nil)}) {
		t.Fatalf("unexpected insert args: %#v", insertArgs)
	}

	if upsertErr != nil {
		t.Fatalf("buildWriteBatch upsert: %v", upsertErr)
	}
	expectedUpsertSQL := `INSERT INTO "public"."docs" ("id", "vector", "metadata", "content") VALUES ($1, $2::vector, $3::jsonb, $4) ON CONFLICT ("id") DO UPDATE SET "vector" = EXCLUDED."vector", "metadata" = EXCLUDED."metadata", "content" = EXCLUDED."content"`
	if upsertSQL != expectedUpsertSQL {
		t.Fatalf("unexpected upsert SQL\nwant: %s\n got: %s", expectedUpsertSQL, upsertSQL)
	}
	if !reflect.DeepEqual(upsertArgs, insertArgs[:4]) {
		t.Fatalf("unexpected upsert args: %#v", upsertArgs)
	}
}

func TestBuildWriteBatchRejectsDimensionMismatch(t *testing.T) {
	// Arrange
	collection := testCollection()

	// Act
	_, _, err := collection.buildWriteBatch([]vectordata.Record{{
		ID:     "r1",
		Vector: []float32{1, 2},
	}}, writeModeInsert)

	// Assert
	if !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch, got %v", err)
	}
}

func TestBuildVectorIndexWithClauseValidatesOptions(t *testing.T) {
	tests := []struct {
		name   string
		method vectordata.IndexMethod
		opts   *vectordata.VectorIndexOptions
	}{
		{
			name:   "hnsw m too small",
			method: vectordata.IndexMethodHNSW,
			opts:   &vectordata.VectorIndexOptions{HNSW: vectordata.HNSWOptions{M: 1}},
		},
		{
			name:   "hnsw ef too small",
			method: vectordata.IndexMethodHNSW,
			opts:   &vectordata.VectorIndexOptions{HNSW: vectordata.HNSWOptions{EfConstruction: 3}},
		},
		{
			name:   "ivfflat negative lists",
			method: vectordata.IndexMethodIVFFlat,
			opts:   &vectordata.VectorIndexOptions{IVFFlat: vectordata.IVFFlatOptions{Lists: -1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := buildVectorIndexWithClause(tt.method, tt.opts)

			// Assert
			if !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
				t.Fatalf("expected ErrInvalidSearchOptions, got %v", err)
			}
		})
	}
}

func TestBuildVectorIndexWithClauseKeepsZeroAsDefault(t *testing.T) {
	// Act
	hnsw, hnswErr := buildVectorIndexWithClause(vectordata.IndexMethodHNSW, &vectordata.VectorIndexOptions{})
	ivf, ivfErr := buildVectorIndexWithClause(vectordata.IndexMethodIVFFlat, &vectordata.VectorIndexOptions{})

	// Assert
	if hnswErr != nil {
		t.Fatalf("hnsw default: %v", hnswErr)
	}
	if hnsw != " WITH (m = 16, ef_construction = 64)" {
		t.Fatalf("unexpected hnsw clause: %s", hnsw)
	}
	if ivfErr != nil {
		t.Fatalf("ivfflat default: %v", ivfErr)
	}
	if ivf != " WITH (lists = 100)" {
		t.Fatalf("unexpected ivfflat clause: %s", ivf)
	}
}
