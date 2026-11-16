// recipes contains focused model API workflows. See README.md for commands.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/samples/internal/demo"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Note struct {
	ID   string `vectordata:"key"`
	Text string `vectordata:"data"`
}
type Named struct {
	ID          string    `vectordata:"key"`
	TitleVector []float32 `vectordata:"vector,dimensions=3"`
	BodyVector  []float32 `vectordata:"vector,dimensions=3"`
}
type SquaredPoint struct {
	ID     string    `vectordata:"key"`
	Vector []float32 `vectordata:"vector,dimensions=2,metric=l2_squared"`
}

type KeyedNote struct {
	ID   int64 `vectordata:"key"`
	Rank int64 `vectordata:"data"`
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("recipes", flag.ContinueOnError)
	recipe := flags.String("recipe", "named-vectors", "named-vectors, key-filters, squared-l2, iterators, embedded-iterators, metadata, capabilities, hybrid, migration")
	dsn := flags.String("dsn", "", "PostgreSQL DSN (required for hybrid/migration)")
	path := flags.String("sqlite", "", "SQLite file (default: memory)")
	prefix := flags.String("prefix", "recipe_demo", "Dedicated collection prefix")
	if err := flags.Parse(args); err != nil {
		return err
	}
	switch *recipe {
	case "named-vectors", "key-filters", "squared-l2", "iterators", "embedded-iterators", "metadata", "capabilities", "hybrid", "migration":
	default:
		return fmt.Errorf("unknown recipe %q", *recipe)
	}
	store, close, err := demo.Open(ctx, *dsn, *path, demo.DeterministicEmbedder())
	if err != nil {
		return err
	}
	defer close()
	return execute(ctx, store, *recipe, *prefix, out)
}
func execute(ctx context.Context, store vectordata.ModelStore, recipe, prefix string, out io.Writer) error {
	if recipe == "capabilities" {
		provider, ok := store.(vectordata.CapabilityProvider)
		if !ok {
			return fmt.Errorf("capability metadata: %w", vectordata.ErrUnsupported)
		}
		c := provider.Capabilities()
		fmt.Fprintf(out, "Backend: %s\nHybrid search: %t\nMigration planning: %t\nPhysical vector index combinations: %d\n", c.Backend, c.HybridSearch, c.MigrationPlanning, len(c.VectorIndexCapabilities))
		return nil
	}
	if recipe == "hybrid" || recipe == "migration" {
		provider, ok := store.(vectordata.CapabilityProvider)
		if !ok {
			return fmt.Errorf("provider metadata required for this recipe: %w", vectordata.ErrUnsupported)
		}
		c := provider.Capabilities()
		if recipe == "hybrid" && !c.HybridSearch || recipe == "migration" && !c.MigrationPlanning {
			return fmt.Errorf("%s requires a capable provider; use -dsn: %w", recipe, vectordata.ErrUnsupported)
		}
	}
	name := prefix + "_" + recipe
	switch recipe {
	case "key-filters":
		c, err := vectordata.EnsureTypedModelCollection[int64, KeyedNote](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if _, err := c.Upsert(ctx, []KeyedNote{{ID: 2, Rank: 1}, {ID: 10, Rank: 0}, {ID: 99, Rank: 2}}); err != nil {
			return err
		}
		filter := vectordata.In(vectordata.Column("ID"), int64(10), int64(2), int64(2), int64(-1), nil)
		count, err := c.Count(ctx, filter)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Matched keys: %d\n", count)
		for note, err := range c.QueryIter(ctx, filter, 1, vectordata.QueryOptions{Skip: 1, OrderBy: []vectordata.SortField{{Property: "Rank"}}}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Paged key: %d\n", note.ID)
		}
		count, err = c.Count(ctx, vectordata.Eq(vectordata.Column("ID"), int64(10)))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Exact-key count: %d\n", count)
	case "metadata":
		c, err := vectordata.GetTypedModelCollection[string, Named](store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		m := c.CollectionMetadata()
		fmt.Fprintf(out, "System: %s\nDatabase: %s\nNamespace: %s\nCollection: %s\n", m.SystemName, m.DatabaseName, m.Namespace, m.CollectionName)
	case "squared-l2":
		c, err := vectordata.EnsureTypedModelCollection[string, SquaredPoint](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if _, err := c.Upsert(ctx, []SquaredPoint{{ID: "origin", Vector: []float32{0, 0}}, {ID: "near", Vector: []float32{3, 4}}}); err != nil {
			return err
		}
		for hit, err := range c.SearchIter(ctx, []float32{0, 0}, 2, vectordata.ModelSearchOptions{}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: distance %.0f score %.6f\n", hit.Item.ID, hit.Distance, hit.Score)
		}
		limit := 5.0
		hits, err := c.Search(ctx, []float32{0, 0}, 2, vectordata.ModelSearchOptions{DistanceThreshold: &limit})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Squared-distance threshold 5: %d match\n", len(hits))
	case "named-vectors":
		c, err := vectordata.EnsureTypedModelCollection[string, Named](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if _, err := c.Upsert(ctx, []Named{{ID: "guide", TitleVector: []float32{1, 0, 0}, BodyVector: []float32{0, 1, 0}}}); err != nil {
			return err
		}
		hits, err := c.Search(ctx, []float32{0, 1, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: "BodyVector", IncludeVectors: true})
		if err != nil {
			return err
		}
		for _, hit := range hits {
			fmt.Fprintf(out, "%s: BodyVector distance %.3f; loaded dimensions %d\n", hit.Item.ID, hit.Distance, len(hit.Item.BodyVector))
		}
	case "iterators":
		c, err := vectordata.EnsureTypedModelCollection[string, Note](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if _, err := c.Upsert(ctx, []Note{{ID: "one", Text: "first"}, {ID: "two", Text: "second"}}); err != nil {
			return err
		}
		for collectionName, err := range vectordata.ListCollectionNamesIter(ctx, store) {
			if err != nil {
				return err
			}
			if collectionName == name {
				fmt.Fprintf(out, "Collection: %s\n", collectionName)
				break
			}
		}
		for note, err := range c.GetManyIter(ctx, []string{"one", "missing"}, vectordata.RetrievalOptions{}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Batch: %s\n", note.ID)
			break
		}
		// Leaving the range closes a PostgreSQL cursor. Memory/SQLite materialize
		// before yielding. Check the iterator's error value on every iteration.
		for note, err := range c.QueryIter(ctx, nil, 2, vectordata.QueryOptions{}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "First: %s\n", note.ID)
			break
		}
		canceled, cancel := context.WithCancel(ctx)
		defer cancel()
		observed := false
		for _, err := range c.QueryIter(canceled, nil, 2, vectordata.QueryOptions{}) {
			// A driver may have buffered rows before cancellation. Check the
			// consumer's context too, before processing another item.
			if err == nil {
				err = canceled.Err()
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					return err
				}
				observed = true
				break
			}
			cancel()
		}
		if !observed {
			return fmt.Errorf("iterator did not report cancellation")
		}
		count, err := c.Count(ctx, nil)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Cancellation observed; count after iterator: %d\n", count)
	case "embedded-iterators":
		c, err := vectordata.EnsureTypedModelCollection[string, demo.Article](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if _, err := c.Upsert(ctx, demo.Articles()); err != nil {
			return err
		}
		for hit, err := range c.SearchTextIter(ctx, "reduce cloud costs", 2, vectordata.ModelSearchOptions{}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Search: %s\n", hit.Item.ID)
			break
		}
		for hit, err := range c.SearchInputIter(ctx, "reduce cloud costs", 2, vectordata.ModelSearchOptions{}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Input: %s\n", hit.Item.ID)
			break
		}
	case "hybrid":
		if !store.(vectordata.CapabilityProvider).Capabilities().HybridKeywordTerms {
			return fmt.Errorf("keyword-list hybrid search: %w", vectordata.ErrUnsupported)
		}
		c, err := vectordata.EnsureTypedModelCollection[string, demo.Article](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if _, err := c.Upsert(ctx, demo.Articles()); err != nil {
			return err
		}
		for hit, err := range c.HybridSearchTextIter(ctx, "reduce cloud costs", "caching", 2, vectordata.HybridSearchOptions{TextProperty: "Body", CandidateLimit: 10}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %s score %.6f\n", hit.Item.ID, hit.ScoreKind, hit.Score)
		}
		// Entries match with OR; words within "cloud costs" match with AND.
		for hit, err := range c.HybridSearchTextIter(ctx, "reduce cloud costs", "", 2, vectordata.HybridSearchOptions{TextProperty: "Body", CandidateLimit: 10, KeywordTerms: []string{"cloud costs", "caching"}}) {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Keyword list: %s: %s score %.6f\n", hit.Item.ID, hit.ScoreKind, hit.Score)
		}
	case "migration":
		planner, ok := store.(vectordata.MigrationPlanner)
		if !ok {
			return vectordata.ErrUnsupported
		}
		// Create/reuse the recipe's baseline fixture, then inspect an addition.
		// PlanMigration itself is read-only. No ApplyMigration call is made here.
		c, err := vectordata.EnsureTypedModelCollection[string, Note](ctx, store, name, nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		desired := c.Definition()
		desired.Data = append(desired.Data, vectordata.DataProperty{Name: "Category", Type: vectordata.TypeString, Optional: true})
		plan, err := planner.PlanMigration(ctx, name, desired)
		if err != nil {
			return err
		}
		for _, issue := range plan.Issues {
			fmt.Fprintf(out, "Issue %s: %s\n", issue.Kind, issue.Description)
		}
		for _, step := range plan.Steps {
			fmt.Fprintf(out, "%s: manual=%t dataLoss=%t backfill=%t reembed=%t\nSQL: %s\nArgs: %#v\n", step.Kind, step.Manual, step.DataLoss, step.RequiresBackfill, step.RequiresReembedding, step.SQL, step.Args)
		}
		if final := plan.Finalization; final != nil {
			fmt.Fprintf(out, "Finalization (not executed): %s\nArgs: %#v\n", final.SQL, final.Args)
		}
		fmt.Fprintln(out, "Preview only; no migration applied.")
	}
	return nil
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
