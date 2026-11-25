//go:build referenceparity

package sqlserverparity

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/parity/rankedguides"
	"github.com/gabisonia/go-vectorstore/stores/sqlserver"
	"github.com/gabisonia/go-vectorstore/vectordata"
	_ "github.com/microsoft/go-mssqldb"
)

// Exercise the physical SQL contract before exposing a public provider.
func TestSharedTable(t *testing.T) {
	db, err := sql.Open("sqlserver", os.Getenv("PARITY_SQL_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, creator := range []string{"go", "csharp"} {
		t.Run(creator, func(t *testing.T) {
			ctx := t.Context()
			name := "parity_" + creator
			// Identifiers come exclusively from the two fixed fixture cases above.
			table := "dbo.[" + name + "]"
			execSQL := func(stmt string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
					t.Fatal(err)
				}
			}
			ref := func(phase string) {
				t.Helper()
				out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput()
				if err != nil {
					t.Fatalf("reference %s: %v\n%s", phase, err, out)
				}
			}
			if creator == "go" {
				execSQL("CREATE TABLE " + table + " (id bigint NOT NULL PRIMARY KEY, body nvarchar(max), direction vector(2))")
			}
			t.Cleanup(func() {
				if _, err := db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table); err != nil {
					t.Errorf("drop fixture: %v", err)
				}
			})
			ref("seed")
			schema := sqlserver.NativeSchema{Table: name, Key: "id", Text: "body", Vector: "direction", Dimensions: 2}
			wrong := schema
			wrong.Dimensions = 3
			if _, err := sqlserver.AttachNative(ctx, db, wrong); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("incorrect dimensions: %v", err)
			}
			c, err := sqlserver.AttachNative(ctx, db, schema)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := c.Get(ctx, 7)
			if err != nil || seed.Text != "reference" || !slices.Equal(seed.Vector, []float32{0, 1}) {
				t.Fatalf("C# seed: %+v %v", seed, err)
			}
			seed.Text = "Go update"
			if err = c.Upsert(ctx, seed); err != nil {
				t.Fatal(err)
			}
			ref("update")
			seed, err = c.Get(ctx, 7)
			if err != nil || seed.Text != "C# update" {
				t.Fatalf("C# update: %+v %v", seed, err)
			}
			if err = c.Upsert(ctx, sqlserver.NativePoint{ID: 42, Text: "from Go", Vector: []float32{1, 0}}); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, []float32{1, 0}, 2)
			if err != nil || len(hits) != 2 || hits[0].ID != 42 || hits[1].ID != 7 || hits[0].Distance != 0 || hits[1].Distance != 1 {
				t.Fatalf("Go ranking: %+v %v", hits, err)
			}
			ref("verify")
			if _, err := c.Get(ctx, 42); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("C# delete: %v", err)
			}
			if err := c.Delete(ctx, 7); err != nil {
				t.Fatal(err)
			}
			ref("verify-empty")
		})
	}
}

func TestRankedGuides(t *testing.T) {
	db, err := sql.Open("sqlserver", os.Getenv("PARITY_SQL_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, "CREATE TABLE dbo.ranked_guides (id bigint NOT NULL PRIMARY KEY,category nvarchar(max),content_vector vector(2))"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP TABLE dbo.ranked_guides"); err != nil {
			t.Error(err)
		}
	}()
	c, err := sqlserver.AttachNative(ctx, db, sqlserver.NativeSchema{Table: "ranked_guides", Key: "id", Text: "category", Vector: "content_vector", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rankedguides.Articles() {
		if err := c.Upsert(ctx, sqlserver.NativePoint{ID: int64(a.ID), Text: a.Category, Vector: a.Direction}); err != nil {
			t.Fatal(err)
		}
	}
	searcher, err := sqlserver.NewNativeModelSearcher[rankedguides.Article](c, nil)
	if err != nil {
		t.Fatal(err)
	}
	rankedguides.Check(t, searcher)
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("%d connections still in use after iteration", inUse)
	}
}
