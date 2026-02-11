package mssql

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func matchesFilter(filter vectordata.Filter, record vectordata.Record) (bool, error) {
	if filter == nil {
		return true, nil
	}

	switch node := filter.(type) {
	case vectordata.EqFilter:
		left, exists, err := resolveFieldValue(node.Field, record)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
		return valuesEqual(left, node.Value), nil
	case vectordata.InFilter:
		if len(node.Values) == 0 {
			return false, fmt.Errorf("%w: IN requires at least one value", vectordata.ErrInvalidFilter)
		}
		left, exists, err := resolveFieldValue(node.Field, record)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
		for _, value := range node.Values {
			if valuesEqual(left, value) {
				return true, nil
			}
		}
		return false, nil
	case vectordata.GtFilter:
		left, exists, err := resolveFieldValue(node.Field, record)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
		comparison := compareValues(left, node.Value)
		return comparison > 0, nil
	case vectordata.LtFilter:
		left, exists, err := resolveFieldValue(node.Field, record)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
		comparison := compareValues(left, node.Value)
		return comparison < 0, nil
	case vectordata.ExistsFilter:
		_, exists, err := resolveFieldValue(node.Field, record)
		if err != nil {
			return false, err
		}
		return exists, nil
	case vectordata.AndFilter:
		if len(node.Children) == 0 {
			return false, fmt.Errorf("%w: AND requires at least one child", vectordata.ErrInvalidFilter)
		}
		for _, child := range node.Children {
			if child == nil {
				return false, fmt.Errorf("%w: AND contains nil child", vectordata.ErrInvalidFilter)
			}
			ok, err := matchesFilter(child, record)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	case vectordata.OrFilter:
		if len(node.Children) == 0 {
			return false, fmt.Errorf("%w: OR requires at least one child", vectordata.ErrInvalidFilter)
		}
		for _, child := range node.Children {
			if child == nil {
				return false, fmt.Errorf("%w: OR contains nil child", vectordata.ErrInvalidFilter)
			}
			ok, err := matchesFilter(child, record)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case vectordata.NotFilter:
		if node.Child == nil {
			return false, fmt.Errorf("%w: NOT requires a child", vectordata.ErrInvalidFilter)
		}
		ok, err := matchesFilter(node.Child, record)
		if err != nil {
			return false, err
		}
		return !ok, nil
	default:
		return false, fmt.Errorf("%w: unsupported node type %T", vectordata.ErrInvalidFilter, filter)
	}
}

func resolveFieldValue(field vectordata.FieldRef, record vectordata.Record) (value any, exists bool, err error) {
	switch field.Kind {
	case vectordata.FieldColumn:
		name := strings.TrimSpace(field.Name)
		if name == "" {
			return nil, false, fmt.Errorf("%w: column field name is empty", vectordata.ErrInvalidFilter)
		}

		switch name {
		case idColumn:
			return record.ID, true, nil
		case contentColumn:
			if record.Content == nil {
				return nil, false, nil
			}
			return *record.Content, true, nil
		default:
			return nil, false, fmt.Errorf("%w: unknown column %q", vectordata.ErrInvalidFilter, name)
		}
	case vectordata.FieldMetadata:
		if len(field.Path) == 0 {
			return nil, false, fmt.Errorf("%w: metadata path is empty", vectordata.ErrInvalidFilter)
		}
		if record.Metadata == nil {
			return nil, false, nil
		}

		var current any = record.Metadata
		for _, segment := range field.Path {
			key := strings.TrimSpace(segment)
			if key == "" {
				return nil, false, fmt.Errorf("%w: metadata path segment is empty", vectordata.ErrInvalidFilter)
			}

			asMap, ok := current.(map[string]any)
			if !ok {
				return nil, false, nil
			}

			next, ok := asMap[key]
			if !ok {
				return nil, false, nil
			}
			current = next
		}

		return current, true, nil
	default:
		return nil, false, fmt.Errorf("%w: unsupported field kind %q", vectordata.ErrInvalidFilter, field.Kind)
	}
}

func valuesEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == right
	}

	leftNumeric, leftIsNumeric := toFloat64(left)
	rightNumeric, rightIsNumeric := toFloat64(right)
	if leftIsNumeric && rightIsNumeric {
		return leftNumeric == rightNumeric
	}

	return reflect.DeepEqual(left, right)
}

func compareValues(left, right any) int {
	leftNumeric, leftIsNumeric := toFloat64(left)
	rightNumeric, rightIsNumeric := toFloat64(right)
	if leftIsNumeric && rightIsNumeric {
		switch {
		case leftNumeric < rightNumeric:
			return -1
		case leftNumeric > rightNumeric:
			return 1
		default:
			return 0
		}
	}

	leftText := fmt.Sprint(left)
	rightText := fmt.Sprint(right)
	switch {
	case leftText < rightText:
		return -1
	case leftText > rightText:
		return 1
	default:
		return 0
	}
}
