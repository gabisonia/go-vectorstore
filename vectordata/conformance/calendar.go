package conformance

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type calendarText string
type calendarDates []vectordata.DateOnly
type calendarModel struct {
	ID          string               `vectordata:"key"`
	Date        vectordata.DateOnly  `vectordata:"data,type=date_only,index"`
	Clock       *vectordata.TimeOnly `vectordata:"data,type=time_only,index"`
	Dates       calendarDates        `vectordata:"data,type=date_only_array,optional,index"`
	Clocks      []calendarText       `vectordata:"data,type=time_only_array,optional,index"`
	Legacy      string               `vectordata:"data"`
	LegacyDates []string             `vectordata:"data"`
	Timestamp   time.Time            `vectordata:"data"`
}

func runCalendarData(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	for _, tt := range []struct {
		kind                            vectordata.DataType
		low, input, want, high, invalid string
	}{
		{vectordata.TypeDateOnly, "0001-01-01", "2024-02-29", "2024-02-29", "9999-12-31", "2026-02-29"},
		{vectordata.TypeTimeOnly, "00:00:00.000000", "09:02:03.123456789", "09:02:03.123456", "23:59:59.999999", "24:00:00"},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			if !slices.Contains(caps.DataTypes, tt.kind) {
				t.Skip("calendar type not advertised")
			}
			ctx := context.Background()
			c, err := factory(t).EnsureModelCollection(ctx, "calendar", vectordata.CollectionDefinition{
				Key:     vectordata.KeyProperty{Name: "ID"},
				Data:    []vectordata.DataProperty{{Name: "Value", StorageName: `calendar";x`, Type: tt.kind, Optional: true, IsIndexed: true}, {Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}},
				Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}},
			}, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			record := func(key string, value any) vectordata.ModelRecord {
				return vectordata.ModelRecord{Key: key, Data: map[string]any{"Value": value, "Body": "alpha"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
			}
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{record("a", tt.low), record("b", calendarText(tt.input)), record("c", tt.high), record("d", nil)}); err != nil {
				t.Fatal(err)
			}
			row, err := c.Get(ctx, "b", vectordata.RetrievalOptions{})
			if err != nil || row.Data["Value"] != tt.want {
				t.Fatalf("calendar round trip: %v, %v", row, err)
			}
			field := vectordata.Column("Value")
			for _, test := range []struct {
				filter vectordata.Filter
				keys   []string
			}{
				{vectordata.Eq(field, calendarText(tt.input)), []string{"b"}},
				{vectordata.In(field, calendarText(tt.input), nil), []string{"b"}},
				{vectordata.Not(vectordata.In(field, tt.input, nil)), nil},
				{vectordata.Gt(field, tt.input), []string{"c"}},
				{vectordata.Gte(field, tt.input), []string{"b", "c"}},
				{vectordata.Lt(field, tt.input), []string{"a"}},
				{vectordata.Lte(field, tt.input), []string{"a", "b"}},
				{vectordata.Eq(field, nil), []string{"d"}},
				{vectordata.Exists(field), []string{"a", "b", "c"}},
				{vectordata.Not(vectordata.Eq(field, tt.input)), []string{"a", "c"}},
			} {
				rows, err := c.Query(ctx, test.filter, 10, vectordata.QueryOptions{})
				if err != nil {
					t.Fatal(err)
				}
				keys := make([]string, len(rows))
				for i, row := range rows {
					keys[i] = row.Key.(string)
				}
				slices.Sort(keys)
				if !slices.Equal(keys, test.keys) {
					t.Fatalf("%#v: keys=%v want=%v", test.filter, keys, test.keys)
				}
				if n, err := c.Count(ctx, test.filter); err != nil || n != int64(len(test.keys)) {
					t.Fatalf("calendar count: %d, %v", n, err)
				}
			}
			for _, desc := range []bool{false, true} {
				rows, err := c.Query(ctx, nil, 4, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Value", Descending: desc}}})
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"a", "b", "c", "d"}
				if desc {
					want = []string{"c", "b", "a", "d"}
				}
				for i, row := range rows {
					if row.Key != want[i] {
						t.Fatalf("calendar ordering: %v", rows)
					}
				}
			}
			for _, f := range []vectordata.Filter{vectordata.Eq(field, tt.invalid), vectordata.Contains(field, tt.input), vectordata.Gt(field, nil), vectordata.Eq(field, time.Now())} {
				if _, err := c.Query(ctx, f, 1, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrInvalidFilter) {
					t.Fatalf("invalid calendar filter: %v", err)
				}
			}
			filter := vectordata.Eq(field, tt.input)
			if hits, err := c.Search(ctx, []float32{1, 0}, 5, vectordata.ModelSearchOptions{Filter: filter}); err != nil || len(hits) != 1 || hits[0].Record.Key != "b" {
				t.Fatalf("calendar vector filter: %v, %v", hits, err)
			}
			if caps.HybridSearch {
				if hits, err := c.(vectordata.HybridSearcher).HybridSearch(ctx, []float32{1, 0}, "alpha", 5, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{Filter: filter}}); err != nil || len(hits) != 1 || hits[0].Record.Key != "b" {
					t.Fatalf("calendar hybrid filter: %v, %v", hits, err)
				}
			}
			if _, err := c.Upsert(ctx, []vectordata.ModelRecord{record("new", tt.low), record("b", tt.invalid)}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("invalid calendar write: %v", err)
			}
			if n, err := c.Count(ctx, nil); err != nil || n != 4 {
				t.Fatalf("partial calendar write: %d, %v", n, err)
			}
		})
	}
	t.Run("typed and legacy", func(t *testing.T) {
		for _, kind := range []vectordata.DataType{vectordata.TypeDateOnly, vectordata.TypeTimeOnly, vectordata.TypeDateOnlyArray, vectordata.TypeTimeOnlyArray} {
			if !slices.Contains(caps.DataTypes, kind) {
				t.Skip("calendar types not advertised")
			}
		}
		ctx := context.Background()
		c, err := vectordata.EnsureTypedModelCollection[string, calendarModel](ctx, factory(t), "typed_calendar", nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		clock := vectordata.TimeOnly("09:02:03.123456789")
		input := calendarModel{ID: "a", Date: "2024-02-29", Clock: &clock, Dates: calendarDates{"0001-01-01", "9999-12-31"}, Clocks: []calendarText{"00:00:00", "23:59:59.999999999"}, Legacy: "2026-02-29", LegacyDates: []string{"not a date"}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)}
		if _, err := c.Insert(ctx, []calendarModel{input, {ID: "null", Date: "0001-01-01"}, {ID: "empty", Date: "9999-12-31", Dates: calendarDates{}, Clocks: []calendarText{}}}); err != nil {
			t.Fatal(err)
		}
		canonical := vectordata.TimeOnly("09:02:03.123456")
		want := input
		want.Clock = &canonical
		want.Clocks = []calendarText{"00:00:00.000000", "23:59:59.999999"}
		got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
		if err == nil && got.Timestamp.Equal(want.Timestamp) {
			// Scalar timestamps retain the provider's existing location behavior.
			got.Timestamp = want.Timestamp
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("typed calendar/legacy round trip: %+v, %v", got, err)
		}
		got, err = c.Get(ctx, "null", vectordata.RetrievalOptions{})
		if err != nil || got.Clock != nil || got.Dates != nil || got.Clocks != nil {
			t.Fatalf("typed calendar NULLs: %+v, %v", got, err)
		}
		got, err = c.Get(ctx, "empty", vectordata.RetrievalOptions{})
		if err != nil || got.Dates == nil || got.Clocks == nil || len(got.Dates)+len(got.Clocks) != 0 {
			t.Fatalf("typed empty arrays: %+v, %v", got, err)
		}
	})
}
