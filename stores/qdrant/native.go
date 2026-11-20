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
)

// NativeVectorCollection operates on unsigned point IDs and one named cosine
// vector in an existing physical Qdrant collection. Callers own collection
// lifetime and schema. Each write replaces or deletes one complete point.
type NativeVectorCollection struct {
	store         *Store
	path, vector  string
	dimensions    int
	keywordFields map[string]bool
}

var nativeName = regexp.MustCompile("^[A-Za-z0-9_-]+$")

// AttachNativeCosine validates the named vector and discovers indexed keyword
// fields. The physical name is used literally, independently of Store.Namespace.
func (s *Store) AttachNativeCosine(ctx context.Context, name, vector string, dimensions int) (*NativeVectorCollection, error) {
	if !nativeName.MatchString(name) || strings.HasPrefix(name, "go_vectorstore_v1_") || vector == "" || dimensions <= 0 {
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
	v := schema.Result.Config.Params.Vectors[vector]
	if len(schema.Result.Config.Params.Sparse) > 0 || v.Size != dimensions || v.Distance != "Cosine" || v.MultivectorConfig != nil || len(schema.Result.Config.Params.Vectors) != 1 {
		return nil, fmt.Errorf("%w: expected one named dense cosine vector", vectordata.ErrSchemaMismatch)
	}
	fields := make(map[string]bool)
	for name, field := range schema.Result.PayloadSchema {
		if field.DataType == "keyword" {
			fields[name] = true
		}
	}
	return &NativeVectorCollection{s, path, vector, dimensions, fields}, nil
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

func (c *NativeVectorCollection) validateVector(vector []float32) error {
	_, err := vectordata.NormalizeModelVector(vector, vectordata.VectorProperty{Dimensions: c.dimensions, Metric: vectordata.DistanceCosine})
	return err
}

// ErrNativeOutcomeUnknown indicates that an attempted native write may have
// reached the server. A read can inspect current data but cannot prove history.
var ErrNativeOutcomeUnknown = errors.New("Qdrant native write outcome unknown")

func (c *NativeVectorCollection) mutate(ctx context.Context, method, path string, body any) error {
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
	if err := c.validateVector(p.Vector); err != nil {
		return err
	}
	record := map[string]any{"id": p.ID, "payload": p.Payload, "vector": map[string][]float32{c.vector: p.Vector}}
	return c.mutate(ctx, http.MethodPut, "/points?wait=true", map[string]any{"points": []any{record}})
}

// GetPoint reads the current point, including its payload and vector.
func (c *NativeVectorCollection) GetPoint(ctx context.Context, id uint64) (NativePoint, error) {
	var out response[struct {
		ID      uint64
		Payload map[string]any
		Vector  map[string][]float32
	}]
	_, err := c.store.request(ctx, http.MethodGet, fmt.Sprintf("%s/points/%d", c.path, id), nil, &out)
	if err != nil {
		return NativePoint{}, err
	}
	p := out.Result
	return NativePoint{p.ID, p.Payload, p.Vector[c.vector]}, nil
}

// DeletePoint deletes a single native point.
func (c *NativeVectorCollection) DeletePoint(ctx context.Context, id uint64) error {
	return c.mutate(ctx, http.MethodPost, "/points/delete?wait=true", map[string]any{"points": []uint64{id}})
}

// SearchKeyword searches the named vector with one server-side indexed keyword
// equality filter. Results retain native Qdrant ordering and score units.
func (c *NativeVectorCollection) SearchKeyword(ctx context.Context, vector []float32, top int, field, value string) ([]NativeHit, error) {
	if !c.keywordFields[field] || !nativeName.MatchString(field) || top <= 0 {
		return nil, fmt.Errorf("%w: expected indexed keyword and positive top", vectordata.ErrInvalidRecord)
	}
	if err := c.validateVector(vector); err != nil {
		return nil, err
	}
	query := map[string]any{"using": c.vector, "query": vector, "limit": top, "filter": map[string]any{"must": []any{map[string]any{"key": field, "match": map[string]string{"value": value}}}}}
	var out response[struct{ Points []NativeHit }]
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
