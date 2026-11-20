package qdrant

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/google/uuid"
)

// NativeVectorCollection operates on unsigned point IDs and one named cosine
// vector in an existing physical Qdrant collection. Callers own collection
// lifetime and schema. Each write replaces or deletes one complete point.
type NativeVectorCollection struct {
	points *NativePoints[uint64]
	vector string
}

var nativeName = regexp.MustCompile("^[A-Za-z0-9_-]+$")

// AttachNativeCosine validates the named vector and discovers indexed keyword
// fields. The physical name is used literally, independently of Store.Namespace.
func (s *Store) AttachNativeCosine(ctx context.Context, name, vector string, dimensions int) (*NativeVectorCollection, error) {
	points, err := AttachNativePoints[uint64](ctx, s, name, map[string]int{vector: dimensions})
	if err != nil {
		return nil, err
	}
	return &NativeVectorCollection{points, vector}, nil
}

// NativeKey is a key type supported by the Qdrant .NET connector.
type NativeKey interface{ uint64 | uuid.UUID }

// NativePoints accesses complete points with one or more named dense cosine vectors.
type NativePoints[K NativeKey] struct {
	store         *Store
	path          string
	dimensions    map[string]int
	keywordFields map[string]bool
	textFields    map[string]bool
}

// AttachNativePoints checks every vector in the physical schema before allowing
// replacement writes. The caller selects the collection's key type.
func AttachNativePoints[K NativeKey](ctx context.Context, s *Store, name string, dimensions map[string]int) (*NativePoints[K], error) {
	if !nativeName.MatchString(name) || strings.HasPrefix(name, "go_vectorstore_v1_") || len(dimensions) == 0 {
		return nil, vectordata.ErrSchemaMismatch
	}
	path := "/collections/" + name
	var schema response[struct {
		Config struct {
			Params struct {
				Sparse  map[string]any `json:"sparse_vectors"`
				Vectors map[string]struct {
					Size              int
					Distance          string
					MultivectorConfig any `json:"multivector_config"`
				}
			}
		}
		PayloadSchema map[string]struct {
			DataType string `json:"data_type"`
		} `json:"payload_schema"`
	}]
	if _, err := s.request(ctx, "GET", path, nil, &schema); err != nil {
		return nil, err
	}
	params := schema.Result.Config.Params
	if len(params.Sparse) > 0 || len(params.Vectors) != len(dimensions) {
		return nil, fmt.Errorf("%w: named vector set differs", vectordata.ErrSchemaMismatch)
	}
	sizes := make(map[string]int, len(dimensions))
	for vector, size := range dimensions {
		v := params.Vectors[vector]
		if vector == "" || size <= 0 || v.Size != size || v.Distance != "Cosine" || v.MultivectorConfig != nil {
			return nil, fmt.Errorf("%w: expected named dense cosine vector %q", vectordata.ErrSchemaMismatch, vector)
		}
		sizes[vector] = size
	}
	fields := make(map[string]bool)
	text := make(map[string]bool)
	for name, field := range schema.Result.PayloadSchema {
		if field.DataType == "text" {
			text[name] = true
		}
		if field.DataType == "keyword" {
			fields[name] = true
		}
	}
	return &NativePoints[K]{s, path, sizes, fields, text}, nil
}

// NativePoint holds the full payload and the selected named vector.
type NativePoint struct {
	ID      uint64
	Payload map[string]any
	Vector  []float32
}

// NativeHit carries Qdrant's raw cosine similarity.
type NativeHit struct {
	ID    uint64
	Score float64
}

// NamedPoint holds the complete payload and named vectors of a physical point.
type NamedPoint[K NativeKey] struct {
	ID      K                    `json:"id"`
	Payload map[string]any       `json:"payload"`
	Vectors map[string][]float32 `json:"vector"`
}

// NamedHit carries the native point ID and cosine similarity.
type NamedHit[K NativeKey] struct {
	ID    K
	Score float64
}

func (c *NativePoints[K]) validateVector(name string, vector []float32) error {
	size, ok := c.dimensions[name]
	if !ok {
		return vectordata.ErrSchemaMismatch
	}
	_, err := vectordata.NormalizeModelVector(vector, vectordata.VectorProperty{Dimensions: size, Metric: vectordata.DistanceCosine})
	return err
}

// ErrNativeOutcomeUnknown indicates that an attempted native write may have
// reached the server. A read can inspect current data but cannot prove history.
var ErrNativeOutcomeUnknown = errors.New("Qdrant native write outcome unknown")

func (c *NativePoints[K]) mutate(ctx context.Context, method, path string, body any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var result response[struct{ Status string }]
	status, err := c.store.request(ctx, method, c.path+path, body, &result)
	if err == nil && result.Result.Status == "completed" {
		return nil
	}
	if err != nil && (errors.Is(err, ErrClosed) || errors.Is(err, errRequestLimit) || status == 400 || status == 401 || status == 403 || status == 404) {
		return err
	}
	return errors.Join(ErrNativeOutcomeUnknown, err)
}

