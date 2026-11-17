//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIntegrationAnnotationMigrationRegistration(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, integrationPool(t))
	a, err := vectordata.ParseProviderAnnotations([]byte(`{"vendor.settings":{"n":1e20,"exact":0.1000000000000000000000000001}}`))
	if err != nil {
		t.Fatal(err)
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", ProviderAnnotations: a}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, ProviderAnnotations: a}}}
	if _, err := s.EnsureModelCollection(ctx, "annotations", d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanMigration(ctx, "annotations", d)
	if err != nil || len(plan.Steps) != 0 || len(plan.Issues) != 0 || plan.Finalization != nil {
		t.Fatalf("JSONB numeric spelling created a migration: %+v %v", plan, err)
	}
	if plan.Registered == nil || plan.Registered.Key.ProviderAnnotations != a || plan.Registered.Data[0].ProviderAnnotations != a {
		t.Fatal("registered annotations lost precision")
	}
	d.Data[0].ProviderAnnotations, err = vectordata.WithProviderAnnotation(a, "vendor.change", true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = s.PlanMigration(ctx, "annotations", d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyMigration(ctx, plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("annotation schema change implicitly applied: %v", err)
	}
}
