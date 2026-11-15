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
	"github.com/jackc/pgx/v5/pgxpool"
)

func calendarProperties() []vectordata.DataProperty {
	return []vectordata.DataProperty{
		{Name: "Date", Type: vectordata.TypeDateOnly, Optional: true, IsIndexed: true},
		{Name: "Clock", Type: vectordata.TypeTimeOnly, Optional: true, IsIndexed: true},
		{Name: "Dates", Type: vectordata.TypeDateOnlyArray, Optional: true, IsIndexed: true},
		{Name: "Clocks", Type: vectordata.TypeTimeOnlyArray, Optional: true, IsIndexed: true},
	}
}

func TestIntegrationCalendarMigrationAndTransport(t *testing.T) {
	ctx := context.Background()
	store, original, d := migrationFixture(t)
	d.Data = append(d.Data, calendarProperties()...)
	plan, err := store.PlanMigration(ctx, original.name, d)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("calendar migration: %+v, %v", plan, err)
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
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range calendarProperties() {
		if record.Data[p.Name] != nil {
			t.Fatalf("new calendar field is not NULL: %v", record)
		}
	}
	want := map[string]any{"Date": "2024-02-29", "Clock": "23:59:59.999999", "Dates": []string{"0001-01-01", "9999-12-31"}, "Clocks": []string{"00:00:00.000000", "23:59:59.999999"}}
	for _, binary := range []bool{false, true} {
		record.Data["Date"] = vectordata.DateOnly("2024-02-29")
		record.Data["Clock"] = vectordata.TimeOnly("23:59:59.999999999")
		record.Data["Dates"] = []vectordata.DateOnly{"0001-01-01", "9999-12-31"}
		record.Data["Clocks"] = []vectordata.TimeOnly{"00:00:00", "23:59:59.999999999"}
		if _, err := c.writeEncoded(ctx, []vectordata.ModelRecord{record}, true, binary); err != nil {
			t.Fatalf("binary=%v write: %v", binary, err)
		}
		row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range want {
			if !reflect.DeepEqual(row.Data[name], value) {
				t.Fatalf("binary=%v field=%s: %v", binary, name, row)
			}
		}
	}
	// Calendar conversions remain explicit even if their old values are ISO text.
	for _, kind := range []vectordata.DataType{vectordata.TypeDateOnly, vectordata.TypeTimeOnly} {
		legacy := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Value", Type: vectordata.TypeString, Optional: true}}}
		old, err := store.EnsureModelCollection(ctx, "legacy_"+string(kind), legacy, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		legacy.Data[0].Type = kind
		conversion, err := store.PlanMigration(ctx, old.Name(), legacy)
		if err != nil || !conversion.HasManualWork() {
			t.Fatalf("calendar conversion: %+v, %v", conversion, err)
		}
		manual := false
		for _, step := range conversion.Steps {
			manual = manual || step.Kind == vectordata.MigrationAlterProperty && step.Manual && step.RequiresBackfill && step.SQL == ""
		}
		if !manual {
			t.Fatalf("conversion not manual: %+v", conversion)
		}
		if err := store.ApplyMigration(ctx, conversion); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
			t.Fatalf("conversion applied: %v", err)
		}
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE "+c.table()+` ALTER COLUMN "Clock" TYPE time with time zone USING "Clock" AT TIME ZONE 'UTC'`); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureExists(ctx); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("calendar type drift accepted: %v", err)
	}
}

func TestIntegrationCalendarSessionSettingsAndInvalidReads(t *testing.T) {
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(integrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["timezone"] = "Pacific/Auckland"
	cfg.ConnConfig.RuntimeParams["datestyle"] = "German, DMY"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := newTestStore(t, pool)
	handle, err := store.EnsureModelCollection(ctx, "calendar_settings", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: calendarProperties()}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	want := map[string]any{"Date": "2024-02-29", "Clock": "09:02:03.123456", "Dates": []string{"0001-01-01", "9999-12-31"}, "Clocks": []string{"00:00:00.000000", "23:59:59.999999"}}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Data: want}}); err != nil {
		t.Fatal(err)
	}
	row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || !reflect.DeepEqual(row.Data, want) {
		t.Fatalf("session calendar round trip: %v, %v", row, err)
	}
	rows, err := c.Query(ctx, vectordata.And(vectordata.Eq(vectordata.Column("Date"), "2024-02-29"), vectordata.Eq(vectordata.Column("Clock"), "09:02:03.123456789"), vectordata.Contains(vectordata.Column("Dates"), "0001-01-01")), 1, vectordata.QueryOptions{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("session calendar filters: %v, %v", rows, err)
	}
	for _, tt := range []struct{ field, expression string }{
		{"Date", "'infinity'::date"}, {"Date", "'-infinity'::date"}, {"Date", "'10000-01-01'::date"}, {"Date", "'0001-01-01 BC'::date"}, {"Clock", "'24:00:00'::time"},
	} {
		t.Run(tt.expression, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "INSERT INTO "+c.table()+` ("ID",`+quoteIdent(tt.field)+`) VALUES ('bad',`+tt.expression+")"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get(ctx, "bad", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("invalid calendar read: %v", err)
			}
			if _, err := pool.Exec(ctx, "DELETE FROM "+c.table()+` WHERE "ID"='bad'`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIntegrationCalendarBTreePlans(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	handle, err := store.EnsureModelCollection(ctx, "calendar_indexes", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: calendarProperties()[:2]}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	records := make([]vectordata.ModelRecord, 2000)
	for i := range records {
		date, clock := "2026-01-01", "00:00:00"
		if i%100 == 0 {
			date, clock = "2024-02-29", "09:02:03.123456"
		}
		records[i] = vectordata.ModelRecord{Key: strconv.Itoa(i), Data: map[string]any{"Date": date, "Clock": clock}}
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
	for _, tt := range []struct{ field, value string }{{"Date", "2024-02-29"}, {"Clock", "09:02:03.123456789"}} {
		t.Run(tt.field, func(t *testing.T) {
			filter := vectordata.Eq(vectordata.Column(tt.field), tt.value)
			query, args, err := c.queryPlan(filter, 100, vectordata.QueryOptions{})
			if err != nil {
				t.Fatal(err)
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
			found := false
			for _, index := range indexes {
				if index.property == tt.field {
					found = index.method == "btree" && strings.Contains(plan.String(), index.name)
				}
			}
			if !found {
				t.Fatalf("calendar index not eligible: %s", plan.String())
			}
			got, err := c.Query(ctx, filter, 100, vectordata.QueryOptions{})
			if err != nil || len(got) != 20 {
				t.Fatalf("calendar index results: %d, %v", len(got), err)
			}
		})
	}
}
