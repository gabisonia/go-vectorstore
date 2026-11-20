//go:build referenceparity

package postgresparity

import (
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

type article struct {
	ID        int64     `vectordata:"key,name=id"`
	Body      string    `vectordata:"data,name=body"`
	Direction []float32 `vectordata:"vector,name=direction,dimensions=2"`
}

func TestSharedTable(t *testing.T) {
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, os.Getenv("PARITY_PG_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewVectorStore(pool, postgres.DefaultStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, creator := range []string{"go", "csharp"} {
		t.Run(creator, func(t *testing.T) {
			name := "reference_" + creator
			ref := func(phase string) {
				t.Helper()
				out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput()
				if err != nil {
					t.Fatalf("C# %s: %s: %v", phase, out, err)
				}
			}
			c, err := vectordata.GetTypedModelCollection[int64, article](store, name, nil, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if creator == "go" {
				if err = c.EnsureExists(ctx); err != nil {
					t.Fatal(err)
				}
			}
			ref("seed")
			got, err := c.Get(ctx, 7, vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || got.Body != "reference" || !slices.Equal(got.Direction, []float32{0, 1}) {
				t.Fatalf("reference point: %+v %v", got, err)
			}
			if _, err = c.UpsertOne(ctx, article{42, "from Go", []float32{1, 0}}); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{})
			if err != nil || len(hits) != 2 || hits[0].Item.ID != 42 || hits[1].Item.ID != 7 {
				t.Fatalf("Go ranking: %+v %v", hits, err)
			}
			ref("verify")
			if _, found, err := c.TryGet(ctx, 42, vectordata.RetrievalOptions{}); err != nil || found {
				t.Fatalf("read after C# delete: %t %v", found, err)
			}
		})
	}
}
