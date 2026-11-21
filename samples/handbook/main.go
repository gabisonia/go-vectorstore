// Command handbook indexes short Markdown pages and prints the closest passage.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/samples/internal/demo"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type passage struct {
	Source string    `vectordata:"key"`
	Body   string    `vectordata:"data"`
	Vector []float32 `vectordata:"vector,dimensions=3,metric=cosine,source=Body"`
}

func load(dir string) ([]passage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var pages []passage
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		body := strings.TrimSpace(string(raw))
		if body == "" {
			return nil, fmt.Errorf("empty page %s", entry.Name())
		}
		pages = append(pages, passage{Source: entry.Name(), Body: body})
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no Markdown pages in %s", dir)
	}
	return pages, nil
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("handbook", flag.ContinueOnError)
	db := flags.String("sqlite", "", "SQLite file (default handbook.db)")
	dir := flags.String("pages", "samples/handbook/pages", "directory of Markdown passages")
	if err := flags.Parse(args); err != nil {
		return err
	}
	action := flags.Arg(0)
	if !(action == "index" && flags.NArg() == 1 || action == "query" && flags.NArg() == 2 && strings.TrimSpace(flags.Arg(1)) != "") {
		return fmt.Errorf("usage: handbook [flags] index | query 'search text'")
	}
	dsn := os.Getenv("HANDBOOK_POSTGRES_DSN")
	if dsn == "" && *db == "" {
		*db = "handbook.db"
	}
	store, closeStore, err := demo.Open(ctx, dsn, *db, demo.DeterministicEmbedder())
	if err != nil {
		return err
	}
	defer closeStore()
	const name = "handbook_passages"
	opts := vectordata.ModelCollectionOptions{}
	if action == "index" {
		pages, err := load(*dir)
		if err != nil {
			return err
		}
		collection, err := vectordata.EnsureTypedModelCollection[string, passage](ctx, store, name, nil, opts)
		if err != nil {
			return err
		}
		if _, err = collection.Upsert(ctx, pages); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Indexed %d pages\n", len(pages))
		return err
	}
	collection, err := vectordata.GetTypedModelCollection[string, passage](store, name, nil, opts)
	if err != nil {
		return err
	}
	hits, err := collection.SearchText(ctx, flags.Arg(1), 1, vectordata.ModelSearchOptions{})
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		return fmt.Errorf("no indexed passages")
	}
	_, err = fmt.Fprintf(out, "%s\n%s\n", hits[0].Item.Source, hits[0].Item.Body)
	return err
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
