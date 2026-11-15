package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

var _ vectordata.MigrationApplier = (*PostgresVectorStore)(nil)

// ApplyMigration applies a reviewed optional-property/index addition atomically.
// Caller-supplied SQL and risk flags are compared with regenerated output; only
// SQL produced from the locked live state is executed.
func (s *PostgresVectorStore) ApplyMigration(ctx context.Context, reviewed vectordata.MigrationPlan) (err error) {
	name := reviewed.Collection
	defer func() {
		if err != nil {
			err = &vectordata.OperationError{Backend: "postgres", Collection: name, Operation: "apply_migration", Err: err}
		}
	}()
	if reviewed.Backend != "postgres" || reviewed.Namespace != s.opts.Schema || reviewed.Revision == "" || reviewed.Registered == nil {
		return fmt.Errorf("%w: review does not identify this store and a registered snapshot", vectordata.ErrMigrationConflict)
	}
	// Freeze the review so later comparisons and desired-schema construction share
	// one value. JSON also strips runtime embedder instances, as planning does.
	reviewJSON, err := json.Marshal(reviewed)
	if err != nil {
		return fmt.Errorf("%w: invalid review: %v", vectordata.ErrMigrationConflict, err)
	}
	var frozen vectordata.MigrationPlan
	if err := json.Unmarshal(reviewJSON, &frozen); err != nil {
		return fmt.Errorf("%w: invalid review: %v", vectordata.ErrMigrationConflict, err)
	}
	reviewed = frozen
	handle, err := s.GetModelCollection(name, reviewed.Desired, vectordata.ModelCollectionOptions{})
	if err != nil {
		return fmt.Errorf("%w: %w", vectordata.ErrMigrationConflict, err)
	}
	c := handle.(*ModelCollection)
	indexes, err := modelIndexSpecs(name, c.definition)
	if err != nil {
		return fmt.Errorf("%w: %w", vectordata.ErrMigrationConflict, err)
	}
	// READ COMMITTED is intentional: after waiting for locks, subsequent inspection
	// must see newly committed registrations and DDL, rather than an older snapshot.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.table()); err != nil {
		return err
	}
	preflight, err := c.planMigrationTx(ctx, tx, indexes)
	if err != nil {
		return err
	}
	if err := validateMigrationReview(reviewJSON, preflight); err != nil {
		return err
	}
	if err := validateAdditiveMigration(preflight); err != nil {
		return err
	}
	// Validate ordinary-table/registry shape before requesting locks on their names.
	// Cooperative lifecycle operations share the advisory lock. External DDL is
	// excluded after table locks; the full snapshot is checked again below.
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+c.table()+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return err
	}
	registry := qualifiedTable(s.opts.Schema, modelRegistry)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+registry+" IN ROW SHARE MODE"); err != nil {
		return err
	}
	var row int
	if err := tx.QueryRow(ctx, "SELECT 1 FROM "+registry+" WHERE name=$1 FOR UPDATE", name).Scan(&row); err != nil {
		return fmt.Errorf("%w: registry row cannot be locked: %w", vectordata.ErrMigrationConflict, err)
	}
	locked, err := c.planMigrationTx(ctx, tx, indexes)
	if err != nil {
		return err
	}
	if err := validateMigrationReview(reviewJSON, locked); err != nil {
		return err
	}
	if err := validateAdditiveMigration(locked); err != nil {
		return err
	}
	for _, step := range locked.Steps {
		if step.SQL == "" {
			continue
		}
		if _, err := tx.Exec(ctx, step.SQL, step.Args...); err != nil {
			return err
		}
	}
	drift, err := c.migrationDrift(ctx, tx, c.definition)
	if err != nil {
		return err
	}
	if len(drift) > 0 {
		return fmt.Errorf("%w: %w: applied storage does not match the desired model: %v", vectordata.ErrMigrationConflict, vectordata.ErrSchemaMismatch, drift)
	}
	if final := locked.Finalization; final != nil {
		tag, err := tx.Exec(ctx, final.SQL, final.Args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != final.ExpectedRows {
			return fmt.Errorf("%w: registry finalization affected %d rows, expected %d", vectordata.ErrMigrationConflict, tag.RowsAffected(), final.ExpectedRows)
		}
	}
	// Validate registration as well as storage after finalization, including effects
	// of database triggers; an unexpected result rolls back all preceding DDL.
	final, err := c.planMigrationTx(ctx, tx, indexes)
	if err != nil {
		return err
	}
	if final.HasManualWork() || len(final.Steps) != 0 || final.Finalization != nil {
		return fmt.Errorf("%w: %w: final model validation failed", vectordata.ErrMigrationConflict, vectordata.ErrSchemaMismatch)
	}
	return tx.Commit(ctx)
}
func validateMigrationReview(reviewJSON []byte, fresh vectordata.MigrationPlan) error {
	if len(fresh.Issues) > 0 {
		return fmt.Errorf("%w: %w: live storage/registration has issues: %v", vectordata.ErrMigrationConflict, vectordata.ErrSchemaMismatch, fresh.Issues)
	}
	current, err := json.Marshal(fresh)
	if err != nil {
		return err
	}
	if !bytes.Equal(reviewJSON, current) {
		return fmt.Errorf("%w: review differs from the regenerated plan; review a fresh plan", vectordata.ErrMigrationConflict)
	}
	return nil
}
func validateAdditiveMigration(plan vectordata.MigrationPlan) error {
	deny := func(property string) error {
		return fmt.Errorf("%w: existing properties must remain unchanged; only optional data and index additions are supported (%s)", vectordata.ErrMigrationNotAdditive, property)
	}
	if plan.Registered == nil || plan.HasManualWork() {
		return deny("manual work or invalid registration")
	}
	before, after := *plan.Registered, plan.Desired
	if before.Key != after.Key {
		return deny("key")
	}
	data := map[string]vectordata.DataProperty{}
	for _, p := range after.Data {
		data[p.Name] = p
	}
	for _, old := range before.Data {
		next, exists := data[old.Name]
		if !exists {
			return deny(old.Name)
		}
		delete(data, old.Name)
		if old.IsIndexed && !next.IsIndexed || old.IsFullTextIndexed && !next.IsFullTextIndexed {
			return deny(old.Name)
		}
		old.IsIndexed = false
		next.IsIndexed = false
		old.IsFullTextIndexed = false
		next.IsFullTextIndexed = false
		if old != next {
			return deny(old.Name)
		}
	}
	for _, p := range data {
		if !p.Optional {
			return deny(p.Name)
		}
	}
	vectors := map[string]vectordata.VectorProperty{}
	for _, p := range after.Vectors {
		vectors[p.Name] = p
	}

	for _, old := range before.Vectors {
		next, exists := vectors[old.Name]
		delete(vectors, old.Name)
		if !exists {
			return deny(old.Name)
		}
		if old.Index != nil && (next.Index == nil || *old.Index != *next.Index) {
			return deny(old.Name + " index")
		}
		old.Index = nil
		next.Index = nil
		old.Embedder = nil
		next.Embedder = nil
		if old != next {
			return deny(old.Name)
		}
	}
	for _, p := range vectors {
		if !p.Optional {
			return deny(p.Name)
		}
	}
	for _, step := range plan.Steps {
		if step.Manual || step.DataLoss || step.RequiresBackfill || step.RequiresReembedding {
			return deny(step.Property)
		}
		if step.Kind != vectordata.MigrationAddProperty && step.Kind != vectordata.MigrationCreateIndex {
			return deny(step.Property)
		}
	}
	return nil
}
