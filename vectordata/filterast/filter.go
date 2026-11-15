// Package filterast defines portable filter nodes shared by providers.
package filterast

// FieldKind determines whether filter field references a fixed column or metadata JSON path.
type FieldKind string

const (
	FieldColumn   FieldKind = "column"
	FieldMetadata FieldKind = "metadata"
	FieldDataPath FieldKind = "data_path"
)

// FieldRef references a queryable field.
type FieldRef struct {
	Kind FieldKind
	Name string
	Path []string
}

// Column builds a fixed-column field reference.
func Column(name string) FieldRef {
	return FieldRef{Kind: FieldColumn, Name: name}
}

// Metadata builds a metadata JSON path field reference.
func Metadata(path ...string) FieldRef {
	cp := make([]string, len(path))
	copy(cp, path)
	return FieldRef{Kind: FieldMetadata, Path: cp}
}

// DataPath references a JSON data property and an optional nested path in a model collection.
func DataPath(property string, path ...string) FieldRef {
	return FieldRef{Kind: FieldDataPath, Name: property, Path: append([]string(nil), path...)}
}

type GteFilter struct {
	Field FieldRef
	Value any
}

func (GteFilter) isFilter() {}

type LteFilter struct {
	Field FieldRef
	Value any
}

func (LteFilter) isFilter() {}

type ContainsFilter struct {
	Field FieldRef
	Value any
}

func (ContainsFilter) isFilter()           {}
func Gte(field FieldRef, value any) Filter { return GteFilter{field, value} }
func Lte(field FieldRef, value any) Filter { return LteFilter{field, value} }
func Ne(field FieldRef, value any) Filter  { return Not(Eq(field, value)) }

// Contains tests array membership, not substring matching.
func Contains(field FieldRef, value any) Filter { return ContainsFilter{field, value} }

// Filter is the AST node interface.
type Filter interface {
	isFilter()
}

// EqFilter checks equality.
type EqFilter struct {
	Field FieldRef
	Value any
}

func (EqFilter) isFilter() {}

// InFilter checks membership.
type InFilter struct {
	Field  FieldRef
	Values []any
}

func (InFilter) isFilter() {}

// GtFilter checks greater-than.
type GtFilter struct {
	Field FieldRef
	Value any
}

func (GtFilter) isFilter() {}

// LtFilter checks less-than.
type LtFilter struct {
	Field FieldRef
	Value any
}

func (LtFilter) isFilter() {}

// ExistsFilter checks whether a field/path exists.
type ExistsFilter struct {
	Field FieldRef
}

func (ExistsFilter) isFilter() {}

// AndFilter combines filters with AND.
type AndFilter struct {
	Children []Filter
}

func (AndFilter) isFilter() {}

// OrFilter combines filters with OR.
type OrFilter struct {
	Children []Filter
}

func (OrFilter) isFilter() {}

// NotFilter negates a child filter.
type NotFilter struct {
	Child Filter
}

func (NotFilter) isFilter() {}

// Eq constructs an equality filter.
func Eq(field FieldRef, value any) Filter {
	return EqFilter{Field: field, Value: value}
}

// In constructs an IN filter.
func In(field FieldRef, values ...any) Filter {
	cp := make([]any, len(values))
	copy(cp, values)
	return InFilter{Field: field, Values: cp}
}

// Gt constructs a greater-than filter.
func Gt(field FieldRef, value any) Filter {
	return GtFilter{Field: field, Value: value}
}

// Lt constructs a less-than filter.
func Lt(field FieldRef, value any) Filter {
	return LtFilter{Field: field, Value: value}
}

// Exists constructs an exists filter.
func Exists(field FieldRef) Filter {
	return ExistsFilter{Field: field}
}

// And constructs an AND filter.
func And(children ...Filter) Filter {
	cp := make([]Filter, len(children))
	copy(cp, children)
	return AndFilter{Children: cp}
}

// Or constructs an OR filter.
func Or(children ...Filter) Filter {
	cp := make([]Filter, len(children))
	copy(cp, children)
	return OrFilter{Children: cp}
}

// Not constructs a NOT filter.
func Not(child Filter) Filter {
	return NotFilter{Child: child}
}

// ContainsAnyFilter tests whether an array contains any of Values.
type ContainsAnyFilter struct {
	Field  FieldRef
	Values []any
}

func (ContainsAnyFilter) isFilter() {}

// ContainsAllFilter tests whether an array contains every value in Values.
type ContainsAllFilter struct {
	Field  FieldRef
	Values []any
}

func (ContainsAllFilter) isFilter() {}

// ContainsAny tests overlap. An empty operand matches no non-NULL array.
func ContainsAny(field FieldRef, values ...any) Filter {
	return ContainsAnyFilter{field, append([]any(nil), values...)}
}

// ContainsAll tests containment. An empty operand matches every non-NULL array.
// SQL NULL arrays produce UNKNOWN for both helpers, including under Not.
func ContainsAll(field FieldRef, values ...any) Filter {
	return ContainsAllFilter{field, append([]any(nil), values...)}
}
