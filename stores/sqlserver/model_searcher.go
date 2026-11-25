package sqlserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type modelSearcher[T any] struct {
	c     *NativeCollection
	codec *vectordata.StructCodec[T]
	def   vectordata.CollectionDefinition
}

// NewNativeModelSearcher binds a model to an attached bigint/text/cosine table.
// A nil definition infers struct tags. Unsigned model keys reject negative IDs.
func NewNativeModelSearcher[T any](c *NativeCollection, definition *vectordata.CollectionDefinition) (vectordata.VectorSearcher[T], error) {
	codec, err := vectordata.NewStructCodec[T](definition)
	if err != nil {
		return nil, err
	}
	d := codec.Definition()
	if c == nil || len(d.Data) != 1 || len(d.Vectors) != 1 || ident(d.Key.StorageName) != c.key || d.Key.Type != vectordata.KeyInt64 && d.Key.Type != vectordata.KeyUint64 {
		return nil, fmt.Errorf("%w: expected an integer key, one text and one vector field", vectordata.ErrSchemaMismatch)
	}
	p, v := d.Data[0], d.Vectors[0]
	if p.Type != vectordata.TypeString || ident(p.StorageName) != c.text || ident(v.StorageName) != c.vector || v.Dimensions != c.dimensions || v.Kind != "" || v.Precision != "" || v.Metric != vectordata.DistanceCosine {
		return nil, fmt.Errorf("%w: model does not match attached text and vector columns", vectordata.ErrSchemaMismatch)
	}
	return &modelSearcher[T]{c, codec, d}, nil
}

func (s *modelSearcher[T]) SearchIter(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[T], error] {
	return func(yield func(vectordata.TypedModelSearchResult[T], error) bool) {
		var zero vectordata.TypedModelSearchResult[T]
		if err := ctx.Err(); err != nil {
			yield(zero, err)
			return
		}
		rows, err := s.query(ctx, vector, top, opts)
		if err != nil {
			yield(zero, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			hit, err := s.read(rows, opts.IncludeVectors)
			if err = errors.Join(err, ctx.Err()); err != nil {
				yield(zero, err)
				return
			}
			if !yield(hit, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(zero, err)
		}
	}
}

func (s *modelSearcher[T]) query(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) (*sql.Rows, error) {
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	if _, err := s.def.Vector(opts.VectorProperty); err != nil {
		return nil, err
	}
	if opts.ProviderOptions != nil || opts.ScoreThreshold != nil || opts.DistanceThreshold != nil {
		return nil, vectordata.ErrUnsupported
	}
	c := s.c
	raw, err := c.encode(vector)
	if err != nil {
		return nil, err
	}
	args := []any{raw, opts.Skip, top}
	where := c.vector + " IS NOT NULL"
	if opts.Filter != nil {
		eq, ok := opts.Filter.(vectordata.EqFilter)
		value, isString := eq.Value.(string)
		if !ok || !isString || eq.Field.Kind != vectordata.FieldColumn || eq.Field.Name != s.def.Data[0].Name || len(eq.Field.Path) != 0 {
			return nil, vectordata.ErrUnsupported
		}
		// Binary collation preserves case and accents. SQL equality pads trailing spaces.
		where += " AND " + c.text + " COLLATE Latin1_General_100_BIN2 = @p4 AND DATALENGTH(" + c.text + ") = DATALENGTH(@p4)"
		args = append(args, value)
	}
	projection := ""
	if opts.IncludeVectors {
		projection = ",CAST(" + c.vector + " AS varchar(max))"
	}
	stmt := fmt.Sprintf("SELECT %s,%s,VECTOR_DISTANCE('cosine',%s,CAST(@p1 AS %s)) AS [__vs_distance]%s FROM %s WHERE %s ORDER BY 3,%s OFFSET @p2 ROWS FETCH NEXT @p3 ROWS ONLY", c.key, c.text, c.vector, c.cast, projection, c.table, where, c.key)
	return c.db.QueryContext(ctx, stmt, args...)
}

func (s *modelSearcher[T]) read(rows *sql.Rows, include bool) (vectordata.TypedModelSearchResult[T], error) {
	var hit vectordata.TypedModelSearchResult[T]
	var id int64
	var text sql.NullString
	var raw string
	dest := []any{&id, &text, &hit.Distance}
	if include {
		dest = append(dest, &raw)
	}
	if err := rows.Scan(dest...); err != nil {
		return hit, err
	}
	key, err := s.def.NormalizeKey(id)
	if err != nil {
		return hit, err
	}
	p := s.def.Data[0]
	r := vectordata.ModelRecord{Key: key, Data: map[string]any{}}
	if text.Valid {
		r.Data[p.Name] = text.String
	} else if !p.Optional {
		return hit, fmt.Errorf("%w: missing %q", vectordata.ErrInvalidRecord, p.Name)
	}
	if include {
		var v []float32
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return hit, err
		}
		r.Vectors = map[string][]float32{s.def.Vectors[0].Name: v}
	}
	hit.Item, err = s.codec.Decode(r)
	hit.ScoreKind = vectordata.ScoreSimilarity
	hit.Score = vectordata.ScoreFromDistance(vectordata.DistanceCosine, hit.Distance)
	return hit, err
}
