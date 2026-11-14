# Model migrations

PostgreSQL implements the optional `vectordata.MigrationPlanner` and
`vectordata.MigrationApplier` interfaces, reporting `MigrationPlanning=true` and
`MigrationApplication=true`. Memory reports both false. Existing `ModelStore` and
collection interfaces do not require migration support.

`PlanMigration(ctx, name, desired)` compares the registered model with live
storage and the desired normalized definition. It returns a report and SQL; it
does not execute migrations, backfill records, generate embeddings, or modify the
registry. Planning uses a single repeatable-read, read-only transaction and works
with catalog access, schema usage and registry SELECT permission, without target
record SELECT or DDL permission. PostgreSQL's
[read-only transaction rules](https://www.postgresql.org/docs/16/sql-set-transaction.html)
protect the inspection transaction from schema/data writes.

## Inspecting a plan

```go
// currentDefinition describes the existing registered products model.
desired := currentDefinition.Clone()
desired.Data = append(desired.Data, vectordata.DataProperty{
    Name: "Category", Type: vectordata.TypeString, Optional: true, IsIndexed: true,
})
plan, err := pgStore.PlanMigration(ctx, "products", desired)
if err != nil {
    return err
}
for _, issue := range plan.Issues {
    fmt.Printf("%s: %s\n", issue.Kind, issue.Description)
}
for _, step := range plan.Steps {
    fmt.Printf("%s: %s (manual=%t, dataLoss=%t, backfill=%t, reembed=%t)\n",
        step.Kind, step.Description, step.Manual, step.DataLoss,
        step.RequiresBackfill, step.RequiresReembedding)
    fmt.Printf("SQL: %s\nArgs: %#v\n", step.SQL, step.Args)
}
if final := plan.Finalization; final != nil {
    fmt.Printf("Finalization after all work: %s\nArgs: %#v\n", final.SQL, final.Args)
}
```

For a typed model, pass the result of `vectordata.DefinitionFor[ProductV2]()` as
`desired`. Through a `ModelStore` interface, use a type assertion to
`vectordata.MigrationPlanner`; unavailable metadata or interfaces do not imply
migration support in a third-party provider.

The plan includes `Backend`, `Namespace`, `Collection`, `Revision`, `StorageExists`, the
registered/desired definitions, ordered `Steps`, blocking `Issues`, and a separate
registry `Finalization`. Runtime embedder instances are omitted from plan
snapshots and metadata SQL. Mutating a returned plan does not configure the store.
Bound SQL arguments remain separate; do not interpolate them into SQL strings.

## Applying additive changes

After reviewing the plan above, apply it explicitly:

```go
if err := pgStore.ApplyMigration(ctx, plan); err != nil {
    return err
}
// Obtain a collection handle for the new definition. Existing handles keep their
// original definitions; they do not acquire new fields automatically.
products, err := pgStore.GetModelCollection("products", desired, vectordata.ModelCollectionOptions{})
if err != nil {
    return err
}
_ = products
```

Through `ModelStore`, check `MigrationApplication` and assert
`vectordata.MigrationApplier`. Application accepts optional scalar/JSON/array data
additions and new data, full-text or vector indexes. Existing property names,
storage mappings, types, nullability, keys and vector settings must stay unchanged.
Removing/rebuilding indexes, renames, required fields, new vectors, conversions,
backfills and re-embedding require a separate migration procedure. A plan with
`HasManualWork()==false` can still fall outside this additive subset.

Pass the complete, unedited plan from this provider version and database. JSON
round-trips are supported; runtime embedders are excluded. Application regenerates
the plan and compares the entire review, including its SQL and risk flags. It
executes only regenerated SQL. `Revision` binds the review to database/schema/table
identities and the registered definition; it is a staleness token, not an approval
signature. Plans from older versions without a revision must be regenerated.

Application uses one transaction and connection. It serializes with collection
lifecycle operations, locks the target table and registry row, then revalidates
live storage and registration before executing any migration SQL. It validates the
desired storage and final registration before commit. DDL, indexes and registration
roll back together on errors or cancellation. The target table uses an exclusive
lock and ordinary index builds, so application blocks reads and writes until the
transaction ends; budget a maintenance window and context deadline accordingly.
See PostgreSQL's [explicit locking documentation](https://www.postgresql.org/docs/16/explicit-locking.html).
The connection needs table ownership/DDL rights and registry SELECT/UPDATE rights,
plus privileges required by the generated statements and row locks.

Use `errors.Is(err, vectordata.ErrMigrationConflict)` for edited/stale reviews or
drift, and `errors.Is(err, vectordata.ErrMigrationNotAdditive)` for unsupported
changes on otherwise valid plans. Drift can also match `ErrSchemaMismatch`;
database errors remain available through `errors.As`. After a successful apply,
the old review is stale. Replan after conflicts or an uncertain commit outcome;
a freshly reviewed no-op plan can be applied successfully.

## Supported changes and classifications

| Change | Planned behavior |
| --- | --- |
| Add optional scalar/JSON/array data | Nullable `ADD COLUMN`, then requested indexes |
| Add required data | Nullable `ADD COLUMN`, manual backfill/verification, then `SET NOT NULL` |
| Add a vector | Same stages, with embedding/backfill flags |
| Relax nullability | `DROP NOT NULL` |
| Require a nullable property | Manual null backfill/verification, then `SET NOT NULL` |
| Rename storage with a stable logical property | `RENAME COLUMN`; rebuild affected managed index names |
| Rename a logical property with stable storage | Metadata change; stored values remain in place |
| Remove a property | `DROP COLUMN` with `DataLoss=true`, without CASCADE |
| Add/remove/change managed indexes | Ordered DROP/CREATE; matching existing requested indexes can be adopted |
| Change a native type or property role | Manual conversion with backfill/data-loss flags; no guessed USING cast |
| Change vector dimensions | Manual vector replacement with backfill/re-embedding/data-loss flags |
| Change vector precision | Manual, potentially lossy storage conversion with backfill/re-embedding flags; rejected by additive application |
| Change embedding source | Manual embedding regeneration or compatibility verification |
| Change distance metric | Manual validation of existing vectors/search semantics, plus index changes |
| Change identity generation | Manual sequence ownership/privilege/alignment review |

Backfill requirements are conservative even for empty tables: the planner never
scans user records to decide whether a backfill can be skipped. Runtime embedding
model identities are not persisted, so changing an embedder implementation alone
cannot be detected. Applications must manage that embedding compatibility.

A rename needs a stable logical name or storage column. Changing both is treated
as removal/addition. Occupied target names and rename cycles require manual work.
The planner preserves indexes outside the registered model unless an explicitly
removed column makes PostgreSQL remove dependent indexes/constraints.

`HasManualWork()` reports issues and manual steps/finalization. False does not mean
no data loss: inspect `DataLoss` and every SQL statement independently. Empty SQL
can describe a metadata-only mapping change or manual work; consult `Manual` and
`Description`.

## Drift and scope

Planning starts from an existing, valid registration on an ordinary table. Use
`EnsureModelCollection` for collection creation; explicit review is required for
unregistered storage adoption, stale/missing tables, views, inheritance and
partitioned tables. Planning a missing namespace does not create it.

Inspection checks column names/native types, nullability, identity/generated
attributes, defaults, primary-key shape, and registered index shape/options.
A modified registry shape also blocks inspection before reading its definitions.
Unexpected columns, missing/altered managed indexes, invalid/future registration
fields and occupied desired index names produce blocking `Issues`. Blocked plans
clear all step SQL/arguments and omit finalization, while retaining applicable
change descriptions. They do not guess a repair of out-of-band storage changes.

Other database dependencies, custom constraints, triggers, permissions, table
contents and deployment limits need separate review. Generated index SQL uses
ordinary blocking builds; online/concurrent migration design is outside this API.

## Registry finalization and manual migrations

Storage and application work must finish before updating the registered model.
`Finalization` is separate from `Steps` and is marked manual whenever preceding
work is unresolved. Its parameterized UPDATE compares the captured registration
with the current JSONB value, and `ExpectedRows=1` requires one affected row. Zero
rows means the registration changed or disappeared; stop and reassess the plan.
Known older JSON representations can yield metadata-only finalization even when
normalization produces the same logical model.

Planning does not reserve locks or promise that later execution sees the same
schema. For changes outside `ApplyMigration`'s additive subset, revalidate live
storage and registration, coordinate with other schema writers, use an appropriate
transaction/deployment strategy, check finalization's affected row count, and
validate the final model with `EnsureModelCollection`. A manual
multi-stage migration can produce intermediate drift until its storage work and
registry finalization are completed.

Integration tests use disposable fixtures on PostgreSQL 16/18. They verify planning
classifications and read-only privileges, additive application with native keys,
data preservation, quoting, rejection of unsupported/edited/stale plans, concurrent
reviews, cancellation and rollback when finalization fails or rewrites metadata.
