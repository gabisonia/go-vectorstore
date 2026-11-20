// Package chroma accesses native collections using Chroma's standalone v2 API.
package chroma

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gabisonia/go-vectorstore/internal/nativehttp"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/google/uuid"
)

// NativeSchema selects the tenant, database and collection. Empty tenant and
// database names use Chroma's standalone defaults.
type NativeSchema struct {
	Tenant, Database, Collection string
	Dimensions                   int
}

// NativeCollection is bound to a physical collection ID. Recreating a
// collection under its old name requires attaching a new handle.
type NativeCollection struct {
	api        *nativehttp.Client
	path       string
	dimensions int
}

// NativePoint contains a Chroma document, its metadata and one dense embedding.
type NativePoint struct {
	ID, Document string
	Metadata     map[string]any
	Embedding    []float32
}

// NativeHit carries the server's raw cosine distance.
type NativeHit struct {
	ID       string
	Distance float64
}

// ErrNativeOutcomeUnknown means a write might have reached the server.
var ErrNativeOutcomeUnknown = errors.New("Chroma native write outcome unknown")

// ResponseError retains the service HTTP status.
type ResponseError struct{ StatusCode int }

func (e *ResponseError) Error() string { return fmt.Sprintf("Chroma returned HTTP %d", e.StatusCode) }

// AttachNative checks the existing collection's HNSW cosine configuration and
// its dimension when initialized. For an empty collection, the first stored
// embedding establishes its dimension on the server.
func AttachNative(ctx context.Context, endpoint string, s NativeSchema, t http.RoundTripper) (*NativeCollection, error) {
	if s.Tenant == "" {
		s.Tenant = "default_tenant"
	}
	if s.Database == "" {
		s.Database = "default_database"
	}
	if s.Dimensions <= 0 {
		return nil, vectordata.ErrSchemaMismatch
	}
	for _, name := range []string{s.Tenant, s.Database, s.Collection} {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	prefix := "/api/v2/tenants/" + url.PathEscape(s.Tenant) + "/databases/" + url.PathEscape(s.Database) + "/collections"
	api, err := nativehttp.New(endpoint, prefix, t, func(code int) error { return &ResponseError{code} }, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", vectordata.ErrSchemaMismatch, err)
	}
	var info struct {
		ID            uuid.UUID
		Name          string
		Dimension     *int
		Configuration struct{ HNSW struct{ Space string } } `json:"configuration_json"`
	}
	if err := api.Request(ctx, http.MethodGet, "/"+url.PathEscape(s.Collection), nil, &info, false); err != nil {
		return nil, err
	}
	if info.ID == uuid.Nil || info.Name != s.Collection || info.Configuration.HNSW.Space != "cosine" || (info.Dimension != nil && *info.Dimension != s.Dimensions) {
		return nil, vectordata.ErrSchemaMismatch
	}
	return &NativeCollection{api, "/" + info.ID.String(), s.Dimensions}, nil
}

func (c *NativeCollection) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.dimensions, Metric: vectordata.DistanceCosine})
	return err
}

// Upsert writes a document and embedding. Supplied metadata follows Chroma's
// native update rules for existing keys.
func (c *NativeCollection) Upsert(ctx context.Context, p NativePoint) error {
	if p.ID == "" {
		return vectordata.ErrInvalidRecord
	}
	if err := c.validate(p.Embedding); err != nil {
		return err
	}
	data := map[string]any{"ids": []string{p.ID}, "documents": []string{p.Document}, "embeddings": [][]float32{p.Embedding}}
	if len(p.Metadata) > 0 {
		data["metadatas"] = []map[string]any{p.Metadata}
	}
	return c.api.Request(ctx, http.MethodPost, c.path+"/upsert", data, nil, true)
}

// UpdateDocument updates the text of an existing ID without resending its
// embedding. Chroma can acknowledge updates for absent IDs.
func (c *NativeCollection) UpdateDocument(ctx context.Context, id, text string) error {
	if id == "" {
		return vectordata.ErrInvalidRecord
	}
	return c.api.Request(ctx, http.MethodPost, c.path+"/update", map[string]any{"ids": []string{id}, "documents": []string{text}}, nil, true)
}

// Get returns vectordata.ErrNotFound for a missing ID.
func (c *NativeCollection) Get(ctx context.Context, id string) (NativePoint, error) {
	if id == "" {
		return NativePoint{}, vectordata.ErrInvalidRecord
	}
	var out struct {
		IDs        []string
		Documents  []*string
		Embeddings [][]float32
		Metadatas  []map[string]any
	}
	if err := c.api.Request(ctx, http.MethodPost, c.path+"/get", map[string]any{"ids": []string{id}, "include": []string{"documents", "embeddings", "metadatas"}}, &out, false); err != nil {
		return NativePoint{}, err
	}
	if out.IDs != nil && len(out.IDs) == 0 {
		return NativePoint{}, vectordata.ErrNotFound
	}
	if len(out.IDs) != 1 || out.IDs[0] != id || len(out.Documents) != 1 || out.Documents[0] == nil || len(out.Metadatas) != 1 || len(out.Embeddings) != 1 {
		return NativePoint{}, vectordata.ErrInvalidRecord
	}
	if err := c.validate(out.Embeddings[0]); err != nil {
		return NativePoint{}, err
	}
	return NativePoint{id, *out.Documents[0], out.Metadatas[0], out.Embeddings[0]}, nil
}

// Delete removes the given ID.
func (c *NativeCollection) Delete(ctx context.Context, id string) error {
	if id == "" {
		return vectordata.ErrInvalidRecord
	}
	return c.api.Request(ctx, http.MethodPost, c.path+"/delete", map[string]any{"ids": []string{id}}, nil, true)
}

// Search returns approximate native neighbors ordered by cosine distance.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int) ([]NativeHit, error) {
	if top < 1 {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	var out struct {
		IDs       [][]string
		Distances [][]*float64
	}
	if err := c.api.Request(ctx, http.MethodPost, c.path+"/query", map[string]any{"query_embeddings": [][]float32{v}, "n_results": top, "include": []string{"distances"}}, &out, false); err != nil {
		return nil, err
	}
	if len(out.IDs) != 1 || len(out.Distances) != 1 || len(out.Distances[0]) != len(out.IDs[0]) {
		return nil, vectordata.ErrInvalidRecord
	}
	hits := make([]NativeHit, len(out.IDs[0]))
	for i, id := range out.IDs[0] {
		if id == "" || out.Distances[0][i] == nil {
			return nil, vectordata.ErrInvalidRecord
		}
		hits[i] = NativeHit{id, *out.Distances[0][i]}
	}
	return hits, nil
}
