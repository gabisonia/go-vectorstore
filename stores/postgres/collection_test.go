package postgres

import (
	"errors"
	"reflect"
	"strings"
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
	if !strings.Contains(plan.query, `"vector"::text`) {
		t.Fatalf("expected vector projection in query: %s", plan.query)
	}
	if strings.Contains(plan.query, `"metadata",`) || strings.Contains(plan.query, `"content",`) {
		t.Fatalf("metadata/content should not be projected: %s", plan.query)
	}
	if !strings.Contains(plan.query, `("metadata" #> ARRAY['category']) = $2::jsonb`) {
		t.Fatalf("expected metadata filter in query: %s", plan.query)
	}
	if !strings.Contains(plan.query, `(("vector" <=> $1::vector) <= $3)`) {
		t.Fatalf("expected threshold predicate in query: %s", plan.query)
	}
	if !reflect.DeepEqual(plan.args, []any{"[1,0,0]", []byte(`"news"`), threshold, 7}) {
		t.Fatalf("unexpected args: %#v", plan.args)
	}
	if plan.projection != projection {
		t.Fatalf("unexpected projection: %#v", plan.projection)
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
