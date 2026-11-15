//go:build integration

package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIntegrationNativeDataMigrationAndTransport(t *testing.T) {
	store, original, d := migrationFixture(t)
	ctx := context.Background()
	d.Data = append(d.Data, vectordata.DataProperty{Name: "Owner", StorageName: `owner"uuid`, Type: vectordata.TypeUUID, Optional: true, IsIndexed: true}, vectordata.DataProperty{Name: "Payload", Type: vectordata.TypeBytes, Optional: true, IsIndexed: true})
	plan, err := store.PlanMigration(ctx, original.name, d)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("native addition plan: %+v, %v", plan, err)
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
	if err != nil || record.Data["Owner"] != nil || record.Data["Payload"] != nil || record.Data["Title"] != "first" {
		t.Fatalf("migrated existing data: %v, %v", record, err)
	}
	const uuid = "8f516db6-38ca-4e8f-ad79-d71b015f5419"
	for _, binary := range []bool{false, true} {
		record.Data["Owner"] = strings.ToUpper(uuid)
		record.Data["Payload"] = []byte{0, 1, 255}
		if _, err := c.writeEncoded(ctx, []vectordata.ModelRecord{record}, true, binary); err != nil {
			t.Fatalf("binary=%v native write: %v", binary, err)
		}
		row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
		if err != nil || row.Data["Owner"] != uuid || !reflect.DeepEqual(row.Data["Payload"], []byte{0, 1, 255}) {
			t.Fatalf("binary=%v native read: %v, %v", binary, row, err)
		}
	}
	converted := c.definition.Clone()
	converted.Data[len(converted.Data)-2].Type = vectordata.TypeString
	converted.Data[len(converted.Data)-1].Type = vectordata.TypeJSON
	converted.Data[len(converted.Data)-1].IsIndexed = false
	conversion, err := store.PlanMigration(ctx, c.name, converted)
	if err != nil || !conversion.HasManualWork() {
		t.Fatalf("conversion classification: %+v, %v", conversion, err)
	}
	manual := 0
	for _, step := range conversion.Steps {
		if step.Kind == vectordata.MigrationAlterProperty && step.Manual && step.DataLoss && step.RequiresBackfill && step.SQL == "" {
			manual++
		}
	}
	if manual != 2 {
		t.Fatalf("expected both native conversions to be manual: %+v", conversion.Steps)
	}
	if err := store.ApplyMigration(ctx, conversion); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("applied conversion: %v", err)
	}
	// Native opclasses are part of index validation and drift detection.
	indexes, err := modelIndexSpecs(c.name, c.definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range indexes {
		if index.property != "Owner" {
			continue
		}
		if _, err := store.pool.Exec(ctx, "DROP INDEX "+qualifiedTable(store.opts.Schema, index.name)+"; CREATE INDEX "+quoteIdent(index.name)+" ON "+c.table()+" USING hash ("+quoteIdent(index.column)+")"); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.EnsureExists(ctx); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("accepted incompatible native index: %v", err)
	}
	drift, err := store.PlanMigration(ctx, c.name, c.definition)
	if err != nil || len(drift.Issues) == 0 || drift.Finalization != nil {
		t.Fatalf("native index drift was not blocked: %+v, %v", drift, err)
	}
}

func TestIntegrationRequiredNativeAdditionsNeedBackfill(t *testing.T) {
	for _, kind := range []vectordata.DataType{vectordata.TypeUUID, vectordata.TypeBytes} {
		t.Run(string(kind), func(t *testing.T) {
			store, c, d := migrationFixture(t)
			d.Data = append(d.Data, vectordata.DataProperty{Name: "Required", Type: kind})
			plan, err := store.PlanMigration(context.Background(), c.name, d)
			if err != nil || !plan.HasManualWork() {
				t.Fatalf("required native plan: %+v, %v", plan, err)
			}
			if err := store.ApplyMigration(context.Background(), plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
				t.Fatalf("required native apply: %v", err)
			}
			assertOriginalMigrationStorage(t, c)
		})
	}
}
