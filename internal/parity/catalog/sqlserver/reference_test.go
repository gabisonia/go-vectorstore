//go:build referenceparity

package sqlserverparity

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"

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
			read := func(id int64) (string, []float32) {
				t.Helper()
				var body, raw string
				if err := db.QueryRowContext(ctx, "SELECT body,CAST(direction AS varchar(max)) FROM "+table+" WHERE id=@p1", id).Scan(&body, &raw); err != nil {
					t.Fatal(err)
				}
				var vector []float32
				if err := json.Unmarshal([]byte(raw), &vector); err != nil {
					t.Fatal(err)
				}
				return body, vector
			}
			if body, v := read(7); body != "reference" || !slices.Equal(v, []float32{0, 1}) {
				t.Fatalf("C# seed: %s %v", body, v)
			}
			execSQL("INSERT INTO "+table+" (id,body,direction) VALUES(@p1,@p2,CAST(@p3 AS vector(2)))", int64(42), "initial", "[0,1]")
			execSQL("UPDATE "+table+" SET body=@p1,direction=CAST(@p2 AS vector(2)) WHERE id=@p3", "from Go", "[1,0]", int64(42))
			rows, err := db.QueryContext(ctx, "SELECT TOP(2) id FROM "+table+" ORDER BY VECTOR_DISTANCE('cosine',direction,CAST(@p1 AS vector(2))),id", "[1,0]")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var ids []int64
			for rows.Next() {
				var id int64
				if err = rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ids, []int64{42, 7}) {
				t.Fatalf("Go ranking: %v", ids)
			}
			ref("verify")
			var n int
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id=42").Scan(&n); err != nil || n != 0 {
				t.Fatalf("read after C# delete: %d %v", n, err)
			}
		})
	}
}
