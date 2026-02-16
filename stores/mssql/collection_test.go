package mssql

import (
	"container/heap"
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestBuildUpsertQueryUsesLockingPattern(t *testing.T) {
	query := buildUpsertQuery("[dbo].[docs]")

	if !strings.Contains(query, "WITH (UPDLOCK, SERIALIZABLE)") {
		t.Fatalf("expected upsert query to use locking hint, got: %s", query)
	}
	if !strings.Contains(query, "IF @@ROWCOUNT = 0") {
		t.Fatalf("expected upsert query to insert on miss, got: %s", query)
	}
	if !strings.Contains(query, "INSERT INTO [dbo].[docs]") {
		t.Fatalf("expected upsert query to target provided table, got: %s", query)
	}
}

func TestSearchResultHeapKeepsBestTopK(t *testing.T) {
	const topK = 3

	h := make(searchResultMaxHeap, 0, topK)
	heap.Init(&h)

	pushTopK := func(id string, distance float64) {
		candidate := vectordata.SearchResult{
			Record:   vectordata.Record{ID: id},
			Distance: distance,
		}
		if h.Len() < topK {
			heap.Push(&h, candidate)
			return
		}
		if isBetterResult(candidate, h[0]) {
			heap.Pop(&h)
			heap.Push(&h, candidate)
		}
	}

	pushTopK("x", 0.40)
	pushTopK("a", 0.10)
	pushTopK("b", 0.20)
	pushTopK("c", 0.20) // Same distance as b; tie-break should keep b.
	pushTopK("d", 0.05)

	results := make([]vectordata.SearchResult, 0, topK)
	for h.Len() > 0 {
		results = append(results, heap.Pop(&h).(vectordata.SearchResult))
	}
	sort.Slice(results, func(i, j int) bool { return isBetterResult(results[i], results[j]) })

	if len(results) != topK {
		t.Fatalf("expected %d results, got %d", topK, len(results))
	}

	expected := []string{"d", "a", "b"}
	for i := range expected {
		if results[i].Record.ID != expected[i] {
			t.Fatalf("unexpected result order at %d: expected %q got %q", i, expected[i], results[i].Record.ID)
		}
	}
}

func TestEnsureIndexesMSSQL(t *testing.T) {
	collection := &MSSQLCollection{}

	if err := collection.EnsureIndexes(context.Background(), vectordata.IndexOptions{}); err != nil {
		t.Fatalf("expected empty options to succeed, got %v", err)
	}

	err := collection.EnsureIndexes(context.Background(), vectordata.IndexOptions{
		Vector: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW},
	})
	if err == nil {
		t.Fatal("expected error when index options are provided")
	}
	if !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("expected ErrSchemaMismatch, got %v", err)
	}
}

func TestBuildSearchSQLPlan(t *testing.T) {
	threshold := 0.55
	collection := &MSSQLCollection{
		store:     &MSSQLVectorStore{opts: StoreOptions{Schema: "dbo"}},
		name:      "docs",
		dimension: 2,
		metric:    vectordata.DistanceCosine,
	}

	plan, err := collection.buildSearchSQLPlan([]float32{1, 0}, 3, vectordata.SearchOptions{
		Threshold: &threshold,
		Projection: &vectordata.Projection{
			IncludeMetadata: true,
			IncludeContent:  true,
		},
	})
	if err != nil {
		t.Fatalf("buildSearchSQLPlan: %v", err)
	}
	if !strings.Contains(plan.query, "FETCH NEXT @p4 ROWS ONLY") {
		t.Fatalf("expected limit placeholder in query, got: %s", plan.query)
	}
	if !reflect.DeepEqual(plan.args, []any{"[1,0]", 2, threshold, 3}) {
		t.Fatalf("unexpected args: %#v", plan.args)
	}
}

func TestBuildSearchSQLPlanRejectsUnsupportedFilterPushdown(t *testing.T) {
	collection := &MSSQLCollection{
		store:     &MSSQLVectorStore{opts: StoreOptions{Schema: "dbo"}},
		name:      "docs",
		dimension: 2,
		metric:    vectordata.DistanceCosine,
	}

	_, err := collection.buildSearchSQLPlan([]float32{1, 0}, 3, vectordata.SearchOptions{
		Filter: vectordata.Eq(vectordata.Column("id"), 123),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errFilterPushdownUnsupported) {
		t.Fatalf("expected errFilterPushdownUnsupported, got %v", err)
	}
}
