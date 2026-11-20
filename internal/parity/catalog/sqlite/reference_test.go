//go:build referenceparity

package sqliteparity

import (
	"database/sql"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

// Exercise both vec0 implementations against the same file before exposing a
// public adapter for physical collections created by the .NET connector.
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
			var body string
			var x, y float64
			if err = db.QueryRow(`SELECT body,json_extract(vec_to_json(direction),'$[0]'),json_extract(vec_to_json(direction),'$[1]') FROM articles JOIN vec_articles USING(id) WHERE id=7`).Scan(&body, &x, &y); err != nil || body != "reference" || x != 0 || y != 1 {
				t.Fatalf("reference row: %q [%g,%g] %v", body, x, y, err)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`INSERT INTO articles(id,body) VALUES(42,'from Go')`); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`INSERT INTO vec_articles(id,direction) VALUES(42,vec_f32('[1,0]'))`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			reference("verify")
			var data, vectors int
			if err = db.QueryRow(`SELECT (SELECT count(*) FROM articles WHERE id=42),(SELECT count(*) FROM vec_articles WHERE id=42)`).Scan(&data, &vectors); err != nil || data != 0 || vectors != 0 {
				t.Fatalf("after reference delete: data=%d vectors=%d err=%v", data, vectors, err)
			}
		})
	}
}
