package redis

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

// NativeVectorIndex reads an existing Redis Search index using the C# connector's
// collection-name key prefix. Index and document lifetime belong to the caller.
type NativeVectorIndex struct {
	store               *Store
	name, field, layout string
	dimensions          int
}

// NativeVectorHit contains a logical key and Redis' raw cosine distance.
type NativeVectorHit struct {
	ID       string
	Distance float64
}

var nativeVectorAlias = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const distanceAlias = "__go_vectorstore_native_distance"

// AttachNativeCosine checks the index prefix, storage type and selected indexed
// FLOAT32 field. Both Redis hashes and RedisJSON documents are supported.
func (s *Store) AttachNativeCosine(ctx context.Context, name, field string, dimensions int) (*NativeVectorIndex, error) {
	if !validName(name) || strings.HasPrefix(name, "__vectorstore_redis_v1:") || !nativeVectorAlias.MatchString(field) || dimensions <= 0 {
		return nil, vectordata.ErrSchemaMismatch
	}
	info, err := s.client.FTInfo(ctx, name).Result()
	if err != nil {
		return nil, err
	}
	index := info.IndexDefinition
	if (index.KeyType != "HASH" && index.KeyType != "JSON") || len(index.Prefixes) != 1 || index.Prefixes[0] != name+":" {
		return nil, fmt.Errorf("%w: expected a collection-name Redis key prefix", vectordata.ErrSchemaMismatch)
	}
	identifier := field
	if index.KeyType == "JSON" {
		identifier = "$." + field
	}
	valid := false
	for _, attr := range info.Attributes {
		if attr.Attribute == distanceAlias {
			return nil, fmt.Errorf("%w: reserved distance alias", vectordata.ErrSchemaMismatch)
		}
		if attr.Attribute == field && attr.Identifier == identifier && attr.Type == "VECTOR" && !attr.NoIndex && attr.DataType == "FLOAT32" && attr.DistanceMetric == "COSINE" && attr.Dim == dimensions {
			valid = true
		}
	}
	if !valid {
		return nil, fmt.Errorf("%w: incompatible native Redis vector field", vectordata.ErrSchemaMismatch)
	}
	return &NativeVectorIndex{s, name, field, index.KeyType, dimensions}, nil
}

