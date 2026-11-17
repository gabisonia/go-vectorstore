package exactsearch

import (
	"fmt"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
)

// Canonicalize operands before a scan acquires database resources. Subsequent
// matching must not invoke caller MarshalJSON/MarshalText/String methods while
// SQLite holds its single pooled connection. JSON numbers retain exact precision.
// FreezeFilter validates and owns normalized operands before provider I/O or
// caller embedding callbacks. It preserves JSON and integer precision.
func FreezeFilter(filter vectordata.Filter, d vectordata.CollectionDefinition) (vectordata.Filter, error) {
	return freezeFilter(filter, d)
}

func freezeFilter(filter vectordata.Filter, d vectordata.CollectionDefinition) (vectordata.Filter, error) {
	if err := vectordata.ValidateModelFilter(filter, d); err != nil {
		return nil, err
	}
	out, err := copyFilter(filter, d)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", vectordata.ErrInvalidFilter, err)
	}
	return out, nil
}
func operand(ref vectordata.FieldRef, value any, membership bool, d vectordata.CollectionDefinition) (vectordata.FieldRef, any, error) {
	ref, err := vectordata.NormalizeFieldRef(ref)
	if err != nil {
		return ref, nil, err
	}
	if filterast.IsNilValue(value) {
		return ref, nil, nil
	}
	if ref.Kind == vectordata.FieldDataPath {
		v, err := vectordata.NormalizeModelData(value, vectordata.DataProperty{Type: vectordata.TypeJSON, Optional: true})
		return ref, v, err
	}
	if ref.Name == d.Key.Name {
		v, err := d.NormalizeKey(value)
		return ref, v, err
	}
	for _, p := range d.Data {
		if p.Name == ref.Name {
			if membership {
				v, err := vectordata.NormalizeModelArrayElement(value, p.Type)
				return ref, v, err
			}
			p.Optional = true
			v, err := vectordata.NormalizeModelData(value, p)
			return ref, v, err
		}
	}
	return ref, nil, vectordata.ErrInvalidFilter
}
func copyFilter(filter vectordata.Filter, d vectordata.CollectionDefinition) (vectordata.Filter, error) {
	one := func(ref vectordata.FieldRef, value any, membership bool, makeNode func(vectordata.FieldRef, any) vectordata.Filter) (vectordata.Filter, error) {
		ref, value, err := operand(ref, value, membership, d)
		if err != nil {
			return nil, err
		}
		return makeNode(ref, value), nil
	}
	many := func(ref vectordata.FieldRef, values []any, membership bool, makeNode func(vectordata.FieldRef, ...any) vectordata.Filter) (vectordata.Filter, error) {
		out := make([]any, len(values))
		var err error
		normalized, err := vectordata.NormalizeFieldRef(ref)
		if err != nil {
			return nil, err
		}
		for i, value := range values {
			_, out[i], err = operand(ref, value, membership, d)
			if err != nil {
				return nil, err
			}
		}
		return makeNode(normalized, out...), nil
	}
	logical := func(children []vectordata.Filter, makeNode func(...vectordata.Filter) vectordata.Filter) (vectordata.Filter, error) {
		out := make([]vectordata.Filter, len(children))
		for i, child := range children {
			var err error
			out[i], err = copyFilter(child, d)
			if err != nil {
				return nil, err
			}
		}
		return makeNode(out...), nil
	}
	switch f := filter.(type) {
	case nil:
		return nil, nil
	case vectordata.EqFilter:
		return one(f.Field, f.Value, false, vectordata.Eq)
	case vectordata.GtFilter:
		return one(f.Field, f.Value, false, vectordata.Gt)
	case vectordata.GteFilter:
		return one(f.Field, f.Value, false, vectordata.Gte)
	case vectordata.LtFilter:
		return one(f.Field, f.Value, false, vectordata.Lt)
	case vectordata.LteFilter:
		return one(f.Field, f.Value, false, vectordata.Lte)
	case vectordata.ContainsFilter:
		return one(f.Field, f.Value, true, vectordata.Contains)
	case vectordata.InFilter:
		return many(f.Field, f.Values, false, vectordata.In)
	case vectordata.ContainsAnyFilter:
		return many(f.Field, f.Values, true, vectordata.ContainsAny)
	case vectordata.ContainsAllFilter:
		return many(f.Field, f.Values, true, vectordata.ContainsAll)
	case vectordata.ExistsFilter:
		ref, err := vectordata.NormalizeFieldRef(f.Field)
		return vectordata.Exists(ref), err
	case vectordata.NotFilter:
		child, err := copyFilter(f.Child, d)
		return vectordata.Not(child), err
	case vectordata.AndFilter:
		return logical(f.Children, vectordata.And)
	case vectordata.OrFilter:
		return logical(f.Children, vectordata.Or)
	default:
		return nil, vectordata.ErrInvalidFilter
	}
}
