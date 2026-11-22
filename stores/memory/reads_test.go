package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func memoryReadsFixture(t testing.TB, size int) vectordata.ModelCollection {
	t.Helper()
	ctx := context.Background()
	d := vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64},
		Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}},
		Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2}},
	}
	c, err := New().EnsureModelCollection(ctx, "reads", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]vectordata.ModelRecord, size)
	for i := range rows {
		rows[i] = vectordata.ModelRecord{Key: int64(i + 1), Data: map[string]any{"Body": "page"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
	}
	if _, err = c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBatchReadNormalizesDeduplicatesAndOwnsResults(t *testing.T) {
	ctx := context.Background()
	c := memoryReadsFixture(t, 3)
	keys := []any{int64(3), int32(1), int64(1), int64(4)}
	got, err := c.GetMany(ctx, keys, vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || len(got) != 2 || got[0].Key != int64(1) || got[1].Key != int64(3) {
		t.Fatalf("batch: %v, %v", got, err)
	}
	got[0].Data["Body"] = "changed"
	got[0].Vectors["Vector"][0] = 9
	r, err := c.Get(ctx, int64(1), vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || r.Data["Body"] != "page" || r.Vectors["Vector"][0] != 1 {
		t.Fatalf("stored row: %v, %v", r, err)
	}
}

func BenchmarkMemoryPointBatchAndCount(b *testing.B) {
	ctx := context.Background()
	for _, size := range []int{100, 10000} {
		c := memoryReadsFixture(b, size)
		b.Run(fmt.Sprintf("GetManyOne/%d", size), func(b *testing.B) {
			keys := []any{int64(1)}
			b.ReportAllocs()
			for b.Loop() {
				rows, err := c.GetMany(ctx, keys, vectordata.RetrievalOptions{})
				if err != nil || len(rows) != 1 {
					b.Fatalf("rows=%d err=%v", len(rows), err)
				}
			}
		})
		b.Run(fmt.Sprintf("CountAll/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				n, err := c.Count(ctx, nil)
				if err != nil || n != int64(size) {
					b.Fatalf("count=%d err=%v", n, err)
				}
			}
		})
	}
}

func TestQueryPageOwnershipAndLargeOffset(t *testing.T) {
	c := memoryReadsFixture(t, 128)
	ctx := t.Context()
	opts := vectordata.QueryOptions{Skip: 1, IncludeVectors: true, OrderBy: []vectordata.SortField{{Property: "Body"}, {Property: "ID", Descending: true}}}
	rows, err := c.Query(ctx, nil, 1, opts)
	if err != nil || len(rows) != 1 || cap(rows) != 1 || rows[0].Key != int64(127) {
		t.Fatalf("page: %v, %v", rows, err)
	}
	rows[0].Vectors["Vector"][0] = 8
	row, err := c.Get(ctx, int64(127), vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || row.Vectors["Vector"][0] != 1 {
		t.Fatalf("stored: %v, %v", row, err)
	}
	opts.Skip = math.MaxInt
	rows, err = c.Query(ctx, nil, math.MaxInt, opts)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("large offset: %v, %v", rows, err)
	}
}

type cancelQueryOperand struct {
	calls  int
	cancel context.CancelFunc
}

func (v *cancelQueryOperand) MarshalJSON() ([]byte, error) {
	v.calls++
	if v.calls == 2 {
		v.cancel()
	}
	return []byte(`"ok"`), nil
}

func TestQueryCancellationWhileEvaluatingFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Tag", Type: vectordata.TypeJSON}}}
	c, err := New().EnsureModelCollection(ctx, "rows", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Tag": "ok"}}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(ctx, vectordata.Eq(vectordata.Column("Tag"), &cancelQueryOperand{cancel: cancel}), 1, vectordata.QueryOptions{})
	if !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
