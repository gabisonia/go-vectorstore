package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

var _ vectordata.MigrationPlanner = (*PostgresVectorStore)(nil)

// PlanMigration inspects a repeatable-read, read-only snapshot. It does not call
// EnsureExists, take migration locks, scan user records, or execute generated SQL.
func (s *PostgresVectorStore) PlanMigration(ctx context.Context, name string, desired vectordata.CollectionDefinition) (plan vectordata.MigrationPlan, err error) {
	defer func() {
		if err != nil {
			err = &vectordata.OperationError{Backend: "postgres", Collection: name, Operation: "plan_migration", Err: err}
		}
	}()
	handle, err := s.GetModelCollection(name, desired, vectordata.ModelCollectionOptions{})
	if err != nil {
		return plan, err
	}
	c := handle.(*ModelCollection)
	indexes, err := modelIndexSpecs(name, c.definition)
	if err != nil {
		return plan, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return plan, err
	}
	defer rollbackTx(ctx, tx)
	plan, err = c.planMigrationTx(ctx, tx, indexes)
	if err != nil {
		return plan, err
	}
	if err := tx.Commit(ctx); err != nil {
		return plan, err
	}
	return plan, nil
}

// planMigrationTx never begins/commits or acquires a second connection.
func (c *ModelCollection) planMigrationTx(ctx context.Context, tx pgx.Tx, indexes []modelIndexSpec) (plan vectordata.MigrationPlan, err error) {
	s, name := c.store, c.name
	plan = vectordata.MigrationPlan{Backend: "postgres", Namespace: s.opts.Schema, Collection: name, Desired: definitionForPlan(c.definition)}
	var schema, registry, vector bool
	var kind, database, databaseID, namespaceID, storageID, registryID string
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname=$1),
 to_regclass($2) IS NOT NULL, to_regtype('vector') IS NOT NULL,
 coalesce((SELECT relkind::text FROM pg_class WHERE oid=to_regclass($3)),''),
 current_database(),(SELECT oid::text FROM pg_database WHERE datname=current_database()),
 coalesce((SELECT oid::text FROM pg_namespace WHERE nspname=$1),''),
 coalesce(to_regclass($3)::oid::text,''),coalesce(to_regclass($2)::oid::text,'')`, s.opts.Schema, qualifiedTable(s.opts.Schema, modelRegistry), c.table()).Scan(&schema, &registry, &vector, &kind, &database, &databaseID, &namespaceID, &storageID, &registryID)
	if err != nil {
		return plan, err
	}
	plan.StorageExists = kind != ""
	var registeredJSON string
	if registry {
		var valid bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (
   SELECT 1 FROM pg_class c WHERE c.oid=to_regclass($1) AND c.relkind='r'
   AND NOT EXISTS (SELECT 1 FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
   AND (SELECT count(*) FROM pg_attribute WHERE attrelid=c.oid AND attnum>0 AND NOT attisdropped)=2
   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=c.oid AND attname='name' AND atttypid='text'::regtype AND attnotnull AND attgenerated='' AND attidentity='')
   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=c.oid AND attname='definition' AND atttypid='jsonb'::regtype AND attnotnull AND attgenerated='' AND attidentity='')
   AND EXISTS (SELECT 1 FROM pg_index i JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=i.indkey[0]
    WHERE i.indrelid=c.oid AND a.attname='name' AND i.indisprimary AND i.indisvalid AND i.indisready AND i.indimmediate AND i.indnatts=1)
  )`, qualifiedTable(s.opts.Schema, modelRegistry)).Scan(&valid)
		if err != nil {
			return plan, err
		}
		if !valid {
			registry = false
			plan.Issues = append(plan.Issues, vectordata.MigrationIssue{Kind: "invalid_registry", Description: "reserved registry schema differs from the expected ordinary table, key and JSONB definition columns"})
		}
	}
	if registry {
		err = tx.QueryRow(ctx, "SELECT definition::text FROM "+qualifiedTable(s.opts.Schema, modelRegistry)+" WHERE name=$1", name).Scan(&registeredJSON)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return plan, err
		}
		if err == nil {
			var d vectordata.CollectionDefinition
			decoder := json.NewDecoder(strings.NewReader(registeredJSON))
			decoder.DisallowUnknownFields()
			decodeErr := decoder.Decode(&d)
			if decodeErr == nil {
				d, decodeErr = d.Normalize()
			}
			if decodeErr != nil {
				plan.Issues = append(plan.Issues, vectordata.MigrationIssue{Kind: "invalid_registration", Description: fmt.Sprintf("registered definition is invalid: %v", decodeErr)})
			} else {
				d = definitionForPlan(d)
				plan.Registered = &d
			}
		}
	}
	identity, _ := json.Marshal([]string{database, databaseID, namespaceID, storageID, registryID, registeredJSON})
	plan.Revision = fmt.Sprintf("%x", sha256.Sum256(identity))
	if kind != "" && kind != "r" {
		plan.Issues = append(plan.Issues, vectordata.MigrationIssue{Kind: "unsupported_relation", Description: "migration planning supports ordinary model tables; views and partitioned/inherited tables need manual migration design"})
	}
	if kind == "r" {
		var inherited bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_inherits WHERE inhrelid=to_regclass($1) OR inhparent=to_regclass($1))", c.table()).Scan(&inherited); err != nil {
			return plan, err
		}
		if inherited {
			plan.Issues = append(plan.Issues, vectordata.MigrationIssue{Kind: "unsupported_relation", Description: "inherited/partitioned tables need manual migration design"})
		}
	}
	if plan.StorageExists && plan.Registered == nil {
		plan.Issues = append(plan.Issues, vectordata.MigrationIssue{Kind: "unregistered_collection", Description: "existing storage has no valid registered model; review adoption explicitly"})
	}
	if !plan.StorageExists && plan.Registered != nil {
		plan.Issues = append(plan.Issues, vectordata.MigrationIssue{Kind: "missing_collection", Description: "registered storage is missing; review data recovery before recreating it"})
	}
	if !plan.StorageExists && plan.Registered == nil {
		description := "collection is missing; create it with EnsureModelCollection before planning model changes"
		if !schema {
			description = "configured namespace is missing; create the collection with EnsureModelCollection before planning model changes"
		}
		plan.Issues = append(plan.Issues, vectordata.MigrationIssue{Kind: "missing_collection", Description: description})
	}
	if len(plan.Issues) == 0 {
		drift, err := c.migrationDrift(ctx, tx, *plan.Registered)
		if err != nil {
			return plan, err
		}
		plan.Issues = append(plan.Issues, drift...)
		plan.Steps = append(plan.Steps, c.propertyMigrationSteps(*plan.Registered)...)
		indexSteps, issues, err := c.indexMigrationSteps(ctx, tx, plan.Registered, indexes)
		if err != nil {
			return plan, err
		}
		plan.Issues = append(plan.Issues, issues...)
		// Old managed indexes must be removed before renames/drops, new ones after.
		var drops, creates []vectordata.MigrationStep
		for _, step := range indexSteps {
			if step.Kind == vectordata.MigrationDropIndex {
				drops = append(drops, step)
			} else {
				creates = append(creates, step)
			}
		}
		plan.Steps = append(drops, plan.Steps...)
		plan.Steps = append(plan.Steps, creates...)
	}
	if len(c.definition.Vectors) > 0 && !vector {
		plan.Steps = append([]vectordata.MigrationStep{{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "install pgvector and make the vector type visible in the migration search_path"}}, plan.Steps...)
	}
	if len(plan.Issues) > 0 {
		// Desired deltas remain visible as descriptions, without SQL against drifted storage.
		for i := range plan.Steps {
			plan.Steps[i].SQL = ""
			plan.Steps[i].Args = nil
			plan.Steps[i].Manual = true
		}
	}
	if len(plan.Issues) == 0 {
		payload, err := definitionJSON(c.definition)
		if err != nil {
			return plan, err
		}
		var same bool
		if err := tx.QueryRow(ctx, "SELECT $1::jsonb=$2::jsonb", registeredJSON, string(payload)).Scan(&same); err != nil {
			return plan, err
		}
		if !same {
			plan.Finalization = &vectordata.MigrationStep{Kind: vectordata.MigrationUpdateDefinition, Description: "register the desired model only after every storage/manual step is complete; require one affected row", ExpectedRows: 1, Manual: plan.HasManualWork(), SQL: "UPDATE " + qualifiedTable(s.opts.Schema, modelRegistry) + " SET definition=$2::jsonb WHERE name=$1 AND definition=$3::jsonb", Args: []any{name, string(payload), registeredJSON}}
		}
	}
	return plan, nil
}
func definitionForPlan(d vectordata.CollectionDefinition) vectordata.CollectionDefinition {
	d = d.Clone()
	for i := range d.Vectors {
		d.Vectors[i].Embedder = nil
	}
	return d
}

