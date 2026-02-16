package mssql

import (
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestMatchesFilterMetadataAndComparison(t *testing.T) {
	content := "payload"
	record := vectordata.Record{
		ID:      "doc-1",
		Vector:  []float32{1, 0},
		Content: &content,
		Metadata: map[string]any{
			"category": "news",
			"rank":     2,
			"nested": map[string]any{
				"k": "v",
			},
		},
	}

	filter := vectordata.And(
		vectordata.Eq(vectordata.Metadata("category"), "news"),
		vectordata.Gt(vectordata.Metadata("rank"), 1),
		vectordata.Eq(vectordata.Metadata("nested", "k"), "v"),
	)

	matched, err := matchesFilter(filter, record)
	if err != nil {
		t.Fatalf("matchesFilter: %v", err)
	}
	if !matched {
		t.Fatalf("expected record to match filter")
	}
}

func TestMatchesFilterColumnExists(t *testing.T) {
	content := "x"
	record := vectordata.Record{ID: "doc-1", Content: &content}

	matched, err := matchesFilter(vectordata.Exists(vectordata.Column(contentColumn)), record)
	if err != nil {
		t.Fatalf("matchesFilter: %v", err)
	}
	if !matched {
		t.Fatalf("expected content exists filter to match")
	}
}

func TestMatchesFilterUnknownColumnReturnsError(t *testing.T) {
	record := vectordata.Record{ID: "doc-1"}
	_, err := matchesFilter(vectordata.Eq(vectordata.Column("unknown"), "x"), record)
	if err == nil {
		t.Fatalf("expected error for unknown column")
	}
}

func TestMatchesFilterTrimsFieldReferences(t *testing.T) {
	content := "x"
	record := vectordata.Record{
		ID:      "doc-1",
		Content: &content,
		Metadata: map[string]any{
			"nested": map[string]any{"key": "value"},
		},
	}

	columnMatch, err := matchesFilter(vectordata.Eq(vectordata.Column("  id "), "doc-1"), record)
	if err != nil {
		t.Fatalf("column match: %v", err)
	}
	if !columnMatch {
		t.Fatalf("expected trimmed column reference to match")
	}

	metadataMatch, err := matchesFilter(vectordata.Eq(vectordata.Metadata(" nested ", " key "), "value"), record)
	if err != nil {
		t.Fatalf("metadata match: %v", err)
	}
	if !metadataMatch {
		t.Fatalf("expected trimmed metadata path to match")
	}
}

func TestDistanceBetweenMetrics(t *testing.T) {
	left := []float32{1, 0}
	right := []float32{0.8, 0.2}

	cosineDistance, err := distanceBetween(vectordata.DistanceCosine, left, right)
	if err != nil {
		t.Fatalf("cosine distance: %v", err)
	}
	if cosineDistance < 0 {
		t.Fatalf("expected non-negative cosine distance, got %f", cosineDistance)
	}

	l2Distance, err := distanceBetween(vectordata.DistanceL2, left, right)
	if err != nil {
		t.Fatalf("l2 distance: %v", err)
	}
	if l2Distance <= 0 {
		t.Fatalf("expected positive l2 distance, got %f", l2Distance)
	}

	innerProductDistance, err := distanceBetween(vectordata.DistanceInnerProduct, left, right)
	if err != nil {
		t.Fatalf("inner product distance: %v", err)
	}
	if innerProductDistance >= 0 {
		t.Fatalf("expected negative inner-product distance, got %f", innerProductDistance)
	}
}

func TestProjectRecordHonorsProjection(t *testing.T) {
	content := "hello"
	record := vectordata.Record{
		ID:       "doc-1",
		Vector:   []float32{1, 2},
		Content:  &content,
		Metadata: map[string]any{"category": "news"},
	}

	projected := projectRecord(record, vectordata.Projection{
		IncludeVector:   false,
		IncludeMetadata: true,
		IncludeContent:  false,
	})

	if len(projected.Vector) != 0 {
		t.Fatalf("expected vector to be omitted")
	}
	if projected.Metadata["category"] != "news" {
		t.Fatalf("expected metadata to be present")
	}
	if projected.Content != nil {
		t.Fatalf("expected content to be omitted")
	}
}
