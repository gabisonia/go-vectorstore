package vectordata

import "context"

// MigrationPlanner is an optional, plan-only model capability. Planning never
// executes returned SQL or changes collection data, schema, indexes or registry.
type MigrationPlanner interface {
	PlanMigration(context.Context, string, CollectionDefinition) (MigrationPlan, error)
}

// MigrationApplier optionally applies reviewed additive model plans. Implementations
// must regenerate SQL, reject stale/non-additive reviews, and commit atomically.
type MigrationApplier interface {
	ApplyMigration(context.Context, MigrationPlan) error
}

type MigrationKind string

const (
	MigrationPrerequisite     MigrationKind = "prerequisite"
	MigrationAddProperty      MigrationKind = "add_property"
	MigrationRemoveProperty   MigrationKind = "remove_property"
	MigrationRenameProperty   MigrationKind = "rename_property"
	MigrationAlterProperty    MigrationKind = "alter_property"
	MigrationBackfill         MigrationKind = "backfill"
	MigrationReembed          MigrationKind = "reembed"
	MigrationCreateIndex      MigrationKind = "create_index"
	MigrationDropIndex        MigrationKind = "drop_index"
	MigrationUpdateDefinition MigrationKind = "update_definition"
)

// MigrationStep contains reviewable provider SQL and its bound arguments. Manual
// steps need application-specific work; an empty SQL field can be a metadata-only
// change or manual work, as indicated by Manual and Description. ExpectedRows is
// zero when unchecked, and one for registry writes requiring a matched snapshot.
type MigrationStep struct {
	Kind                                                    MigrationKind
	Property, StorageName, Description                      string
	SQL                                                     string
	Args                                                    []any
	ExpectedRows                                            int64
	Manual, DataLoss, RequiresBackfill, RequiresReembedding bool
}

// MigrationIssue blocks generated SQL, for example when live storage has drifted
// from its registered definition. Planning reports these rather than guessing a repair.
type MigrationIssue struct {
	Kind, Property, Description string
}

// MigrationPlan is a point-in-time report, not an execution or locking guarantee.
// Steps are ordered, use provider SQL, and must be reviewed before explicit use.
// Issues suppress all SQL; finalization stays separate from storage/manual steps.
type MigrationPlan struct {
	Backend, Namespace, Collection string
	// Revision binds a review to provider storage identity and registration state.
	// It is an opaque staleness token, not an authorization signature.
	Revision      string
	StorageExists bool
	Registered    *CollectionDefinition
	Desired       CollectionDefinition
	Issues        []MigrationIssue
	Steps         []MigrationStep
	// Finalization updates the registry only after every preceding step is complete.
	// Its Manual flag mirrors unresolved manual work; issues leave it nil.
	Finalization *MigrationStep
}

func (p MigrationPlan) HasManualWork() bool {
	if len(p.Issues) > 0 {
		return true
	}
	for _, step := range p.Steps {
		if step.Manual {
			return true
		}
	}
	return p.Finalization != nil && p.Finalization.Manual
}
