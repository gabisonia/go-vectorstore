package exactsearch_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func snapshotFixture(t testing.TB, n int) (vectordata.CollectionDefinition, []vectordata.ModelRecord) {
	t.Helper()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceL2, Optional: true}}}
	rows := make([]vectordata.ModelRecord, n)
	for i := range rows {
		rows[i] = vectordata.ModelRecord{Key: int64(n - i), Data: map[string]any{"Group": fmt.Sprint(i % 2)}}
		if i%5 != 0 {
			rows[i].Vectors = map[string][]float32{"V": {float32(i % 3), 1}}
		}
	}
	prepared, err := vectordata.PrepareModelRecords(t.Context(), d, vectordata.ModelCollectionOptions{}, rows)
	if err != nil {
		t.Fatal(err)
	}
	return d, prepared
}

func rebuiltSnapshotSearch(ctx context.Context, d vectordata.CollectionDefinition, rows []vectordata.ModelRecord, q []float32, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	c, err := memory.New().EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	if _, err = c.Insert(ctx, rows); err != nil {
		return nil, err
	}
	return c.Search(ctx, q, 5, opts)
}

func TestDenseSnapshotMatchesRebuiltCollection(t *testing.T) {
	d, rows := snapshotFixture(t, 100)
	q := []float32{1, 1}
	maxDistance := 1.0
	opts := vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("Group"), "1"), VectorProperty: "V", Skip: 2, IncludeVectors: true, DistanceThreshold: &maxDistance}
	want, err := rebuiltSnapshotSearch(t.Context(), d, rows, q, opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := exactsearch.DenseSnapshot(t.Context(), d, d.Vectors[0], q, 5, opts, rows)
	if err != nil || len(got) != 5 || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v err=%v", got, want, err)
	}
	got[0].Record.Vectors["V"][0] = 99
	for _, row := range rows {
		if row.Key == got[0].Record.Key && row.Vectors["V"][0] == 99 {
			t.Fatal("returned vectors alias snapshot")
		}
	}
}

func TestDenseSnapshotRejectsDuplicateNormalizedUUID(t *testing.T) {
	d, rows := snapshotFixture(t, 2)
	d.Key.Type = vectordata.KeyUUID
	for i := range rows {
		rows[i].Key = "00112233-4455-6677-8899-aabbccddeeff"
	}
	got, err := exactsearch.DenseSnapshot(t.Context(), d, d.Vectors[0], []float32{1, 1}, 1, vectordata.ModelSearchOptions{}, rows)
	if !errors.Is(err, vectordata.ErrInvalidRecord) || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func BenchmarkDenseSnapshotRanking(b *testing.B) {
	d, rows := snapshotFixture(b, 1000)
	q := []float32{1, 1}
	opts := vectordata.ModelSearchOptions{VectorProperty: "V"}
	for _, name := range []string{"rebuild", "direct"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var got []vectordata.ModelSearchResult
				var err error
				if name == "direct" {
					got, err = exactsearch.DenseSnapshot(b.Context(), d, d.Vectors[0], q, 5, opts, rows)
				} else {
					got, err = rebuiltSnapshotSearch(b.Context(), d, rows, q, opts)
				}
				if err != nil || len(got) != 5 {
					b.Fatalf("len=%d err=%v", len(got), err)
				}
			}
		})
	}
}
