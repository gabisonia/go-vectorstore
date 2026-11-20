// Package milvus accesses existing collections through the Milvus v2 REST API.
package milvus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/internal/nativehttp"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema describes a collection in the default database.
type NativeSchema struct {
	Collection, Key, Text, Vector string
	Dimensions                    int
}

// NativeCollection uses explicit VarChar keys and a dense float cosine vector.
type NativeCollection struct {
	api                 *nativehttp.Client
	s                   NativeSchema
	keyLimit, textLimit int
	nullable            bool
}

// NativeDocument contains a complete row. Text may be nil for nullable fields.
type NativeDocument struct {
	ID     string
	Text   *string
	Vector []float32
}

// NativeHit contains the native higher-is-better cosine similarity.
type NativeHit struct {
	ID    string
	Score float64
}

// ErrNativeOutcomeUnknown marks an unconfirmed write. The adapter does not retry.
var ErrNativeOutcomeUnknown = errors.New("Milvus native write outcome unknown")

// ResponseError preserves the Milvus application code from a REST response.
type ResponseError struct {
	Code    int
	Message string
}

func (e *ResponseError) Error() string { return fmt.Sprintf("Milvus code %d: %s", e.Code, e.Message) }

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*$`)

type field struct {
	Name, Type                         string
	PrimaryKey, Nullable, PartitionKey bool
	Params                             []struct{ Key, Value string }
}

func (f field) limit(name string) int {
	for _, param := range f.Params {
		if param.Key == name {
			n, err := strconv.Atoi(param.Value)
			if err == nil {
				return n
			}
		}
	}
	return 0
}

// AttachNative checks the physical fields, dimensions and metric before allowing writes.
// Supply a transport for authentication or custom TLS.
func AttachNative(ctx context.Context, endpoint string, s NativeSchema, transport http.RoundTripper) (*NativeCollection, error) {
	for _, v := range []string{s.Collection, s.Key, s.Text, s.Vector} {
		if !identifier.MatchString(v) {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	if s.Key == s.Text || s.Key == s.Vector || s.Text == s.Vector || s.Key == "distance" || s.Dimensions <= 0 {
		return nil, vectordata.ErrSchemaMismatch
	}
	api, err := nativehttp.New(endpoint, "/v2/vectordb", transport, func(code int) error { return fmt.Errorf("Milvus HTTP %d", code) }, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, err
	}
	c := &NativeCollection{api: api, s: s}
	var desc struct {
		CollectionName             string
		AutoID, EnableDynamicField bool
		Fields                     []field
		StructFields, Functions    []json.RawMessage
		Indexes                    []struct{ FieldName, MetricType string }
	}
	if err := c.request(ctx, "collections/describe", map[string]any{}, &desc, false); err != nil {
		return nil, err
	}
	if desc.CollectionName != s.Collection || desc.AutoID || desc.EnableDynamicField || len(desc.Fields) != 3 || len(desc.StructFields)+len(desc.Functions) != 0 {
		return nil, vectordata.ErrSchemaMismatch
	}
	fields := make(map[string]field)
	for _, f := range desc.Fields {
		if f.PartitionKey || f.PrimaryKey != (f.Name == s.Key) {
			return nil, vectordata.ErrSchemaMismatch
		}
		fields[f.Name] = f
	}
	k, t, v := fields[s.Key], fields[s.Text], fields[s.Vector]
	c.keyLimit, c.textLimit, c.nullable = k.limit("max_length"), t.limit("max_length"), t.Nullable
	if k.Type != "VarChar" || t.Type != "VarChar" || v.Type != "FloatVector" || v.Nullable || v.limit("dim") != s.Dimensions || c.keyLimit < 1 || c.textLimit < 1 {
		return nil, vectordata.ErrSchemaMismatch
	}
	for _, idx := range desc.Indexes {
		if idx.FieldName == s.Vector && idx.MetricType == "COSINE" {
			return c, nil
		}
	}
	return nil, vectordata.ErrSchemaMismatch
}

func (c *NativeCollection) request(ctx context.Context, path string, args map[string]any, out any, write bool) error {
	args["collectionName"] = c.s.Collection
	var reply struct {
		Code    *int
		Message string
		Data    json.RawMessage
	}
	if err := c.api.Request(ctx, http.MethodPost, "/"+path, args, &reply, write); err != nil {
		return err
	}
	var err error
	switch {
	case reply.Code == nil:
		err = errors.New("missing Milvus response code")
	case *reply.Code != 0:
		err = &ResponseError{*reply.Code, reply.Message}
	case len(reply.Data) == 0 || string(reply.Data) == "null":
		err = errors.New("missing Milvus response data")
	default:
		err = json.Unmarshal(reply.Data, out)
	}
	if err != nil && write {
		return errors.Join(ErrNativeOutcomeUnknown, err)
	}
	return err
}

func (c *NativeCollection) validID(id string) bool {
	return id != "" && len(id) <= c.keyLimit && utf8.ValidString(id)
}
func (c *NativeCollection) validateVector(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.s.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}

// Upsert replaces the complete row and verifies the acknowledged key.
func (c *NativeCollection) Upsert(ctx context.Context, p NativeDocument) error {
	if !c.validID(p.ID) || (p.Text == nil && !c.nullable) || (p.Text != nil && (len(*p.Text) > c.textLimit || !utf8.ValidString(*p.Text))) {
		return vectordata.ErrInvalidRecord
	}
	if err := c.validateVector(p.Vector); err != nil {
		return err
	}
	row := map[string]any{c.s.Key: p.ID, c.s.Text: p.Text, c.s.Vector: p.Vector}
	var ack struct {
		UpsertCount int
		UpsertIDs   []string
	}
	if err := c.request(ctx, "entities/upsert", map[string]any{"data": []any{row}}, &ack, true); err != nil {
		return err
	}
	if ack.UpsertCount != 1 || len(ack.UpsertIDs) != 1 || ack.UpsertIDs[0] != p.ID {
		return ErrNativeOutcomeUnknown
	}
	return nil
}

// Get uses a strongly consistent primary-key read.
func (c *NativeCollection) Get(ctx context.Context, id string) (NativeDocument, error) {
	if !c.validID(id) {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	var rows []map[string]json.RawMessage
	err := c.request(ctx, "entities/get", map[string]any{"id": []string{id}, "outputFields": []string{c.s.Key, c.s.Text, c.s.Vector}, "consistencyLevel": "Strong"}, &rows, false)
	if err != nil {
		return NativeDocument{}, err
	}
	if len(rows) == 0 {
		return NativeDocument{}, vectordata.ErrNotFound
	}
	var p NativeDocument
	if len(rows) != 1 || json.Unmarshal(rows[0][c.s.Key], &p.ID) != nil || p.ID != id || json.Unmarshal(rows[0][c.s.Text], &p.Text) != nil || (p.Text == nil && !c.nullable) || json.Unmarshal(rows[0][c.s.Vector], &p.Vector) != nil {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	if err := c.validateVector(p.Vector); err != nil {
		return NativeDocument{}, err
	}
	return p, nil
}

// Delete acknowledges a single-key delete, including a missing row.
func (c *NativeCollection) Delete(ctx context.Context, id string) error {
	if !c.validID(id) {
		return vectordata.ErrInvalidRecord
	}
	var ack struct{ DeleteCount *int }
	err := c.request(ctx, "entities/delete", map[string]any{"filter": c.s.Key + " == {value}", "exprParams": map[string]string{"value": id}}, &ack, true)
	if err != nil {
		return err
	}
	if ack.DeleteCount == nil || *ack.DeleteCount < 0 || *ack.DeleteCount > 1 {
		return ErrNativeOutcomeUnknown
	}
	return nil
}

// Search returns strongly consistent native cosine results. If textEquals is
// supplied, Milvus evaluates the bound text predicate before ranking.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	if top < 1 || top > 16384 || (textEquals != nil && !utf8.ValidString(*textEquals)) {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validateVector(v); err != nil {
		return nil, err
	}
	args := map[string]any{"data": [][]float32{v}, "annsField": c.s.Vector, "limit": top, "consistencyLevel": "Strong"}
	if textEquals != nil {
		args["filter"] = c.s.Text + " == {value}"
		args["exprParams"] = map[string]string{"value": *textEquals}
	}
	var rows []map[string]json.RawMessage
	if err := c.request(ctx, "entities/search", args, &rows, false); err != nil {
		return nil, err
	}
	hits := make([]NativeHit, len(rows))
	for i, row := range rows {
		var score *float64
		if json.Unmarshal(row[c.s.Key], &hits[i].ID) != nil || !c.validID(hits[i].ID) || json.Unmarshal(row["distance"], &score) != nil || score == nil {
			return nil, vectordata.ErrInvalidRecord
		}
		hits[i].Score = *score
	}
	return hits, nil
}
