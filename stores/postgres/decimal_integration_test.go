//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIntegrationDecimalMigrationTransportAndBounds(t *testing.T) {
	ctx := context.Background()
	store, original, d := migrationFixture(t)
	d.Data = append(d.Data, vectordata.DataProperty{Name: "Price", Type: vectordata.TypeDecimal, Optional: true, IsIndexed: true})
	plan, err := store.PlanMigration(ctx, original.name, d)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("decimal migration: %+v, %v", plan, err)
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
	if err != nil || record.Data["Price"] != nil {
		t.Fatalf("migrated decimal: %v, %v", record, err)
	}
	for _, binary := range []bool{false, true} {
		for scale := 0; scale <= 28; scale++ {
			for _, sign := range []string{"", "-"} {
				value, err := vectordata.ParseDecimal(fmt.Sprintf("%s79228162514264337593543950335e-%d", sign, scale))
				if err != nil {
					t.Fatal(err)
				}
				record.Data["Price"] = value
				if _, err := c.writeEncoded(ctx, []vectordata.ModelRecord{record}, true, binary); err != nil {
					t.Fatalf("binary=%v scale=%d: %v", binary, scale, err)
				}
				row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
				if err != nil || row.Data["Price"] != value {
					t.Fatalf("decimal bound round trip: %v, %v", row, err)
				}
			}
		}
	}
	legacy := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Price", Type: vectordata.TypeFloat64, Optional: true}}}
	old, err := store.EnsureModelCollection(ctx, "legacy_price", legacy, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacy.Data[0].Type = vectordata.TypeDecimal
	conversion, err := store.PlanMigration(ctx, old.Name(), legacy)
	if err != nil || !conversion.HasManualWork() {
		t.Fatalf("float to decimal migration: %+v, %v", conversion, err)
	}
	manual := false
	for _, step := range conversion.Steps {
		manual = manual || step.Kind == vectordata.MigrationAlterProperty && step.Manual && step.RequiresBackfill && step.SQL == ""
	}
	if !manual {
		t.Fatalf("float conversion not manual: %+v", conversion)
	}
	if err := store.ApplyMigration(ctx, conversion); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("float conversion applied: %v", err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE "+c.table()+` ALTER COLUMN "Price" TYPE numeric(29,0) USING "Price"::numeric(29,0)`); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureExists(ctx); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("scale-coercing schema drift accepted: %v", err)
	}
}

func TestIntegrationDecimalRejectsOutOfBandValues(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	handle, err := store.EnsureModelCollection(ctx, "decimal_reads", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Price", Type: vectordata.TypeDecimal, Optional: true}}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	for _, text := range []string{"NaN", "Infinity", "-Infinity", "1e-29", "79228162514264337593543950336", "-79228162514264337593543950336", "7.9228162514264337593543950336"} {
		t.Run(text, func(t *testing.T) {
			if _, err := store.pool.Exec(ctx, "INSERT INTO "+c.table()+` ("ID","Price") VALUES ('bad',$1::numeric) ON CONFLICT ("ID") DO UPDATE SET "Price"=EXCLUDED."Price"`, text); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get(ctx, "bad", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("invalid numeric read: %v", err)
			}
		})
	}
}

func TestIntegrationDecimalBTreePlans(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	handle, err := store.EnsureModelCollection(ctx, "decimal_index", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Price", Type: vectordata.TypeDecimal, IsIndexed: true}}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	const exact = "9007199254740993.000000001"
	records := make([]vectordata.ModelRecord, 2000)
	for i := range records {
		price := strconv.Itoa(i)
		if i%100 == 0 {
			price = exact
		}
		records[i] = vectordata.ModelRecord{Key: strconv.Itoa(i), Data: map[string]any{"Price": price}}
	}
	if _, err := c.Insert(ctx, records); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "ANALYZE "+c.table()); err != nil {
		t.Fatal(err)
	}
	filter := vectordata.Eq(vectordata.Column("Price"), vectordata.Decimal(exact+"0"))
	query, args, err := c.queryPlan(filter, 100, vectordata.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) == 0 || !reflect.DeepEqual(args[0], vectordata.Decimal(exact)) {
		t.Fatalf("inexact decimal binding: %v", args)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTx(ctx, tx)
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan.WriteString(line)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	indexes, err := modelIndexSpecs(c.name, c.definition)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, index := range indexes {
		if index.property == "Price" {
			found = index.method == "btree" && strings.Contains(plan.String(), index.name)
		}
	}
	if !found {
		t.Fatalf("decimal B-tree not eligible: %s", plan.String())
	}
	got, err := c.Query(ctx, filter, 100, vectordata.QueryOptions{})
	if err != nil || len(got) != 20 {
		t.Fatalf("exact index results: %d, %v", len(got), err)
	}
}
