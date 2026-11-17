package redis

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Keep true and false sets separately: the remaining set is SQL UNKNOWN. NOT
// swaps the sets, preserving NULL behavior without treating UNKNOWN as false.
type nativeTruth struct{ yes, no string }
type nativeFilterPlan struct {
	query  string
	params []any
}
type nativeCompiler struct {
	c      *Collection
	params []any
	nodes  int
}

const nativeAll = "*"
const nativeNone = "@key:{never}"

func nativeNot(expr string) string {
	if expr == nativeAll {
		return nativeNone
	}
	if expr == nativeNone {
		return nativeAll
	}
	return "-(" + expr + ")"
}
func nativeJoin(parts []string, and bool) string {
	identity, zero := nativeNone, nativeAll
	join := " | "
	if and {
		identity, zero = nativeAll, nativeNone
		join = " "
	}
	var kept []string
	for _, part := range parts {
		if part == zero {
			return zero
		}
		if part != identity {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return identity
	}
	if len(kept) == 1 {
		return kept[0]
	}
	return "(" + strings.Join(kept, join) + ")"
}
func (n *nativeCompiler) bind(value any) string {
	name := "p" + strconv.Itoa(len(n.params)/2)
	n.params = append(n.params, name, value)
	return "$" + name
}
func (c *Collection) compileNativeFilter(filter vectordata.Filter) (nativeFilterPlan, bool, error) {
	n := nativeCompiler{c: c}
	truth, ok, err := n.filter(filter, 0)
	if err != nil || !ok || len(n.params) > 1024 {
		return nativeFilterPlan{}, false, err
	}
	return nativeFilterPlan{query: truth.yes, params: n.params}, true, nil
}
func (n *nativeCompiler) field(ref vectordata.FieldRef) (string, string, string, string, vectordata.DataProperty, bool, bool, error) {
	ref, err := vectordata.NormalizeFieldRef(ref)
	if err != nil {
		return "", "", "", "", vectordata.DataProperty{}, false, false, err
	}
	if ref.Kind != vectordata.FieldColumn {
		return "", "", "", "", vectordata.DataProperty{}, false, false, nil
	}
	if ref.Name == n.c.definition.Key.Name {
		return "key", nativeAll, "kh", "kl", vectordata.DataProperty{}, true, true, nil
	}
	for i, p := range n.c.definition.Data {
		if p.Name == ref.Name {
			suffix := strconv.Itoa(i)
			return "t" + suffix, "@h" + suffix + ":{x}", "hi" + suffix, "lo" + suffix, p, false, nativeData(p), nil
		}
	}
	return "", "", "", "", vectordata.DataProperty{}, false, false, nil
}
func (n *nativeCompiler) compare(ref vectordata.FieldRef, values []any, op string) (nativeTruth, bool, error) {
	alias, present, hiAlias, loAlias, p, key, ok, err := n.field(ref)
	if err != nil || !ok {
		return nativeTruth{}, false, err
	}
	if op == "exists" {
		return nativeTruth{present, nativeNot(present)}, true, nil
	}
	var normalized []any
	for _, v := range values {
		if v != nil {
			if key {
				v, err = n.c.definition.NormalizeKey(v)
			} else {
				v, err = vectordata.NormalizeModelData(v, p)
			}
			if err != nil {
				return nativeTruth{}, false, fmt.Errorf("%w: native operand: %v", vectordata.ErrInvalidFilter, err)
			}
		}
		normalized = append(normalized, v)
	}
	if op == "eq" && normalized[0] == nil {
		return nativeTruth{nativeNot(present), present}, true, nil
	}
	if op == "eq" || op == "in" {
		null := false
		var tags []string
		for _, v := range normalized {
			if v == nil {
				null = true
				continue
			}
			if key {
				if !modelcodec.ValidText(v) {
					continue
				}
				v = token(v)
			}
			tag, supported := nativeTag(v)
			if !supported {
				return nativeTruth{}, false, nil
			}
			tags = append(tags, n.bind(tag))
			if len(tags) > 256 {
				return nativeTruth{}, false, nil
			}
		}
		yes := nativeNone
		if len(tags) > 0 {
			yes = "@" + alias + ":{" + strings.Join(tags, "|") + "}"
		}
		no := nativeNone
		if !null {
			no = nativeJoin([]string{present, nativeNot(yes)}, true)
		}
		return nativeTruth{yes, no}, true, nil
	}
	hi, lo, numeric := integerParts(normalized[0])
	if !numeric {
		return nativeTruth{}, false, nil
	}
	h, l := n.bind(strconv.FormatInt(hi, 10)), n.bind(strconv.FormatInt(lo, 10))
	rangeExpr := func(alias, operand, operator string) string {
		switch operator {
		case "gt":
			return "@" + alias + ":[(" + operand + " +inf]"
		case "gte":
			return "@" + alias + ":[" + operand + " +inf]"
		case "lt":
			return "@" + alias + ":[-inf (" + operand + "]"
		default:
			return "@" + alias + ":[-inf " + operand + "]"
		}
	}
	strict := "gt"
	if op == "lt" || op == "lte" {
		strict = "lt"
	}
	equalHi := "@" + hiAlias + ":[" + h + " " + h + "]"
	yes := nativeJoin([]string{rangeExpr(hiAlias, h, strict), nativeJoin([]string{equalHi, rangeExpr(loAlias, l, op)}, true)}, false)
	return nativeTruth{yes, nativeJoin([]string{present, nativeNot(yes)}, true)}, true, nil
}
func (n *nativeCompiler) filter(filter vectordata.Filter, depth int) (nativeTruth, bool, error) {
	n.nodes++
	if depth > 16 || n.nodes > 128 {
		return nativeTruth{}, false, nil
	}
	switch f := filter.(type) {
	case nil:
		return nativeTruth{nativeAll, nativeNone}, true, nil
	case vectordata.EqFilter:
		return n.compare(f.Field, []any{f.Value}, "eq")
	case vectordata.InFilter:
		return n.compare(f.Field, f.Values, "in")
	case vectordata.GtFilter:
		return n.compare(f.Field, []any{f.Value}, "gt")
	case vectordata.GteFilter:
		return n.compare(f.Field, []any{f.Value}, "gte")
	case vectordata.LtFilter:
		return n.compare(f.Field, []any{f.Value}, "lt")
	case vectordata.LteFilter:
		return n.compare(f.Field, []any{f.Value}, "lte")
	case vectordata.ExistsFilter:
		return n.compare(f.Field, nil, "exists")
	case vectordata.NotFilter:
		t, ok, err := n.filter(f.Child, depth+1)
		return nativeTruth{t.no, t.yes}, ok, err
	case vectordata.AndFilter:
		return n.logical(f.Children, true, depth)
	case vectordata.OrFilter:
		return n.logical(f.Children, false, depth)
	default:
		return nativeTruth{}, false, nil
	}
}
func (n *nativeCompiler) logical(children []vectordata.Filter, and bool, depth int) (nativeTruth, bool, error) {
	var yes, no []string
	for _, child := range children {
		t, ok, err := n.filter(child, depth+1)
		if err != nil || !ok {
			return nativeTruth{}, false, err
		}
		yes = append(yes, t.yes)
		no = append(no, t.no)
	}
	return nativeTruth{nativeJoin(yes, and), nativeJoin(no, !and)}, true, nil
}