// Search returns up to top logical IDs ordered by ascending native cosine distance.
func (c *NativeVectorIndex) Search(ctx context.Context, vector []float32, top int) ([]NativeVectorHit, error) {
	if top <= 0 {
		return nil, vectordata.ErrInvalidRecord
	}
	if _, err := vectordata.NormalizeModelVector(vector, vectordata.VectorProperty{Dimensions: c.dimensions, Metric: vectordata.DistanceCosine}); err != nil {
		return nil, err
	}
	packed, err := binary.Append(nil, binary.LittleEndian, vector)
	if err != nil {
		return nil, err
	}
	query := "*=>[KNN $k @" + c.field + " $v AS " + distanceAlias + "]"
	result, err := c.store.client.FTSearchWithArgs(ctx, c.name, query, &goredis.FTSearchOptions{
		Params: map[string]any{"k": top, "v": packed}, SortBy: []goredis.FTSearchSortBy{{FieldName: distanceAlias, Asc: true}},
		Return: []goredis.FTSearchReturn{{FieldName: distanceAlias}}, Limit: top, DialectVersion: 2,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(result.Warnings) > 0 {
		return nil, fmt.Errorf("native Redis search: %s", strings.Join(result.Warnings, ", "))
	}
	hits := make([]NativeVectorHit, 0, len(result.Docs))
	for _, doc := range result.Docs {
		if doc.Error != nil {
			return nil, doc.Error
		}
		key, ok := strings.CutPrefix(doc.ID, c.name+":")
		d, err := strconv.ParseFloat(doc.Fields[distanceAlias], 64)
		if !ok || err != nil || math.IsNaN(d) || math.IsInf(d, 0) {
			return nil, vectordata.ErrInvalidRecord
		}
		hits = append(hits, NativeVectorHit{key, d})
	}
	return hits, nil
}

// NativeDocument contains the complete text and vector fields of a two-property
// reference model. TextField arguments refer to physical storage names.
type NativeDocument struct {
	ID, Text string
	Vector   []float32
}

func (c *NativeVectorIndex) validateTextField(textField string) error {
	if !nativeVectorAlias.MatchString(textField) || textField == c.field {
		return vectordata.ErrSchemaMismatch
	}
	return nil
}

// ErrNativeOutcomeUnknown reports a native command whose reply could not be
// confirmed. Store disables automatic retries for writes.
var ErrNativeOutcomeUnknown = errors.New("Redis native write outcome unknown")

func nativeWriteResult(err error) error {
	if err == nil {
		return nil
	}
	var serverError goredis.Error
	if errors.As(err, &serverError) {
		return err
	}
	return errors.Join(ErrNativeOutcomeUnknown, err)
}

// UpsertDocument uses the reference connector's native write for the layout.
// HSET updates the two named fields. JSON.SET replaces the root document.
func (c *NativeVectorIndex) UpsertDocument(ctx context.Context, textField string, p NativeDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateTextField(textField); err != nil {
		return err
	}
	if _, err := vectordata.NormalizeModelVector(p.Vector, vectordata.VectorProperty{Dimensions: c.dimensions, Metric: vectordata.DistanceCosine}); err != nil {
		return err
	}
	key := c.name + ":" + p.ID
	if c.layout == "JSON" {
		data, err := json.Marshal(map[string]any{textField: p.Text, c.field: p.Vector})
		if err != nil {
			return err
		}
		return nativeWriteResult(c.store.client.Do(ctx, "JSON.SET", key, "$", data).Err())
	}
	data, err := binary.Append(nil, binary.LittleEndian, p.Vector)
	if err != nil {
		return err
	}
	return nativeWriteResult(c.store.client.HSet(ctx, key, textField, p.Text, c.field, data).Err())
}

// GetDocument reads the selected text and vector. A missing key returns redis.Nil.
func (c *NativeVectorIndex) GetDocument(ctx context.Context, id, textField string) (NativeDocument, error) {
	if err := c.validateTextField(textField); err != nil {
		return NativeDocument{}, err
	}
	key := c.name + ":" + id
	p := NativeDocument{ID: id}
	if c.layout == "JSON" {
		raw, err := c.store.client.Do(ctx, "JSON.GET", key).Text()
		if err != nil {
			return NativeDocument{}, err
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal([]byte(raw), &fields); err != nil {
			return NativeDocument{}, err
		}
		var text *string
		if err = json.Unmarshal(fields[textField], &text); err != nil || text == nil {
			return NativeDocument{}, vectordata.ErrInvalidRecord
		}
		p.Text = *text
		if err = json.Unmarshal(fields[c.field], &p.Vector); err != nil {
			return NativeDocument{}, err
		}
	} else {
		fields, err := c.store.client.HGetAll(ctx, key).Result()
		if err != nil {
			return NativeDocument{}, err
		}
		if len(fields) == 0 {
			return NativeDocument{}, goredis.Nil
		}
		var ok bool
		p.Text, ok = fields[textField]
		data := fields[c.field]
		if !ok || len(data)%4 != 0 || len(data)/4 != c.dimensions {
			return NativeDocument{}, vectordata.ErrInvalidRecord
		}
		p.Vector = make([]float32, c.dimensions)
		if _, err = binary.Decode([]byte(data), binary.LittleEndian, p.Vector); err != nil {
			return NativeDocument{}, err
		}
	}
	if _, err := vectordata.NormalizeModelVector(p.Vector, vectordata.VectorProperty{Dimensions: c.dimensions, Metric: vectordata.DistanceCosine}); err != nil {
		return NativeDocument{}, err
	}
	return p, nil
}

// DeleteDocument removes the entire native key.
func (c *NativeVectorIndex) DeleteDocument(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nativeWriteResult(c.store.client.Del(ctx, c.name+":"+id).Err())
}
