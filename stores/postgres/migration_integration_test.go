//go:build integration

package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func migrationFixture(t *testing.T) (*PostgresVectorStore, *ModelCollection, vectordata.CollectionDefinition) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", StorageName: "record_id"}, Data: []vectordata.DataProperty{{Name: "Title", StorageName: "title", Type: vectordata.TypeString, IsIndexed: true, IsFullTextIndexed: true}, {Name: "Rank", Type: vectordata.TypeInt64, Optional: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", StorageName: "embedding", Dimensions: 3, SourceProperty: "Title", Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}}}
	base, err := store.EnsureModelCollection(ctx, "migration_models", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*ModelCollection)
	d = c.Definition()
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Title": "first", "Rank": int64(1)}, Vectors: map[string][]float32{"Vector": {1, 0, 0}}}, {Key: "b", Data: map[string]any{"Title": "second", "Rank": nil}, Vectors: map[string][]float32{"Vector": {0, 1, 0}}}}); err != nil {
		t.Fatal(err)
	}
	return store, c, d
}
func hasMigrationStep(plan vectordata.MigrationPlan, kind vectordata.MigrationKind) bool {
	for _, step := range plan.Steps {
		if step.Kind == kind {
			return true
		}
	}
	return false
}
func assertBlockedMigration(t *testing.T, plan vectordata.MigrationPlan) {
	t.Helper()
	if len(plan.Issues) == 0 || !plan.HasManualWork() || plan.Finalization != nil {
		t.Fatalf("expected blocked plan: %+v", plan)
	}
	for _, step := range plan.Steps {
		if step.SQL != "" || len(step.Args) != 0 {
			t.Fatalf("blocked plan emitted SQL: %+v", step)
		}
	}
}

