//go:build referenceparity

package redisparity

import (
	"errors"
	"os/exec"
	"slices"
	"testing"

	storeRedis "github.com/gabisonia/go-vectorstore/stores/redis"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/redis/go-redis/v9"
)

func TestNativeLayouts(t *testing.T) {
	ctx := t.Context()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379", Protocol: 2, MaxRetries: -1})
	defer client.Close()
	store, err := storeRedis.New(storeRedis.StoreOptions{Addr: "localhost:6379"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, layout := range []string{"JSON", "HASH"} {
		for _, creator := range []string{"go", "csharp"} {
			t.Run(layout+"_"+creator, func(t *testing.T) {
				name := "reference_" + layout + "_" + creator
				ref := func(phase string) {
					t.Helper()
					out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name, layout).CombinedOutput()
					if err != nil {
						t.Fatalf("C# %s: %s %v", phase, out, err)
					}
				}
				if creator == "go" {
					field := "Direction"
					if layout == "JSON" {
						field = "$.Direction"
					}
					err := client.Do(ctx, "FT.CREATE", name, "ON", layout, "PREFIX", 1, name+":", "SCHEMA", field, "AS", "Direction", "VECTOR", "FLAT", 6, "TYPE", "FLOAT32", "DIM", 2, "DISTANCE_METRIC", "COSINE").Err()
					if err != nil {
						t.Fatal(err)
					}
				}
				ref("seed")
				if _, err := store.AttachNativeCosine(ctx, name, "Direction", 3); !errors.Is(err, vectordata.ErrSchemaMismatch) {
					t.Fatalf("wrong dimensions: %v", err)
				}
				index, err := store.AttachNativeCosine(ctx, name, "Direction", 2)
				if err != nil {
					t.Fatal(err)
				}
				seed, err := index.GetDocument(ctx, "seed", "Body")
				if err != nil || seed.Text != "reference" || !slices.Equal(seed.Vector, []float32{0, 1}) {
					t.Fatalf("reference record: %+v %v", seed, err)
				}
				point := storeRedis.NativeDocument{ID: "go", Text: "from Go", Vector: []float32{1, 0}}
				if err := index.UpsertDocument(ctx, "Body", point); err != nil {
					t.Fatal(err)
				}
				hits, err := index.Search(ctx, point.Vector, 2)
				if err != nil || len(hits) != 2 || hits[0].ID != "go" || hits[1].ID != "seed" || hits[0].Distance != 0 || hits[1].Distance != 1 {
					t.Fatalf("Go search: %+v %v", hits, err)
				}
				ref("verify")
				if _, err := index.GetDocument(ctx, "go", "Body"); !errors.Is(err, redis.Nil) {
					t.Fatalf("read after C# deletion: %v", err)
				}
				if err := index.DeleteDocument(ctx, "seed"); err != nil {
					t.Fatal(err)
				}
				if _, err := index.GetDocument(ctx, "seed", "Body"); !errors.Is(err, redis.Nil) {
					t.Fatalf("read after Go deletion: %v", err)
				}
			})
		}
	}
}
