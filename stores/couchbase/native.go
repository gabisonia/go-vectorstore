// Package couchbase accesses externally provisioned Couchbase collections using
// the Query and Search REST services.
package couchbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/internal/nativehttp"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema identifies one collection and its scoped cosine Search index.
type NativeSchema struct {
	Bucket, Scope, Collection, Index, Text, Vector string
	Dimensions                                     int
}

// NativeCollection uses the caller's transport for authentication and TLS.
type NativeCollection struct {
	query, search *nativehttp.Client
	keyspace      string
	s             NativeSchema
}

// NativeDocument contains the document ID (stored outside the JSON payload).
type NativeDocument struct {
	ID, Text string
	Vector   []float32
}

// NativeHit carries Couchbase Search's raw score.
type NativeHit struct {
	ID    string
	Score float64
}

// ErrNativeOutcomeUnknown marks an unconfirmed mutation.
var ErrNativeOutcomeUnknown = errors.New("Couchbase native write outcome unknown")

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

type fieldMapping struct {
	Enabled bool
	Fields  []struct {
		Name, Type, Similarity, Analyzer string
		Index                            bool
		Dims                             int
	}
}
type mapping struct {
	Enabled    bool
	Properties map[string]fieldMapping
}

// AttachNative validates the index's collection mapping, keyword field and
// cosine vector dimensions. Pass Query and Search service origins respectively.
func AttachNative(ctx context.Context, queryOrigin, searchOrigin string, s NativeSchema, transport http.RoundTripper) (*NativeCollection, error) {
	for _, name := range []string{s.Bucket, s.Scope, s.Collection, s.Index, s.Text, s.Vector} {
		if !identifier.MatchString(name) {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	if s.Text == s.Vector || s.Dimensions < 1 {
		return nil, vectordata.ErrSchemaMismatch
	}
	status := func(n int) error { return fmt.Errorf("Couchbase HTTP %d", n) }
	query, err := nativehttp.New(queryOrigin, "/query/service", transport, status, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, err
	}
	search, err := nativehttp.New(searchOrigin, "/api/bucket/"+s.Bucket+"/scope/"+s.Scope+"/index/"+s.Index, transport, status, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, err
	}
	var def struct {
		IndexDef struct {
			Type, SourceName string
			Params           struct {
				DocConfig struct{ Mode string } `json:"doc_config"`
				Mapping   struct {
					Default mapping `json:"default_mapping"`
					Types   map[string]mapping
				}
			}
		}
	}
	if err := search.Request(ctx, http.MethodGet, "", nil, &def, false); err != nil {
		return nil, err
	}
	index := def.IndexDef
	m := index.Params.Mapping.Types[s.Scope+"."+s.Collection]
	if index.Type != "fulltext-index" || index.SourceName != s.Bucket || index.Params.DocConfig.Mode != "scope.collection.type_field" || index.Params.Mapping.Default.Enabled || len(index.Params.Mapping.Types) != 1 || !m.Enabled {
		return nil, vectordata.ErrSchemaMismatch
	}
	textProperty, vectorProperty := m.Properties[s.Text], m.Properties[s.Vector]
	if !textProperty.Enabled || !vectorProperty.Enabled {
		return nil, vectordata.ErrSchemaMismatch
	}
	text, vector := textProperty.Fields, vectorProperty.Fields
	if len(text) != 1 || text[0].Name != s.Text || text[0].Type != "text" || !text[0].Index || text[0].Analyzer != "keyword" || len(vector) != 1 || vector[0].Name != s.Vector || vector[0].Type != "vector" || !vector[0].Index || vector[0].Similarity != "cosine" || vector[0].Dims != s.Dimensions {
		return nil, vectordata.ErrSchemaMismatch
	}
	return &NativeCollection{query, search, quote(s.Bucket) + "." + quote(s.Scope) + "." + quote(s.Collection), s}, nil
}

func quote(name string) string { return "`" + name + "`" }
func validID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 250 && utf8.ValidString(id)
}
func (c *NativeCollection) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.s.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}