// This executor exists only for disposable integration fixtures. Public API plans
// never execute, and application-specific manual work must be supplied explicitly.
func applyMigrationFixture(t *testing.T, store *PostgresVectorStore, plan vectordata.MigrationPlan, manual func(pgx.Tx, vectordata.MigrationStep)) {
	t.Helper()
	if len(plan.Issues) > 0 || (plan.HasManualWork() && manual == nil) {
		t.Fatalf("cannot apply unresolved fixture plan: %+v", plan)
	}
	ctx := context.Background()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTx(ctx, tx)
	for _, step := range plan.Steps {
		if step.Manual {
			manual(tx, step)
		}
		if step.SQL != "" {
			if _, err := tx.Exec(ctx, step.SQL, step.Args...); err != nil {
				t.Fatalf("%s: %v", step.SQL, err)
			}
		}
	}
	if step := plan.Finalization; step != nil {
		tag, err := tx.Exec(ctx, step.SQL, step.Args...)
		if err != nil || tag.RowsAffected() != step.ExpectedRows {
			t.Fatalf("registry finalization: rows=%d, error=%v", tag.RowsAffected(), err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestIntegrationMigrationNoopAndAdditiveSQL(t *testing.T) {
	store, c, d := migrationFixture(t)
	ctx := context.Background()
	calls := 0
	d.Vectors[0].Embedder = vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		calls++
		return nil, errors.New("must not embed while planning")
	})
	plan, err := store.PlanMigration(ctx, c.name, d)
	if err != nil || plan.HasManualWork() || len(plan.Steps) != 0 || plan.Finalization != nil || calls != 0 {
		t.Fatalf("no-op plan: %+v, calls=%d, error=%v", plan, calls, err)
	}
	if d.Vectors[0].Embedder == nil || plan.Desired.Vectors[0].Embedder != nil {
		t.Fatal("runtime embedder ownership/serialization")
	}
	if _, err := store.pool.Exec(ctx, "CREATE TABLE "+qualifiedTable(store.opts.Schema, "sentinel")+" (id int)"); err != nil {
		t.Fatal(err)
	}
	next := d.Clone()
	next.Data = append(next.Data, vectordata.DataProperty{Name: "Score", StorageName: `score"; DROP TABLE sentinel;--`, Type: vectordata.TypeInt64, Optional: true, IsIndexed: true})
	plan, err = store.PlanMigration(ctx, c.name, next)
	if err != nil || plan.HasManualWork() || !hasMigrationStep(plan, vectordata.MigrationAddProperty) || !hasMigrationStep(plan, vectordata.MigrationCreateIndex) || plan.Finalization == nil {
		t.Fatalf("add plan: %+v, %v", plan, err)
	}
	repeated, err := store.PlanMigration(ctx, c.name, next)
	if err != nil || !reflect.DeepEqual(plan, repeated) {
		t.Fatalf("planning must be deterministic: %+v, %v", repeated, err)
	}
	// Planning has not created columns or updated the registered model.
	if err := c.EnsureExists(ctx); err != nil {
		t.Fatalf("planning changed original schema/registry: %v", err)
	}
	applyMigrationFixture(t, store, plan, nil)
	if _, err := store.EnsureModelCollection(ctx, c.name, next, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatalf("generated migration does not match desired schema: %v", err)
	}
	var sentinel bool
	if err := store.pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualifiedTable(store.opts.Schema, "sentinel")).Scan(&sentinel); err != nil || !sentinel {
		t.Fatalf("identifier escaping failed: %v", err)
	}
	noop, err := store.PlanMigration(ctx, c.name, next)
	if err != nil || len(noop.Steps) != 0 || noop.Finalization != nil {
		t.Fatalf("post-migration plan: %+v, %v", noop, err)
	}
}
func TestIntegrationMigrationRenameIndexesAndRemoval(t *testing.T) {
	store, c, d := migrationFixture(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, "CREATE INDEX extra_unmanaged ON "+c.table()+" (\"Rank\")"); err != nil {
		t.Fatal(err)
	}
	next := d.Clone()
	next.Data[0].StorageName = "heading"
	plan, err := store.PlanMigration(ctx, c.name, next)
	if err != nil || plan.HasManualWork() || !hasMigrationStep(plan, vectordata.MigrationRenameProperty) {
		t.Fatalf("rename: %+v, %v", plan, err)
	}
	applyMigrationFixture(t, store, plan, nil)
	if _, err := store.EnsureModelCollection(ctx, c.name, next, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := c.store.GetModelCollection(c.name, next, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if row, err := got.Get(ctx, "a", vectordata.RetrievalOptions{}); err != nil || row.Data["Title"] != "first" {
		t.Fatalf("rename lost data: %+v, %v", row, err)
	}
	logical := next.Clone()
	logical.Data[0].Name = "Heading"
	logical.Vectors[0].SourceProperty = "Heading"
	plan, err = store.PlanMigration(ctx, c.name, logical)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("logical rename: %+v, %v", plan, err)
	}
	for _, step := range plan.Steps {
		if step.SQL != "" || step.RequiresReembedding {
			t.Fatalf("logical mapping changed storage/embeddings: %+v", step)
		}
	}
	applyMigrationFixture(t, store, plan, nil)
	indexed := logical.Clone()
	indexed.Vectors[0].Index.HNSW.M = 20
	indexed.Vectors[0].Index.HNSW.EfConstruction = 80
	plan, err = store.PlanMigration(ctx, c.name, indexed)
	if err != nil || plan.HasManualWork() || !hasMigrationStep(plan, vectordata.MigrationDropIndex) || !hasMigrationStep(plan, vectordata.MigrationCreateIndex) {
		t.Fatalf("index options: %+v, %v", plan, err)
	}
	applyMigrationFixture(t, store, plan, nil)
	if _, err := store.EnsureModelCollection(ctx, c.name, indexed, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	var extra bool
	if err := store.pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualifiedTable(store.opts.Schema, "extra_unmanaged")).Scan(&extra); err != nil || !extra {
		t.Fatalf("unmanaged index was changed: %v", err)
	}
	removed := indexed.Clone()
	removed.Data = removed.Data[:1]
	plan, err = store.PlanMigration(ctx, c.name, removed)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("removal: %+v, %v", plan, err)
	}
	loss := false
	for _, step := range plan.Steps {
		loss = loss || step.DataLoss
		if strings.Contains(step.SQL, "CASCADE") {
			t.Fatal("planner must not cascade deletions")
		}
	}
	if !loss {
		t.Fatal("column removal did not expose data loss")
	}
	applyMigrationFixture(t, store, plan, nil)
	if _, err := store.EnsureModelCollection(ctx, c.name, removed, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	// Removing its column necessarily removes a dependent unmanaged index; earlier
	// renames/index-only changes must leave it alone until this explicit data removal.
}
func TestIntegrationMigrationBackfillAndReembedding(t *testing.T) {
	store, c, d := migrationFixture(t)
	ctx := context.Background()
	next := d.Clone()
	next.Data[1].Optional = false
	plan, err := store.PlanMigration(ctx, c.name, next)
	if err != nil || !plan.HasManualWork() || plan.Finalization == nil || !plan.Finalization.Manual {
		t.Fatalf("nullability: %+v, %v", plan, err)
	}
	applyMigrationFixture(t, store, plan, func(tx pgx.Tx, step vectordata.MigrationStep) {
		if !step.RequiresBackfill {
			t.Fatalf("unexpected manual step: %+v", step)
		}
		if _, err := tx.Exec(ctx, "UPDATE "+c.table()+" SET \"Rank\"=0 WHERE \"Rank\" IS NULL"); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := store.EnsureModelCollection(ctx, c.name, next, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name      string
		change    func(*vectordata.CollectionDefinition)
		embedding bool
	}{
		{"dimension change", func(d *vectordata.CollectionDefinition) { d.Vectors[0].Dimensions = 4 }, true},
		{"new vector", func(d *vectordata.CollectionDefinition) {
			d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Image", Dimensions: 2})
		}, true},
		{"new required scalar", func(d *vectordata.CollectionDefinition) {
			d.Data = append(d.Data, vectordata.DataProperty{Name: "Required", Type: vectordata.TypeInt64})
		}, false},
		{"native type conversion", func(d *vectordata.CollectionDefinition) { d.Data[1].Type = vectordata.TypeString }, false},
		{"identity generation", func(d *vectordata.CollectionDefinition) { d.Key.Type = vectordata.KeyInt64; d.Key.AutoGenerated = true }, false},
		{"new embedding source", func(d *vectordata.CollectionDefinition) {
			d.Data = append(d.Data, vectordata.DataProperty{Name: "Body", Type: vectordata.TypeString, Optional: true})
			d.Vectors[0].SourceProperty = "Body"
		}, true},
		{"metric change", func(d *vectordata.CollectionDefinition) {
			d.Vectors[0].Metric = vectordata.DistanceL2
			d.Vectors[0].Index.Metric = vectordata.DistanceL2
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target := next.Clone()
			tt.change(&target)
			plan, err := store.PlanMigration(ctx, c.name, target)
			if err != nil || !plan.HasManualWork() || plan.Finalization == nil || !plan.Finalization.Manual {
				t.Fatalf("manual plan: %+v, %v", plan, err)
			}
			embedding := false
			for _, step := range plan.Steps {
				embedding = embedding || step.RequiresReembedding
			}
			if embedding != tt.embedding {
				t.Fatalf("embedding requirement=%v, want=%v", embedding, tt.embedding)
			}
		})
	}
	if _, err := store.EnsureModelCollection(ctx, c.name, next, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatalf("manual planning changed storage: %v", err)
	}
}
func TestIntegrationMigrationDriftAndConflicts(t *testing.T) {
	for _, tt := range []struct{ name, sql string }{
		{"extra column", `ALTER TABLE %s ADD extra text`},
		{"native type", `ALTER TABLE %s ALTER COLUMN "Rank" TYPE text USING "Rank"::text`},
		{"default expression", `ALTER TABLE %s ALTER COLUMN "Rank" SET DEFAULT 5`},
		{"primary key", `ALTER TABLE %s DROP CONSTRAINT migration_models_pkey`},
		{"missing managed index", `DROP INDEX %%SCHEMA%%.idx_migration_models_embedding_hnsw`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, c, d := migrationFixture(t)
			ctx := context.Background()
			sql := strings.ReplaceAll(tt.sql, "%%SCHEMA%%", quoteIdent(store.opts.Schema))
			sql = strings.ReplaceAll(sql, "%s", c.table())
			if _, err := store.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			next := d.Clone()
			next.Data = append(next.Data, vectordata.DataProperty{Name: "Added", Type: vectordata.TypeString, Optional: true})
			plan, err := store.PlanMigration(ctx, c.name, next)
			if err != nil {
				t.Fatal(err)
			}
			assertBlockedMigration(t, plan)
		})
	}
	t.Run("index collision", func(t *testing.T) {
		store, c, d := migrationFixture(t)
		ctx := context.Background()
		if _, err := store.pool.Exec(ctx, "CREATE TABLE "+qualifiedTable(store.opts.Schema, "occupied_index")+" (id int)"); err != nil {
			t.Fatal(err)
		}
		d.Vectors[0].Index.Name = "occupied_index"
		plan, err := store.PlanMigration(ctx, c.name, d)
		if err != nil {
			t.Fatal(err)
		}
		assertBlockedMigration(t, plan)
	})
	t.Run("missing and unregistered storage", func(t *testing.T) {
		store, c, d := migrationFixture(t)
		ctx := context.Background()
		for _, name := range []string{"missing", c.name} {
			if name == c.name {
				if _, err := store.pool.Exec(ctx, "DELETE FROM "+qualifiedTable(store.opts.Schema, modelRegistry)+" WHERE name=$1", name); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := store.PlanMigration(ctx, name, d)
			if err != nil {
				t.Fatal(err)
			}
			assertBlockedMigration(t, plan)
		}
	})
}

func TestIntegrationMigrationReadOnlyAndSingleConnection(t *testing.T) {
	store, c, d := migrationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	role := store.opts.Schema + "_planner"
	if _, err := store.pool.Exec(ctx, "CREATE ROLE "+quoteIdent(role)+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), "DROP OWNED BY "+quoteIdent(role))
		_, _ = store.pool.Exec(context.Background(), "DROP ROLE "+quoteIdent(role))
	})
	for _, sql := range []string{"GRANT USAGE ON SCHEMA " + quoteIdent(store.opts.Schema) + " TO " + quoteIdent(role), "GRANT SELECT ON " + qualifiedTable(store.opts.Schema, modelRegistry) + " TO " + quoteIdent(role)} {
		if _, err := store.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgxpool.ParseConfig(integrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+quoteIdent(role))
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	restricted, err := NewVectorStore(pool, store.opts)
	if err != nil {
		t.Fatal(err)
	}
	var dataAccess bool
	if err := pool.QueryRow(ctx, "SELECT has_table_privilege(current_user,$1,'SELECT')", c.table()).Scan(&dataAccess); err != nil || dataAccess {
		t.Fatalf("fixture must lack data access: %v, %v", dataAccess, err)
	}
	d.Data = append(d.Data, vectordata.DataProperty{Name: "Added", Type: vectordata.TypeString, Optional: true, IsIndexed: true})
	plan, err := restricted.PlanMigration(ctx, c.name, d)
	if err != nil || plan.HasManualWork() || plan.Finalization == nil {
		t.Fatalf("read-only plan: %+v, %v", plan, err)
	}
	if err := c.EnsureExists(ctx); err != nil {
		t.Fatalf("planning mutated storage/registry: %v", err)
	}
	// The read transaction released the only connection.
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	if _, err := restricted.PlanMigration(canceled, c.name, d); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("connection leaked after cancellation: %v", err)
	}
}
func TestIntegrationMigrationMissingNamespaceAndStaleFinalization(t *testing.T) {
	store, c, d := migrationFixture(t)
	ctx := context.Background()
	missing, err := NewVectorStore(store.pool, StoreOptions{Schema: store.opts.Schema + "_missing", EnsureExtension: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := missing.PlanMigration(ctx, c.name, d)
	if err != nil {
		t.Fatal(err)
	}
	assertBlockedMigration(t, plan)
	var created bool
	if err := store.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname=$1)", missing.opts.Schema).Scan(&created); err != nil || created {
		t.Fatalf("planning created namespace: %v, %v", created, err)
	}
	d.Data = append(d.Data, vectordata.DataProperty{Name: "Added", Type: vectordata.TypeString, Optional: true})
	plan, err = store.PlanMigration(ctx, c.name, d)
	if err != nil || plan.Finalization == nil {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTx(ctx, tx)
	if _, err := tx.Exec(ctx, "UPDATE "+qualifiedTable(store.opts.Schema, modelRegistry)+" SET definition=jsonb_set(definition,'{Key,Name}','\"Changed\"') WHERE name=$1", c.name); err != nil {
		t.Fatal(err)
	}
	step := plan.Finalization
	tag, err := tx.Exec(ctx, step.SQL, step.Args...)
	if err != nil || tag.RowsAffected() != 0 || step.ExpectedRows != 1 {
		t.Fatalf("stale registry guard: rows=%d, expected=%d, error=%v", tag.RowsAffected(), step.ExpectedRows, err)
	}
}

func TestIntegrationMigrationRegistryCompatibilityAndUnsupportedStorage(t *testing.T) {
	t.Run("canonical registry finalization", func(t *testing.T) {
		store, c, d := migrationFixture(t)
		ctx := context.Background()
		// A known field omitted by an older writer normalizes to the same model, but
		// strict registry equality still needs an explicit metadata update.
		payload, err := definitionJSON(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, "UPDATE "+qualifiedTable(store.opts.Schema, modelRegistry)+" SET definition=$2::jsonb #- '{Key,AutoGenerated}' WHERE name=$1", c.name, string(payload)); err != nil {
			t.Fatal(err)
		}
		plan, err := store.PlanMigration(ctx, c.name, d)
		if err != nil || plan.HasManualWork() || len(plan.Steps) != 0 || plan.Finalization == nil {
			t.Fatalf("metadata-only finalization: %+v, %v", plan, err)
		}
		applyMigrationFixture(t, store, plan, nil)
		if err := c.EnsureExists(ctx); err != nil {
			t.Fatal(err)
		}
	})
	for _, scenario := range []string{"invalid registry", "future registry fields", "missing registered table", "view", "inheritance", "managed unique index", "registry schema", "registry view"} {
		t.Run(scenario, func(t *testing.T) {
			store, c, d := migrationFixture(t)
			ctx := context.Background()
			registry := qualifiedTable(store.opts.Schema, modelRegistry)
			var sql string
			switch scenario {
			case "registry schema":
				sql = "ALTER TABLE " + registry + " ADD extra text"
			case "registry view":
				sql = "DROP TABLE " + registry + "; CREATE VIEW " + registry + " AS SELECT 'migration_models'::text AS name, '{}'::jsonb AS definition"
			case "invalid registry":
				sql = "UPDATE " + registry + " SET definition='[]'::jsonb"
			case "future registry fields":
				sql = "UPDATE " + registry + " SET definition=definition||'{\"FutureField\":true}'::jsonb"
			case "missing registered table":
				sql = "DROP TABLE " + c.table()
			case "view":
				sql = "DROP TABLE " + c.table() + "; CREATE VIEW " + c.table() + " AS SELECT 1 AS id"
			case "inheritance":
				sql = "CREATE TABLE " + qualifiedTable(store.opts.Schema, "inherited") + " () INHERITS (" + c.table() + ")"
			case "managed unique index":
				sql = "DROP INDEX " + qualifiedTable(store.opts.Schema, "idx_migration_models_title_data") + "; CREATE UNIQUE INDEX idx_migration_models_title_data ON " + c.table() + " (title)"
			}
			if _, err := store.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			plan, err := store.PlanMigration(ctx, c.name, d)
			if err != nil {
				t.Fatal(err)
			}
			assertBlockedMigration(t, plan)
		})
	}
}

func TestIntegrationMigrationKeyRenameAndIndexAdoption(t *testing.T) {
	store, c, d := migrationFixture(t)
	ctx := context.Background()
	next := d.Clone()
	next.Key.Name = "Key"
	next.Key.StorageName = "new_key"
	next.Data[0].Optional = true
	plan, err := store.PlanMigration(ctx, c.name, next)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("key rename: %+v, %v", plan, err)
	}
	applyMigrationFixture(t, store, plan, nil)
	base, err := store.EnsureModelCollection(ctx, c.name, next, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if row, err := base.Get(ctx, "a", vectordata.RetrievalOptions{}); err != nil || row.Key != "a" {
		t.Fatalf("renamed key: %+v, %v", row, err)
	}
	if _, err := store.pool.Exec(ctx, "CREATE INDEX adopted_hnsw ON "+c.table()+" USING hnsw (embedding vector_cosine_ops) WITH (m=16,ef_construction=64)"); err != nil {
		t.Fatal(err)
	}
	next.Vectors[0].Index.Name = "adopted_hnsw"
	plan, err = store.PlanMigration(ctx, c.name, next)
	if err != nil || plan.HasManualWork() || hasMigrationStep(plan, vectordata.MigrationCreateIndex) {
		t.Fatalf("adopt matching index: %+v, %v", plan, err)
	}
	applyMigrationFixture(t, store, plan, nil)
	if _, err := store.EnsureModelCollection(ctx, c.name, next, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	collided := next.Clone()
	collided.Data[0].StorageName = "Rank"
	collided.Data[1].StorageName = "title"
	plan, err = store.PlanMigration(ctx, c.name, collided)
	if err != nil || !plan.HasManualWork() || plan.Finalization == nil || !plan.Finalization.Manual {
		t.Fatalf("rename collision: %+v, %v", plan, err)
	}
	for _, step := range plan.Steps {
		if step.Kind == vectordata.MigrationRenameProperty && step.SQL != "" {
			t.Fatalf("colliding rename must be manual: %+v", step)
		}
	}
}
