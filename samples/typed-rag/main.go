// typed-rag retrieves typed documents and passes cited sources to generation.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/samples/internal/demo"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("typed-rag", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "PostgreSQL DSN (default: memory)")
	path := flags.String("sqlite", "", "SQLite file, mutually exclusive with -dsn")
	live := flags.Bool("openai", false, "Use OpenAI embeddings and chat")
	dims := flags.Int("dimensions", 0, "Embedding dimensions (default: 3 fixture, 1536 OpenAI)")
	name := flags.String("collection", "typed_rag_demo", "Dedicated sample collection")
	question := flags.String("q", "How can we reduce cloud costs?", "Question")
	top := flags.Int("top", 2, "Maximum sources")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *top <= 0 {
		return fmt.Errorf("top must be positive")
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
	var client *demo.OpenAI
	if *live {
		client, err = demo.OpenAIFromEnv(dimensions)
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
	articles, err := vectordata.EnsureTypedModelCollection[string, demo.Article](ctx, store, *name, &definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if _, err := articles.Upsert(ctx, demo.Articles()); err != nil {
		return err
	}
	// Thresholds are application/model specific. This demo retrieves the nearest
	// sources without claiming that distance alone establishes relevance.
	hits, err := articles.SearchText(ctx, *question, *top, vectordata.ModelSearchOptions{})
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Fprintln(out, "No sources found; generation skipped.")
		return nil
	}
	var sources strings.Builder
	for _, hit := range hits {
		fmt.Fprintf(&sources, "[%s] %s\n%s\n", hit.Item.ID, hit.Item.Title, hit.Item.Body)
	}
	fmt.Fprintf(out, "Sources:\n%s", sources.String())
	if client == nil {
		fmt.Fprintln(out, "Fixture mode: retrieved sources only; use -openai for generation.")
		return nil
	}
	answer, err := client.Answer(ctx, *question, sources.String())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Answer:\n%s\n", answer)
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
