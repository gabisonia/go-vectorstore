//go:build referenceparity

package sqliteparity

import (
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/sqlitevec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Both clients use their public APIs on the same physical file.
func TestSharedFile(t *testing.T) {
	for _, creator := range []string{"go", "csharp"} {
		t.Run(creator, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shared.db")
			reference := func(phase string) {
				t.Helper()
				out, err := exec.CommandContext(t.Context(), "dotnet", "Reference.dll", phase, path).CombinedOutput()
				if err != nil {
					t.Fatalf("%s: %v: %s", phase, err, out)
				}
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if creator == "go" {
				if _, err = db.Exec(`CREATE TABLE articles(id INTEGER PRIMARY KEY,body TEXT);
 CREATE VIRTUAL TABLE vec_articles USING vec0(id INTEGER PRIMARY KEY,direction float[2] distance_metric=cosine);`); err != nil {
					t.Fatal(err)
				}
			}
			reference("seed")
			schema := sqlitevec.NativeSchema{DataTable: "articles", Key: "id", Text: "body", Vector: "direction", Dimensions: 2}
			ctx := t.Context()
			wrong := schema
			wrong.Dimensions = 3
			if _, err := sqlitevec.AttachNative(ctx, db, wrong); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("incorrect dimensions: %v", err)
			}
			collection, err := sqlitevec.AttachNative(ctx, db, schema)
			if err != nil {
				t.Fatal(err)
			}
			record, err := collection.Get(ctx, 7)
			if err != nil || record.Text != "reference" || !slices.Equal(record.Vector, []float32{0, 1}) {
				t.Fatalf("C# seed: %+v, %v", record, err)
			}
			point := sqlitevec.NativePoint{ID: 42, Text: "from Go", Vector: []float32{1, 0}}
			if err := collection.Upsert(ctx, point); err != nil {
				t.Fatal(err)
			}
			if err := collection.Upsert(ctx, point); err != nil {
				t.Fatal(err)
			}
			hits, err := collection.Search(ctx, point.Vector, 2)
			if err != nil || len(hits) != 2 || hits[0].ID != 42 || hits[1].ID != 7 || hits[0].Distance != 0 || hits[1].Distance != 1 {
				t.Fatalf("Go ranking: %+v %v", hits, err)
			}
			// Force the second statement of a delete to fail after the vector row was removed.
			if _, err = db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON articles BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
				t.Fatal(err)
			}
			if err = collection.Delete(ctx, 42); err == nil || errors.Is(err, sqlitevec.ErrNativeOutcomeUnknown) {
				t.Fatalf("expected rollback: %v", err)
			}
			if _, err = db.Exec("DROP TRIGGER reject_delete"); err != nil {
				t.Fatal(err)
			}
			reference("verify")
			var data, vectors int
			if err = db.QueryRow(`SELECT (SELECT count(*) FROM articles WHERE id=42),(SELECT count(*) FROM vec_articles WHERE id=42)`).Scan(&data, &vectors); err != nil || data != 0 || vectors != 0 {
				t.Fatalf("after reference delete: data=%d vectors=%d err=%v", data, vectors, err)
			}
			if err := collection.Delete(ctx, 7); err != nil {
				t.Fatal(err)
			}
			if _, err := collection.Get(ctx, 7); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("read after delete: %v", err)
			}
		})
	}
}
