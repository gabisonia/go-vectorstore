//go:build referenceparity

package oracleparity

import (
	"database/sql"
	"errors"
	"math"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/oracle"
	"github.com/gabisonia/go-vectorstore/vectordata"
	_ "github.com/sijms/go-ora/v2"
)

func TestSharedTable(t *testing.T) {
	db, err := sql.Open("oracle", os.Getenv("PARITY_ORACLE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"PARITY_GO", "PARITY_CSHARP"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ref := func(phase string) {
				t.Helper()
				if out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput(); err != nil {
					t.Fatalf("C# %s: %s %v", phase, out, err)
				}
			}
			if name == "PARITY_GO" {
				if _, err := db.ExecContext(ctx, "CREATE TABLE PARITY_GO (ID NUMBER(19) PRIMARY KEY,CATEGORY NUMBER(10),DIRECTION VECTOR(2,FLOAT32))"); err != nil {
					t.Fatal(err)
				}
			} else {
				ref("create")
			}
			ref("seed")
			s := oracle.NativeSchema{Table: name, Key: "ID", Category: "CATEGORY", Vector: "DIRECTION", Dimensions: 2}
			wrong := s
			wrong.Dimensions++
			if _, err := oracle.AttachNative(ctx, db, wrong); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("wrong dimensions: %v", err)
			}
			c, err := oracle.AttachNative(ctx, db, s)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := c.Get(ctx, 7)
			if err != nil || seed.Category == nil || *seed.Category != 1 || !slices.Equal(seed.Vector, []float32{0, 1}) {
				t.Fatalf("C# seed %+v %v", seed, err)
			}
			p := oracle.NativePoint{ID: 42, Vector: []float32{0, 1}}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			got, err := c.Get(ctx, 42)
			if err != nil || got.Category != nil {
				t.Fatalf("null category: %+v %v", got, err)
			}
			category := int32(1)
			p.Category, p.Vector = &category, []float32{0.8, 0.6}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			v := []float32{1, 0}
			all, err := c.Search(ctx, v, 3, nil)
			if err != nil || len(all) != 3 || all[0].ID != 8 {
				t.Fatalf("unfiltered ranking: %+v %v", all, err)
			}
			hits, err := c.Search(ctx, v, 3, &category)
			if err != nil || len(hits) != 2 || hits[0].ID != 42 || hits[1].ID != 7 || math.Abs(hits[0].Distance-0.2) > 1e-5 || math.Abs(hits[1].Distance-1) > 1e-5 {
				t.Fatalf("filtered ranking: %+v %v", hits, err)
			}
			ref("verify")
			got, err = c.Get(ctx, 42)
			if err != nil || got.Category == nil || *got.Category != 3 {
				t.Fatalf("C# update %+v %v", got, err)
			}
			if _, err := c.Get(ctx, 7); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("C# delete: %v", err)
			}
			if err := c.Delete(ctx, 42); err != nil {
				t.Fatal(err)
			}
			ref("verify-delete")
		})
	}
}
