package memory

import (
	"container/heap"
	"context"
	"math"
	"sort"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func (c *Collection) Query(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) ([]vectordata.ModelRecord, error) {
	if err := vectordata.ValidateModelQuery(c.definition, top, opts); err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelFilter(filter, c.definition); err != nil {
		return nil, err
	}
	limit := opts.Skip + min(top, math.MaxInt-opts.Skip)
	best := queryPage{key: c.definition.Key.Name, order: opts.OrderBy}
	for row, err := range c.searchSnapshot(ctx) {
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		match, err := vectordata.MatchModelFilter(filter, row, c.definition)
		if err != nil {
			return nil, err
		}
		if !match {
			continue
		}
		if best.Len() < limit {
			heap.Push(&best, row)
		} else if best.before(row, best.rows[0]) {
			best.rows[0] = row
			heap.Fix(&best, 0)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(&best))
	page := best.rows[min(opts.Skip, best.Len()):]
	out := make([]vectordata.ModelRecord, len(page))
	for i, row := range page {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[i] = vectordata.CloneModelRecord(row, c.definition, opts.IncludeVectors)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// The heap root is the last row in the requested ordering.
type queryPage struct {
	rows  []vectordata.ModelRecord
	key   string
	order []vectordata.SortField
}

func (p *queryPage) before(a, b vectordata.ModelRecord) bool {
	for _, field := range p.order {
		x, y := a.Data[field.Property], b.Data[field.Property]
		if field.Property == p.key {
			x, y = a.Key, b.Key
		}
		if n := vectordata.CompareModelValues(x, y); n != 0 {
			if x == nil || y == nil {
				return x != nil
			}
			if field.Descending {
				return n > 0
			}
			return n < 0
		}
	}
	switch key := a.Key.(type) {
	case int64:
		return key < b.Key.(int64)
	case string:
		return key < b.Key.(string)
	default:
		return vectordata.CompareModelValues(a.Key, b.Key) < 0
	}
}

func (p *queryPage) Len() int           { return len(p.rows) }
func (p *queryPage) Less(i, j int) bool { return p.before(p.rows[j], p.rows[i]) }
func (p *queryPage) Swap(i, j int)      { p.rows[i], p.rows[j] = p.rows[j], p.rows[i] }
func (p *queryPage) Push(v any)         { p.rows = append(p.rows, v.(vectordata.ModelRecord)) }
func (p *queryPage) Pop() any {
	last := len(p.rows) - 1
	v := p.rows[last]
	p.rows[last] = vectordata.ModelRecord{}
	p.rows = p.rows[:last]
	return v
}
