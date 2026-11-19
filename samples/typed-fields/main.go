// typed-fields demonstrates generated, compile-checked model property references.
package main

//go:generate go run ../../cmd/vectordata-gen -type Article

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/samples/internal/demo"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Label string
type Labels []Label

type Article struct {
	ID      string         `vectordata:"key"`
	Title   string         `vectordata:"data,name=stored_title"`
	Rating  *int           `vectordata:"data"`
	Enabled bool           `vectordata:"data"`
	Tags    Labels         `vectordata:"data"`
	Details map[string]any `vectordata:"data"`
	Vector  []float32      `vectordata:"vector,dimensions=2,metric=l2"`
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("typed-fields", flag.ContinueOnError)
	path := flags.String("sqlite", "", "SQLite file (default: memory)")
	dsn := flags.String("dsn", "", "PostgreSQL DSN, mutually exclusive with -sqlite")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, close, err := demo.Open(ctx, *dsn, *path, nil)
	if err != nil {
		return err
	}
	defer close()
	collection, err := vectordata.EnsureTypedModelCollection[string, Article](ctx, store, "typed_fields_demo", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	rating := 5
	if _, err := collection.Upsert(ctx, []Article{
		{ID: "go", Title: "Go guide", Rating: &rating, Enabled: true, Tags: Labels{"go", "guide"}, Details: map[string]any{"level": "beginner"}, Vector: []float32{1, 0}},
		{ID: "draft", Title: "Draft", Enabled: false, Tags: Labels{"go"}, Vector: []float32{0, 1}},
	}); err != nil {
		return err
	}
	filter := vectordata.And(
		ArticleFields.Enabled.Eq(true), ArticleFields.Rating.Gte(4),
		ArticleFields.Tags.ContainsAll("go", "guide"),
		// JSON paths remain dynamic and use the existing AST.
		vectordata.Eq(ArticleFields.Details.Path("level"), "beginner"),
	)
	rows, err := collection.Query(ctx, filter, 10, vectordata.QueryOptions{OrderBy: []vectordata.SortField{ArticleFields.Rating.Desc(), ArticleFields.ID.Asc()}})
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].ID != "go" {
		return fmt.Errorf("unexpected typed query results: %v", rows)
	}
	fmt.Fprintf(out, "Matches: %d\n%s: %s\n", len(rows), rows[0].ID, rows[0].Title)
	missing, err := collection.Count(ctx, ArticleFields.Rating.IsNull())
	if err != nil {
		return err
	}
	if missing != 1 {
		return fmt.Errorf("unexpected null count: %d", missing)
	}
	fmt.Fprintf(out, "Unrated: %d\n", missing)
	hits, err := collection.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{Filter: filter})
	if err != nil {
		return err
	}
	if len(hits) != 1 || hits[0].Item.ID != "go" {
		return fmt.Errorf("unexpected typed search results")
	}
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
