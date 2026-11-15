//go:build integration

package postgres

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func arrayProperties() []vectordata.DataProperty {
	return []vectordata.DataProperty{{Name: "Ints", Type: vectordata.TypeInt64Array, Optional: true, IsIndexed: true}, {Name: "Floats", Type: vectordata.TypeFloat64Array, Optional: true, IsIndexed: true}, {Name: "Flags", Type: vectordata.TypeBoolArray, Optional: true, IsIndexed: true}}
}

func TestIntegrationNativeArrayMigrationAndTransport(t *testing.T) {
	ctx := context.Background()
	store, original, d := migrationFixture(t)
	d.Data = append(d.Data, arrayProperties()...)
	plan, err := store.PlanMigration(ctx, original.name, d)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("native array plan: %+v, %v", plan, err)
	}
	if err := store.ApplyMigration(ctx, plan); err != nil {
		t.Fatal(err)
	}
	handle, err := store.EnsureModelCollection(ctx, original.name, d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	record, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || record.Data["Title"] != "first" || record.Data["Ints"] != nil || record.Data["Floats"] != nil || record.Data["Flags"] != nil {
		t.Fatalf("migrated record: %v, %v", record, err)
	}
	for _, binary := range []bool{false, true} {
		record.Data["Ints"] = []int32{1, -2}
		record.Data["Floats"] = []float32{0.25, -1.5}
		record.Data["Flags"] = []bool{true, false}
		if _, err := c.writeEncoded(ctx, []vectordata.ModelRecord{record}, true, binary); err != nil {
			t.Fatalf("binary=%v array write: %v", binary, err)
		}
		row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
		if err != nil || !reflect.DeepEqual(row.Data["Ints"], []int64{1, -2}) || !reflect.DeepEqual(row.Data["Floats"], []float64{0.25, -1.5}) || !reflect.DeepEqual(row.Data["Flags"], []bool{true, false}) {
			t.Fatalf("binary=%v array read: %v, %v", binary, row, err)
		}
	}
	// A different native array type is storage drift even though both use array_ops.
	if _, err := store.pool.Exec(ctx, "ALTER TABLE "+c.table()+` ALTER COLUMN "Ints" TYPE integer[] USING "Ints"::integer[]`); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureExists(ctx); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("native array type drift: %v", err)
	}
	drift, err := store.PlanMigration(ctx, c.name, c.definition)
	if err != nil || len(drift.Issues) == 0 || drift.Finalization != nil {
		t.Fatalf("array drift was not blocked: %+v, %v", drift, err)
	}
}

func TestIntegrationNativeArrayMigrationClassifications(t *testing.T) {
	for _, p := range arrayProperties() {
		t.Run(string(p.Type), func(t *testing.T) {
			ctx := context.Background()
			store, c, d := migrationFixture(t)
			required := d.Clone()
			p.Optional = false
			required.Data = append(required.Data, p)
			plan, err := store.PlanMigration(ctx, c.name, required)
			if err != nil || !plan.HasManualWork() {
				t.Fatalf("required array plan: %+v, %v", plan, err)
			}
			if err := store.ApplyMigration(ctx, plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
				t.Fatalf("required array apply: %v", err)
			}
			assertOriginalMigrationStorage(t, c)
			legacy := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Values", Type: vectordata.TypeJSON, Optional: true}}}
			old, err := store.EnsureModelCollection(ctx, "legacy_array", legacy, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			legacy.Data[0].Type = p.Type
			conversion, err := store.PlanMigration(ctx, old.Name(), legacy)
			if err != nil || !conversion.HasManualWork() {
				t.Fatalf("JSON array conversion: %+v, %v", conversion, err)
			}
			found := false
			for _, step := range conversion.Steps {
				found = found || step.Kind == vectordata.MigrationAlterProperty && step.Manual && step.DataLoss && step.RequiresBackfill && step.SQL == ""
			}
			if !found {
				t.Fatalf("conversion flags: %+v", conversion.Steps)
			}
			if err := store.ApplyMigration(ctx, conversion); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
				t.Fatalf("JSON conversion applied: %v", err)
			}
			if err := old.EnsureExists(ctx); err != nil {
				t.Fatalf("legacy JSON mapping changed: %v", err)
			}
		})
	}
}

func TestIntegrationNativeArrayGINPlans(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: arrayProperties()}
	handle, err := store.EnsureModelCollection(ctx, "array_plans", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	records := make([]vectordata.ModelRecord, 2000)
	for i := range records {
		ints, floats, flags := []int64{int64(i)}, []float64{float64(i)}, []bool{true}
		if i%100 == 0 {
			ints = append(ints, -7)
			floats = append(floats, 0.25)
			flags = append(flags, false)
		}
		records[i] = vectordata.ModelRecord{Key: strconv.Itoa(i), Data: map[string]any{"Ints": ints, "Floats": floats, "Flags": flags}}
	}
	if _, err := c.Insert(ctx, records); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "ANALYZE "+c.table()); err != nil {
		t.Fatal(err)
	}
	indexes, err := modelIndexSpecs(c.name, c.definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		field  string
		needle any
	}{{"Ints", int16(-7)}, {"Floats", float32(0.25)}, {"Flags", false}} {
		t.Run(tt.field, func(t *testing.T) {
			filter := vectordata.Contains(vectordata.Column(tt.field), tt.needle)
			sql, args, err := c.queryPlan(filter, 100, vectordata.QueryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// Force index eligibility for a deterministic check; production cost
			// choices still depend on selectivity and GIN pending-list statistics.
			tx, err := store.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTx(ctx, tx)
			if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			var explain strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				explain.WriteString(line)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			for _, index := range indexes {
				if index.property == tt.field && (index.method != "gin" || !strings.Contains(explain.String(), index.name)) {
					t.Fatalf("GIN index not eligible: %s", explain.String())
				}
			}
			got, err := c.Query(ctx, filter, 100, vectordata.QueryOptions{})
			if err != nil || len(got) != 20 {
				t.Fatalf("typed membership results: %d, %v", len(got), err)
			}
		})
	}
}

func TestIntegrationNativeArrayRejectsOutOfBandShapes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	handle, err := store.EnsureModelCollection(ctx, "array_shapes", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: arrayProperties()}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	for _, tt := range []struct{ field, expression string }{
		{"Ints", "ARRAY[1,NULL]::bigint[]"}, {"Ints", "ARRAY[[1,2],[3,4]]::bigint[]"}, {"Ints", "'[0:1]={1,2}'::bigint[]"},
		{"Floats", "ARRAY[1,'NaN']::double precision[]"}, {"Floats", "ARRAY['Infinity']::double precision[]"},
		{"Flags", "ARRAY[true,NULL]::boolean[]"},
	} {
		t.Run(tt.field+tt.expression, func(t *testing.T) {
			if _, err := store.pool.Exec(ctx, "INSERT INTO "+c.table()+" (\"ID\","+quoteIdent(tt.field)+") VALUES ('bad',"+tt.expression+") ON CONFLICT (\"ID\") DO UPDATE SET "+quoteIdent(tt.field)+"=EXCLUDED."+quoteIdent(tt.field)); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get(ctx, "bad", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("unsupported SQL array accepted: %v", err)
			}
			if _, err := store.pool.Exec(ctx, "DELETE FROM "+c.table()+" WHERE \"ID\"='bad'"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
