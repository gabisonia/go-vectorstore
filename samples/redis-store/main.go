// redis-store demonstrates persistent Redis models and optional native indexes.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/redis"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Document struct {
	ID       string    `vectordata:"key"`
	Title    string    `vectordata:"data"`
	Category string    `vectordata:"data"`
	Vector   []float32 `vectordata:"vector,dimensions=3"`
}

func main() {
	addr := flag.String("addr", "localhost:6379", "Standalone Redis address")
	namespace := flag.String("namespace", "redis_demo", "Dedicated namespace for retained sample records")
	db := flag.Int("db", 0, "Redis database number")
	tlsEnabled := flag.Bool("tls", false, "Use verified TLS")
	storage := flag.String("storage", "", "New collection layout: snapshot (default), hash (native default), or json (RedisJSON)")
	native := flag.Bool("native", false, "Enable Redis Search indexes; use a separate namespace for the indexed sample model")
	index := flag.String("index", "hnsw", "Native vector index: hnsw or flat; use a separate namespace per model")
	fullText := flag.Bool("fulltext", false, "Index Title as Redis TEXT; requires -native and a separate namespace")
	hybrid := flag.Bool("hybrid", false, "Run keyword/vector RRF search; implies -fulltext and requires -native")
	keywordTerms := flag.Bool("keyword-terms", false, "Run OR groups of plain keyword terms; implies -hybrid")
	upgrade := flag.Bool("upgrade-hash", false, "Explicitly upgrade the sample collection to hash storage")
	upgradeJSON := flag.Bool("upgrade-json", false, "Explicitly convert the sample collection to RedisJSON storage")
	flag.Parse()
	var tlsConfig *tls.Config
	if *tlsEnabled {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	opts := redis.StoreOptions{Addr: *addr, Namespace: *namespace, DB: *db, Username: os.Getenv("REDIS_USERNAME"), Password: os.Getenv("REDIS_PASSWORD"), TLSConfig: tlsConfig, StorageMode: redis.StorageMode(*storage), NativeIndexes: *native}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, opts, *upgrade, vectordata.IndexMethod(*index), *fullText, *upgradeJSON, *hybrid, *keywordTerms); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, opts redis.StoreOptions, upgrade bool, method vectordata.IndexMethod, fullText, upgradeJSON, hybrid, keywordTerms bool) error {
	if keywordTerms {
		hybrid = true
	}
	if hybrid {
		if !opts.NativeIndexes {
			return fmt.Errorf("-hybrid requires -native")
		}
		fullText = true
	}
	if upgrade && upgradeJSON {
		return fmt.Errorf("choose one storage conversion: -upgrade-hash or -upgrade-json")
	}
	if method != vectordata.IndexMethodHNSW && method != vectordata.IndexMethodFlat {
		return fmt.Errorf("unknown native index %q: choose hnsw or flat", method)
	}
	if method == vectordata.IndexMethodFlat && !opts.NativeIndexes {
		return fmt.Errorf("-index flat requires -native")
	}
	if fullText && !opts.NativeIndexes {
		return fmt.Errorf("-fulltext requires -native")
	}
	s, err := redis.New(opts)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := vectordata.DefinitionFor[Document]()
	if err != nil {
		return err
	}
	if opts.NativeIndexes {
		for i := range d.Data {
			if d.Data[i].Name == "Title" {
				d.Data[i].IsFullTextIndexed = fullText
			}
			if d.Data[i].Name == "Category" {
				d.Data[i].IsIndexed = true
			}
		}
		d.Vectors[0].Index = &vectordata.VectorIndexOptions{Method: method}
	}
	if upgrade || upgradeJSON {
		base, err := s.EnsureModelCollection(ctx, "documents", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		if upgradeJSON {
			err = base.(*redis.Collection).MigrateToJSONStorage(ctx)
		} else {
			err = base.(*redis.Collection).MigrateToHashStorage(ctx)
		}
		if err != nil {
			return err
		}
	}
	c, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, s, "documents", &d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if _, err = c.Upsert(ctx, []Document{{ID: "architecture", Title: "System architecture", Category: "guide", Vector: []float32{1, 0, 0}}, {ID: "storage", Title: "Storage choices", Category: "guide", Vector: []float32{0.8, 0.2, 0}}, {ID: "release", Title: "Release notes", Category: "news", Vector: []float32{0, 1, 0}}}); err != nil {
		return err
	}
	filter := vectordata.Eq(vectordata.Column("Category"), "guide")
	n, err := c.Count(ctx, filter)
	if err != nil {
		return err
	}
	fmt.Printf("Guides: %d\n", n)
	hits, err := c.Search(ctx, []float32{1, 0, 0}, 1, vectordata.ModelSearchOptions{Filter: filter})
	if err != nil {
		return err
	}
	for _, hit := range hits {
		fmt.Printf("Nearest: %s (distance %.3f)\n", hit.Item.Title, hit.Distance)
	}
	if hybrid {
		keywords := "storage"
		options := vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{Filter: filter}}
		if keywordTerms {
			keywords = ""
			options.KeywordTerms = []string{"storage choices", "release notes"}
		}
		results, err := c.HybridSearch(ctx, []float32{1, 0, 0}, keywords, 1, options)
		if err != nil {
			return err
		}
		for _, hit := range results {
			fmt.Printf("Hybrid: %s (%s)\n", hit.Item.Title, hit.ScoreKind)
		}
	}
	if err = s.Close(); err != nil {
		return err
	}
	reopened, err := redis.New(opts)
	if err != nil {
		return err
	}
	defer reopened.Close()
	again, err := vectordata.GetTypedModelCollection[string, Document](reopened, "documents", &d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	n, err = again.Count(ctx, nil)
	if err != nil {
		return err
	}
	fmt.Printf("Records after reconnect: %d\n", n)
	return nil
}
