package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestApplyMigrationRejectsInvalidReviewBeforeDatabaseAccess(t *testing.T) {
	store := &PostgresVectorStore{opts: StoreOptions{Schema: "public"}}
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	base := vectordata.MigrationPlan{Backend: "postgres", Namespace: "public", Collection: "products", Revision: "review", Registered: &definition, Desired: definition}
	for _, tt := range []struct {
		name   string
		change func(*vectordata.MigrationPlan)
	}{
		{"backend", func(p *vectordata.MigrationPlan) { p.Backend = "memory" }},
		{"namespace", func(p *vectordata.MigrationPlan) { p.Namespace = "other" }},
		{"old review", func(p *vectordata.MigrationPlan) { p.Revision = "" }},
		{"registration", func(p *vectordata.MigrationPlan) { p.Registered = nil }},
		{"name", func(p *vectordata.MigrationPlan) { p.Collection = "" }},
		{"definition", func(p *vectordata.MigrationPlan) { p.Desired = vectordata.CollectionDefinition{} }},
		{"unserializable arguments", func(p *vectordata.MigrationPlan) { p.Steps = []vectordata.MigrationStep{{Args: []any{make(chan int)}}} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			review := base
			tt.change(&review)
			if err := store.ApplyMigration(context.Background(), review); !errors.Is(err, vectordata.ErrMigrationConflict) {
				t.Fatalf("invalid review: %v", err)
			}
		})
	}
}
