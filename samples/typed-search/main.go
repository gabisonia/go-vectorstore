// typed-search demonstrates automatic embeddings and typed filtered retrieval.
package main

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

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("typed-search", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "PostgreSQL DSN (default: memory)")
	path := flags.String("sqlite", "", "SQLite file, mutually exclusive with -dsn")
	live := flags.Bool("openai", false, "Use OpenAI instead of the deterministic fixture")
	dims := flags.Int("dimensions", 0, "Embedding dimensions (default: 3 fixture, 1536 OpenAI)")
	name := flags.String("collection", "typed_search_demo", "Dedicated sample collection")
	query := flags.String("q", "reduce cloud costs", "Search text")
	category := flags.String("category", "", "Optional category filter")
	if err := flags.Parse(args); err != nil {
		return err
	}
	dimensions := *dims
	if dimensions == 0 {
		dimensions = 3
		if *live {
			dimensions = 1536
		}
	}
	if !*live && dimensions != 3 {
		return fmt.Errorf("the deterministic fixture requires dimensions=3")
	}
	definition, err := demo.Definition(dimensions)
	if err != nil {
		return err
	}
	embedder := demo.DeterministicEmbedder()
	if *live {
		client, err := demo.OpenAIFromEnv(dimensions)
		if err != nil {
			return err
		}
		embedder = client
	}
	store, close, err := demo.Open(ctx, *dsn, *path, embedder)
	if err != nil {
		return err
	}
	defer close()
	// Typed schemas map Go fields to native model properties. Source=Body asks the
	// store to embed missing vectors on writes, using batches of at most two texts.
	articles, err := vectordata.EnsureTypedModelCollection[string, demo.Article](ctx, store, *name, &definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if _, err := articles.Upsert(ctx, demo.Articles()); err != nil {
		return err
	}
	var filter vectordata.Filter
	if *category != "" {
		filter = vectordata.Eq(vectordata.Column("Category"), *category)
	}
	hits, err := articles.SearchText(ctx, *query, 3, vectordata.ModelSearchOptions{Filter: filter})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Matches: %d\n", len(hits))
	for _, hit := range hits {
		fmt.Fprintf(out, "%s: %s (score %.3f, distance %.3f)\n", hit.Item.ID, hit.Item.Title, hit.Score, hit.Distance)
	}
	return nil
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