// UpsertPoint replaces all vectors and payload fields for one ID.
func (c *NativeVectorCollection) UpsertPoint(ctx context.Context, p NativePoint) error {
	return c.points.UpsertPoint(ctx, NamedPoint[uint64]{p.ID, p.Payload, map[string][]float32{c.vector: p.Vector}})
}

// UpsertPoint replaces a point after validating every configured vector.
func (c *NativePoints[K]) UpsertPoint(ctx context.Context, p NamedPoint[K]) error {
	if len(p.Vectors) != len(c.dimensions) {
		return vectordata.ErrSchemaMismatch
	}
	for name, v := range p.Vectors {
		if err := c.validateVector(name, v); err != nil {
			return err
		}
	}
	return c.mutate(ctx, http.MethodPut, "/points?wait=true", map[string]any{"points": []NamedPoint[K]{p}})
}

// GetPoint reads the current point, including its payload and vector.
func (c *NativeVectorCollection) GetPoint(ctx context.Context, id uint64) (NativePoint, error) {
	p, err := c.points.GetPoint(ctx, id)
	return NativePoint{p.ID, p.Payload, p.Vectors[c.vector]}, err
}

// GetPoint reads all stored vectors and payload fields.
func (c *NativePoints[K]) GetPoint(ctx context.Context, id K) (NamedPoint[K], error) {
	var out response[NamedPoint[K]]
	_, err := c.store.request(ctx, http.MethodGet, fmt.Sprintf("%s/points/%v", c.path, id), nil, &out)
	return out.Result, err
}

// DeletePoint deletes a single native point.
func (c *NativeVectorCollection) DeletePoint(ctx context.Context, id uint64) error {
	return c.points.DeletePoint(ctx, id)
}

// DeletePoint deletes a single native point.
func (c *NativePoints[K]) DeletePoint(ctx context.Context, id K) error {
	return c.mutate(ctx, http.MethodPost, "/points/delete?wait=true", map[string]any{"points": []K{id}})
}

// SearchKeyword searches the named vector with one server-side indexed keyword
// equality filter. Results retain native Qdrant ordering and score units.
func (c *NativeVectorCollection) SearchKeyword(ctx context.Context, vector []float32, top int, field, value string) ([]NativeHit, error) {
	hits, err := c.points.SearchKeyword(ctx, c.vector, vector, top, field, value)
	if err != nil {
		return nil, err
	}
	out := make([]NativeHit, len(hits))
	for i, hit := range hits {
		out[i] = NativeHit{hit.ID, hit.Score}
	}
	return out, nil
}

// SearchKeyword searches a selected vector with server-side keyword equality.
func (c *NativePoints[K]) SearchKeyword(ctx context.Context, name string, vector []float32, top int, field, value string) ([]NamedHit[K], error) {
	if !c.keywordFields[field] || !nativeName.MatchString(field) || top <= 0 {
		return nil, fmt.Errorf("%w: expected indexed keyword and positive top", vectordata.ErrInvalidRecord)
	}
	if err := c.validateVector(name, vector); err != nil {
		return nil, err
	}
	query := map[string]any{"using": name, "query": vector, "limit": top, "filter": map[string]any{"must": []any{map[string]any{"key": field, "match": map[string]string{"value": value}}}}}
	return c.query(ctx, query)
}

// HybridSearch fuses a named cosine search and full-text keyword matches using
// Qdrant's reciprocal rank fusion and the reference connector's prefetch defaults.
func (c *NativePoints[K]) HybridSearch(ctx context.Context, name string, vector []float32, field string, keywords []string, top int) ([]NamedHit[K], error) {
	if !c.textFields[field] || !nativeName.MatchString(field) || top < 1 {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validateVector(name, vector); err != nil {
		return nil, err
	}
	terms := make([]any, 0, len(keywords))
	for _, keyword := range keywords {
		terms = append(terms, map[string]any{"key": field, "match": map[string]string{"text": keyword}})
	}
	return c.query(ctx, map[string]any{
		"prefetch": []any{
			map[string]any{"using": name, "query": vector},
			map[string]any{"filter": map[string]any{"must": []any{map[string]any{"should": terms}}}},
		},
		"query": map[string]string{"fusion": "rrf"}, "limit": top,
	})
}

func (c *NativePoints[K]) query(ctx context.Context, query any) ([]NamedHit[K], error) {
	var out response[struct{ Points []NamedHit[K] }]
	if _, err := c.store.request(ctx, http.MethodPost, c.path+"/points/query", query, &out); err != nil {
		return nil, err
	}
	for _, hit := range out.Result.Points {
		if math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
			return nil, ErrCorrupt
		}
	}
	return out.Result.Points, nil
}
