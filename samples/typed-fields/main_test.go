package main

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/samples/internal/demo"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestQueries(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var args []string
			if backend == "sqlite" {
				args = []string{"-sqlite", filepath.Join(t.TempDir(), "fields.db")}
			}
			for range 2 {
				var out bytes.Buffer
				if err := run(context.Background(), args, &out); err != nil {
					t.Fatal(err)
				}
				if out.String() != "Matches: 1\ngo: Go guide\nUnrated: 1\n" {
					t.Fatalf("unexpected output: %s", &out)
				}
			}
		})
	}
}

func TestGeneratedFilterSemantics(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var path string
			if backend == "sqlite" {
				path = filepath.Join(t.TempDir(), "operators.db")
			}
			store, close, err := demo.Open(t.Context(), "", path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer close()
			collection, err := vectordata.EnsureTypedModelCollection[string, Article](t.Context(), store, "operators", nil, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			five, two := 5, 2
			_, err = collection.Upsert(t.Context(), []Article{
				{ID: "a", Title: "Alpha", Rating: &five, Enabled: true, Tags: Labels{"go", "guide"}, Details: map[string]any{"level": "beginner"}, Vector: []float32{1, 0}},
				{ID: "b", Title: "Beta", Tags: Labels{"go"}, Vector: []float32{0, 1}},
				{ID: "c", Title: "Gamma", Rating: &two, Enabled: true, Tags: Labels{"other"}, Details: map[string]any{}, Vector: []float32{1, 1}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name   string
				filter vectordata.Filter
				ids    []string
			}{
				{"typed in", ArticleFields.Title.In("Alpha", "Gamma"), []string{"a", "c"}},
				{"not equal", ArticleFields.Title.Ne("Beta"), []string{"a", "c"}},
				{"sql null", ArticleFields.Rating.IsNull(), []string{"b"}},
				{"not sql null", ArticleFields.Rating.IsNotNull(), []string{"a", "c"}},
				{"three valued not", vectordata.Not(ArticleFields.Rating.Gte(4)), []string{"c"}},
				{"membership any", ArticleFields.Tags.ContainsAny("guide", "other"), []string{"a", "c"}},
				{"membership", ArticleFields.Tags.Contains("guide"), []string{"a"}},
				{"array equality", ArticleFields.Tags.Eq(Labels{"go"}), []string{"b"}},
				{"array in", ArticleFields.Tags.In(Labels{"go"}, Labels{"other"}), []string{"b", "c"}},
				{"json null", ArticleFields.Details.IsJSONNull(), []string{"b"}},
				{"json null differs from sql null", ArticleFields.Details.IsNull(), nil},
				{"or", vectordata.Or(ArticleFields.Rating.Lt(3), ArticleFields.Enabled.Eq(false)), []string{"b", "c"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rows, err := collection.Query(t.Context(), tc.filter, 10, vectordata.QueryOptions{OrderBy: []vectordata.SortField{ArticleFields.ID.Asc()}})
					if err != nil {
						t.Fatal(err)
					}
					var ids []string
					for _, row := range rows {
						ids = append(ids, row.ID)
					}
					if !reflect.DeepEqual(ids, tc.ids) {
						t.Fatalf("got %v; want %v", ids, tc.ids)
					}
				})
			}
			rows, err := collection.Query(t.Context(), ArticleFields.Rating.IsNotNull(), 2, vectordata.QueryOptions{OrderBy: []vectordata.SortField{ArticleFields.Rating.Desc()}})
			if err != nil || len(rows) != 2 || rows[0].ID != "a" || rows[1].ID != "c" {
				t.Fatalf("typed descending order: %v, %v", rows, err)
			}
			if _, err := collection.Count(t.Context(), ArticleFields.Title.In()); err == nil {
				t.Fatal("empty In must retain runtime validation")
			}
			if ArticleFields.Title.Name() != "Title" {
				t.Fatal("reference used storage alias instead of logical field name")
			}
		})
	}
}
