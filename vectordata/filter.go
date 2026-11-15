package vectordata

import "github.com/gabisonia/go-vectorstore/vectordata/filterast"

type FieldRef = filterast.FieldRef
type FieldKind = filterast.FieldKind
type Filter = filterast.Filter
type EqFilter = filterast.EqFilter
type InFilter = filterast.InFilter
type GtFilter = filterast.GtFilter
type LtFilter = filterast.LtFilter
type GteFilter = filterast.GteFilter
type LteFilter = filterast.LteFilter
type ContainsFilter = filterast.ContainsFilter
type ExistsFilter = filterast.ExistsFilter
type AndFilter = filterast.AndFilter
type OrFilter = filterast.OrFilter
type NotFilter = filterast.NotFilter

const (
	FieldColumn   = filterast.FieldColumn
	FieldMetadata = filterast.FieldMetadata
	FieldDataPath = filterast.FieldDataPath
)

func Column(name string) FieldRef                       { return filterast.Column(name) }
func Metadata(path ...string) FieldRef                  { return filterast.Metadata(path...) }
func DataPath(property string, path ...string) FieldRef { return filterast.DataPath(property, path...) }
func Eq(field FieldRef, value any) Filter               { return filterast.Eq(field, value) }
func Ne(field FieldRef, value any) Filter               { return filterast.Ne(field, value) }
func Gt(field FieldRef, value any) Filter               { return filterast.Gt(field, value) }
func Lt(field FieldRef, value any) Filter               { return filterast.Lt(field, value) }
func Gte(field FieldRef, value any) Filter              { return filterast.Gte(field, value) }
func Lte(field FieldRef, value any) Filter              { return filterast.Lte(field, value) }
func Contains(field FieldRef, value any) Filter         { return filterast.Contains(field, value) }
func In(field FieldRef, values ...any) Filter           { return filterast.In(field, values...) }
func Exists(field FieldRef) Filter                      { return filterast.Exists(field) }
func And(children ...Filter) Filter                     { return filterast.And(children...) }
func Or(children ...Filter) Filter                      { return filterast.Or(children...) }
func Not(child Filter) Filter                           { return filterast.Not(child) }

type ContainsAnyFilter = filterast.ContainsAnyFilter
type ContainsAllFilter = filterast.ContainsAllFilter

func ContainsAny(field FieldRef, values ...any) Filter {
	return filterast.ContainsAny(field, values...)
}
func ContainsAll(field FieldRef, values ...any) Filter {
	return filterast.ContainsAll(field, values...)
}
