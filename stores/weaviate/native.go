// Package weaviate accesses existing Weaviate collections with caller-supplied vectors.
package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"net/http"

	"regexp"
	"strconv"

	"github.com/gabisonia/go-vectorstore/internal/nativehttp"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/google/uuid"
)

// NativeSchema maps one text property and one named dense cosine vector.
// Dimensions are a caller contract: Weaviate's class schema omits this value.
type NativeSchema struct {
	Class, Text, Vector string
	Dimensions          int
}

// NativeCollection uses the caller's transport for authentication and TLS.
type NativeCollection struct {
	api    *nativehttp.Client
	schema NativeSchema
}

// NativePoint is a complete record in the attached collection.
type NativePoint struct {
	ID     uuid.UUID
	Text   string
	Vector []float32
}

// NativeHit reports the server's raw cosine distance.
type NativeHit struct {
	ID       uuid.UUID
	Distance float64
}

// ErrNativeOutcomeUnknown indicates that a mutation may have reached the server.
var ErrNativeOutcomeUnknown = errors.New("Weaviate native write outcome unknown")

var className = regexp.MustCompile(`^[A-Z][_0-9A-Za-z]*$`)
var fieldName = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

// AttachNative checks the physical class and vector configuration before writes.
// It accepts a single-tenant class with one text property and one self-provided vector.
func AttachNative(ctx context.Context, endpoint string, s NativeSchema, transport http.RoundTripper) (*NativeCollection, error) {
	if !className.MatchString(s.Class) || !fieldName.MatchString(s.Text) || !fieldName.MatchString(s.Vector) || s.Dimensions < 1 {
		return nil, vectordata.ErrSchemaMismatch
	}
	api, err := nativehttp.New(endpoint, "/v1", transport, func(code int) error { return &ResponseError{code} }, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", vectordata.ErrSchemaMismatch, err)
	}
	c := &NativeCollection{api, s}
	var schema struct {
		Class              string
		MultiTenancyConfig struct{ Enabled bool }
		Properties         []struct {
			Name     string
			DataType []string
		}
		VectorConfig map[string]struct {
			Vectorizer        map[string]json.RawMessage
			VectorIndexType   string
			VectorIndexConfig struct {
				Distance    string
				Multivector struct{ Enabled bool }
			}
		}
	}
	if err := c.api.Request(ctx, http.MethodGet, "/schema/"+s.Class, nil, &schema, false); err != nil {
		return nil, err
	}
	if schema.Class != s.Class || schema.MultiTenancyConfig.Enabled || len(schema.Properties) != 1 || len(schema.VectorConfig) != 1 {
		return nil, fmt.Errorf("%w: expected a single-tenant class with one property and one named vector", vectordata.ErrSchemaMismatch)
	}
	property := schema.Properties[0]
	if property.Name != s.Text || len(property.DataType) != 1 || property.DataType[0] != "text" {
		return nil, fmt.Errorf("%w: expected text property %q", vectordata.ErrSchemaMismatch, s.Text)
	}
	v := schema.VectorConfig[s.Vector]
	if _, selfProvided := v.Vectorizer["none"]; !selfProvided || len(v.Vectorizer) != 1 {
		return nil, fmt.Errorf("%w: expected self-provided vector %q", vectordata.ErrSchemaMismatch, s.Vector)
	}
	if (v.VectorIndexType != "flat" && v.VectorIndexType != "hnsw") || v.VectorIndexConfig.Distance != "cosine" || v.VectorIndexConfig.Multivector.Enabled {
		return nil, fmt.Errorf("%w: expected a dense flat or HNSW cosine index", vectordata.ErrSchemaMismatch)
	}
	return c, nil
}

// ResponseError retains the service HTTP status code.
type ResponseError struct{ StatusCode int }

func (e *ResponseError) Error() string { return fmt.Sprintf("Weaviate returned HTTP %d", e.StatusCode) }

func (c *NativeCollection) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.schema.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}

// Upsert replaces a single object using Weaviate's native batch endpoint.
func (c *NativeCollection) Upsert(ctx context.Context, p NativePoint) error {
	if err := c.validate(p.Vector); err != nil {
		return err
	}
	obj := map[string]any{"class": c.schema.Class, "id": p.ID, "properties": map[string]string{c.schema.Text: p.Text}, "vectors": map[string][]float32{c.schema.Vector: p.Vector}}
	var reply []struct {
		ID     *uuid.UUID
		Result struct {
			Status string
			Errors *struct{ Error []struct{ Message string } }
		}
	}
	if err := c.api.Request(ctx, http.MethodPost, "/batch/objects", map[string]any{"objects": []any{obj}}, &reply, true); err != nil {
		return err
	}
	if len(reply) != 1 || reply[0].ID == nil || *reply[0].ID != p.ID {
		return ErrNativeOutcomeUnknown
	}
	switch reply[0].Result.Status {
	case "SUCCESS":
		return nil
	case "FAILED":
		if e := reply[0].Result.Errors; e != nil && len(e.Error) > 0 {
			return fmt.Errorf("Weaviate rejected object: %s", e.Error[0].Message)
		}
		return errors.New("Weaviate rejected object")
	default:
		return ErrNativeOutcomeUnknown
	}
}

// Get reads the text and named vector of a UUID-keyed object.
func (c *NativeCollection) Get(ctx context.Context, id uuid.UUID) (NativePoint, error) {
	var obj struct {
		ID         uuid.UUID
		Properties map[string]*string
		Vectors    map[string][]float32
	}
	if err := c.api.Request(ctx, http.MethodGet, "/objects/"+c.schema.Class+"/"+id.String()+"?include=vector", nil, &obj, false); err != nil {
		return NativePoint{}, err
	}
	text := obj.Properties[c.schema.Text]
	v := obj.Vectors[c.schema.Vector]
	if text == nil || obj.ID != id {
		return NativePoint{}, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return NativePoint{}, err
	}
	return NativePoint{id, *text, v}, nil
}

// Delete removes one object from the named class.
func (c *NativeCollection) Delete(ctx context.Context, id uuid.UUID) error {
	return c.api.Request(ctx, http.MethodDelete, "/objects/"+c.schema.Class+"/"+id.String(), nil, nil, true)
}

// Search returns up to top native hits ordered by cosine distance.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int) ([]NativeHit, error) {
	if top < 1 {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("{Get{%s(limit:%d,nearVector:{vector:%s,targetVectors:[%s]}){_additional{id distance}}}}", c.schema.Class, top, raw, strconv.Quote(c.schema.Vector))
	var reply struct {
		Data struct {
			Get map[string][]struct {
				Additional struct {
					ID       *uuid.UUID
					Distance *float64
				} `json:"_additional"`
			}
		}
		Errors []struct{ Message string }
	}
	if err := c.api.Request(ctx, http.MethodPost, "/graphql", map[string]string{"query": query}, &reply, false); err != nil {
		return nil, err
	}
	if len(reply.Errors) > 0 {
		return nil, fmt.Errorf("Weaviate query: %s", reply.Errors[0].Message)
	}
	records := reply.Data.Get[c.schema.Class]
	if records == nil {
		return nil, vectordata.ErrInvalidRecord
	}
	hits := make([]NativeHit, 0, len(records))
	for _, r := range records {
		if r.Additional.ID == nil || r.Additional.Distance == nil {
			return nil, vectordata.ErrInvalidRecord
		}
		hits = append(hits, NativeHit{*r.Additional.ID, *r.Additional.Distance})
	}
	return hits, nil
}