type migrationColumn struct {
	typ, identity, generated, defaultExpr string
	required                              bool
}

func definitionColumns(d vectordata.CollectionDefinition) map[string]migrationColumn {
	identity := ""
	if d.Key.AutoGenerated {
		identity = "d"
	}
	columns := map[string]migrationColumn{d.Key.StorageName: {typ: keySQLType(d.Key.Type), required: true, identity: identity}}
	for _, p := range d.Data {
		columns[p.StorageName] = migrationColumn{typ: dataSQLType(p.Type), required: !p.Optional}
	}
	for _, p := range d.Vectors {
		columns[p.StorageName] = migrationColumn{typ: fmt.Sprintf("vector(%d)", p.Dimensions), required: true}
	}
	return columns
}
func (c *ModelCollection) migrationDrift(ctx context.Context, tx pgx.Tx, registered vectordata.CollectionDefinition) ([]vectordata.MigrationIssue, error) {
	rows, err := tx.Query(ctx, `SELECT a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity::text,a.attgenerated::text,coalesce(pg_get_expr(d.adbin,d.adrelid),'')
 FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, c.table())
	if err != nil {
		return nil, err
	}
	actual := map[string]migrationColumn{}
	for rows.Next() {
		var name string
		var col migrationColumn
		if err := rows.Scan(&name, &col.typ, &col.required, &col.identity, &col.generated, &col.defaultExpr); err != nil {
			rows.Close()
			return nil, err
		}
		actual[name] = col
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	expected := definitionColumns(registered)
	names := map[string]bool{}
	for name := range expected {
		names[name] = true
	}
	for name := range actual {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	var issues []vectordata.MigrationIssue
	for _, name := range ordered {
		want, wanted := expected[name]
		got, exists := actual[name]
		if !wanted {
			issues = append(issues, vectordata.MigrationIssue{Kind: "schema_drift", Property: name, Description: "unregistered live column"})
			continue
		}
		if !exists {
			issues = append(issues, vectordata.MigrationIssue{Kind: "schema_drift", Property: name, Description: "registered column is missing"})
			continue
		}
		if got != want {
			issues = append(issues, vectordata.MigrationIssue{Kind: "schema_drift", Property: name, Description: fmt.Sprintf("live column differs from registration: expected %+v, observed %+v", want, got)})
		}
	}
	current := &ModelCollection{store: c.store, name: c.name, definition: registered}
	// Includes the original strict primary-key check, without changing storage.
	if len(issues) == 0 {
		if err := current.validateSchema(ctx, tx); err != nil {
			if errors.Is(err, vectordata.ErrSchemaMismatch) {
				issues = append(issues, vectordata.MigrationIssue{Kind: "schema_drift", Description: err.Error()})
			} else {
				return nil, err
			}
		}
	}
	indexes, err := modelIndexSpecs(c.name, registered)
	if err != nil {
		return nil, err
	}
	for _, index := range indexes {
		err := current.validateMigrationIndex(ctx, tx, index)
		if err != nil {
			if errors.Is(err, vectordata.ErrSchemaMismatch) {
				issues = append(issues, vectordata.MigrationIssue{Kind: "index_drift", Property: index.property, Description: err.Error()})
			} else {
				return nil, err
			}
		}
	}
	return issues, nil
}

func samePhysicalIndex(a, b modelIndexSpec) bool {
	return a.name == b.name && a.method == b.method && a.column == b.column && a.opClass == b.opClass && a.with == b.with && a.fullText == b.fullText
}
func (c *ModelCollection) indexMigrationSteps(ctx context.Context, tx pgx.Tx, registered *vectordata.CollectionDefinition, next []modelIndexSpec) (steps []vectordata.MigrationStep, issues []vectordata.MigrationIssue, err error) {
	old := map[string]modelIndexSpec{}
	desired := map[string]modelIndexSpec{}
	if registered != nil {
		indexes, err := modelIndexSpecs(c.name, *registered)
		if err != nil {
			return nil, nil, err
		}
		for _, index := range indexes {
			old[index.name] = index
		}
	}
	for _, index := range next {
		desired[index.name] = index
	}
	names := make([]string, 0, len(old))
	for name := range old {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		index := old[name]
		want, keep := desired[name]
		if !keep || !samePhysicalIndex(index, want) {
			steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationDropIndex, Property: index.property, StorageName: index.name, Description: "drop the old managed index", SQL: "DROP INDEX " + qualifiedTable(c.store.opts.Schema, index.name)})
		}
	}
	sort.Slice(next, func(i, j int) bool { return next[i].name < next[j].name })
	for _, index := range next {
		prior, managed := old[index.name]
		if managed && samePhysicalIndex(prior, index) {
			continue
		}
		if !managed {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualifiedTable(c.store.opts.Schema, index.name)).Scan(&exists); err != nil {
				return nil, nil, err
			}
			if exists {
				if err := c.validateMigrationIndex(ctx, tx, index); err != nil {
					if errors.Is(err, vectordata.ErrSchemaMismatch) {
						issues = append(issues, vectordata.MigrationIssue{Kind: "index_conflict", Property: index.property, Description: err.Error()})
						continue
					}
					return nil, nil, err
				}
				continue // A matching physical index can be adopted without rebuilding it.
			}
		}
		steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationCreateIndex, Property: index.property, StorageName: index.name, Description: "create the desired managed index", SQL: index.createSQL(c)})
	}
	return steps, issues, nil
}

// Migration planning must not treat uniqueness constraints as disposable indexes.
// Keep this stricter ownership check separate from existing EnsureExists behavior.
func (c *ModelCollection) validateMigrationIndex(ctx context.Context, tx pgx.Tx, index modelIndexSpec) error {
	if err := c.validateModelIndex(ctx, tx, index.name, index.method, index.column, index.opClass, index.with, index.fullText); err != nil {
		return err
	}
	var protected bool
	if err := tx.QueryRow(ctx, "SELECT indisunique OR indisprimary OR indisexclusion FROM pg_index WHERE indexrelid=to_regclass($1)", qualifiedTable(c.store.opts.Schema, index.name)).Scan(&protected); err != nil {
		return err
	}
	if protected {
		return fmt.Errorf("%w: managed index %q enforces an unmodeled constraint", vectordata.ErrSchemaMismatch, index.name)
	}
	return nil
}