func (c *NativeCollection) statement(ctx context.Context, stmt, id string, doc map[string]any, write bool) ([]json.RawMessage, error) {
	args := map[string]any{"statement": stmt, "$id": id, "readonly": !write}
	if doc != nil {
		args["$doc"] = doc
	}
	var reply struct {
		Status  string
		Results []json.RawMessage
		Errors  []struct {
			Code int
			Msg  string
		}
	}
	if err := c.query.Request(ctx, http.MethodPost, "", args, &reply, write); err != nil {
		return nil, err
	}
	if reply.Status != "success" || len(reply.Errors) > 0 || reply.Results == nil {
		err := fmt.Errorf("Couchbase query status %q: %v", reply.Status, reply.Errors)
		if write {
			err = errors.Join(ErrNativeOutcomeUnknown, err)
		}
		return nil, err
	}
	return reply.Results, nil
}

func (c *NativeCollection) mutate(ctx context.Context, stmt, id string, doc map[string]any, allowMissing bool) error {
	rows, err := c.statement(ctx, stmt, id, doc, true)
	if err != nil {
		return err
	}
	if len(rows) == 0 && allowMissing {
		return nil
	}
	var returned string
	if len(rows) != 1 || json.Unmarshal(rows[0], &returned) != nil || returned != id {
		return ErrNativeOutcomeUnknown
	}
	return nil
}

// Upsert replaces the document body with the two mapped fields.
func (c *NativeCollection) Upsert(ctx context.Context, d NativeDocument) error {
	if !validID(d.ID) || !utf8.ValidString(d.Text) {
		return vectordata.ErrInvalidRecord
	}
	if err := c.validate(d.Vector); err != nil {
		return err
	}
	return c.mutate(ctx, "UPSERT INTO "+c.keyspace+" (KEY, VALUE) VALUES ($id, $doc) RETURNING RAW META().id", d.ID, map[string]any{c.s.Text: d.Text, c.s.Vector: d.Vector}, false)
}

// Delete removes a single ID. Deleting an absent ID succeeds.
func (c *NativeCollection) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return vectordata.ErrInvalidRecord
	}
	return c.mutate(ctx, "DELETE FROM "+c.keyspace+" USE KEYS $id RETURNING RAW META().id", id, nil, true)
}

// Get performs a keyed read independently of Search index visibility.
func (c *NativeCollection) Get(ctx context.Context, id string) (NativeDocument, error) {
	if !validID(id) {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	rows, err := c.statement(ctx, "SELECT d."+quote(c.s.Text)+" AS txt, d."+quote(c.s.Vector)+" AS vec FROM "+c.keyspace+" AS d USE KEYS $id", id, nil, false)
	if err != nil {
		return NativeDocument{}, err
	}
	if len(rows) == 0 {
		return NativeDocument{}, vectordata.ErrNotFound
	}
	var row struct {
		Txt *string
		Vec []float32
	}
	if len(rows) != 1 || json.Unmarshal(rows[0], &row) != nil || row.Txt == nil {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	if err := c.validate(row.Vec); err != nil {
		return NativeDocument{}, err
	}
	return NativeDocument{id, *row.Txt, row.Vec}, nil
}

// Search applies an optional exact keyword prefilter inside the KNN request.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	if top < 1 || (textEquals != nil && !utf8.ValidString(*textEquals)) {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	knn := map[string]any{"field": c.s.Vector, "vector": v, "k": top}
	if textEquals != nil {
		knn["filter"] = map[string]string{"field": c.s.Text, "term": *textEquals}
	}
	var reply struct {
		Status struct{ Failed *int }
		Hits   []struct {
			ID    string
			Score *float64
		}
	}
	if err := c.search.Request(ctx, http.MethodPost, "/query", map[string]any{"size": top, "knn": []any{knn}}, &reply, false); err != nil {
		return nil, err
	}
	if reply.Status.Failed == nil || *reply.Status.Failed != 0 || reply.Hits == nil {
		return nil, errors.New("incomplete Couchbase Search response")
	}
	hits := make([]NativeHit, len(reply.Hits))
	for i, h := range reply.Hits {
		if !validID(h.ID) || h.Score == nil {
			return nil, vectordata.ErrInvalidRecord
		}
		hits[i] = NativeHit{h.ID, *h.Score}
	}
	return hits, nil
}
