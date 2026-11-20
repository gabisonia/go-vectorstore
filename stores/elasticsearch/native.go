// Package elasticsearch accesses existing Elasticsearch 8.x dense-vector indexes.
package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/gabisonia/go-vectorstore/internal/nativehttp"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema maps a concrete index, one string field and an indexed float32
// cosine vector. The document key is Elasticsearch's _id.
type NativeSchema struct {
	Index, Text, Vector string
	Dimensions          int
}

// NativeIndex uses the caller's transport for TLS and authentication.
type NativeIndex struct {
	api    *nativehttp.Client
	schema NativeSchema
}

// NativeDocument holds the complete source of a two-field document.
type NativeDocument struct {
	ID, Text string
	Vector   []float32
}

// NativeHit returns Elasticsearch's higher-is-better _score.
type NativeHit struct {
	ID    string
	Score float64
}

// ErrNativeOutcomeUnknown indicates that a write may have reached Elasticsearch.
var ErrNativeOutcomeUnknown = errors.New("Elasticsearch native write outcome unknown")

// ResponseError retains the server's HTTP status code.
type ResponseError struct{ StatusCode int }

func (e *ResponseError) Error() string {
	return fmt.Sprintf("Elasticsearch returned HTTP %d", e.StatusCode)
}

var indexName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
var fieldName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// AttachNative checks the concrete mapping and requires the source fields to
// be retrievable. It does not create an index or change its refresh settings.
func AttachNative(ctx context.Context, endpoint string, s NativeSchema, transport http.RoundTripper) (*NativeIndex, error) {
	if !indexName.MatchString(s.Index) || !fieldName.MatchString(s.Text) || !fieldName.MatchString(s.Vector) || s.Text == s.Vector || s.Dimensions < 1 {
		return nil, vectordata.ErrSchemaMismatch
	}
	api, err := nativehttp.New(endpoint, "/"+s.Index, transport, func(code int) error { return &ResponseError{code} }, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", vectordata.ErrSchemaMismatch, err)
	}
	var indices map[string]struct {
		Mappings struct {
			Source struct {
				Enabled            *bool
				Includes, Excludes []string
			} `json:"_source"`
			Routing    struct{ Required bool } `json:"_routing"`
			Properties map[string]struct {
				Type        string
				Dims        int
				Index       *bool
				Similarity  string
				ElementType string `json:"element_type"`
			}
		}
	}
	if err := api.Request(ctx, http.MethodGet, "/_mapping", nil, &indices, false); err != nil {
		return nil, err
	}
	m := indices[s.Index].Mappings
	if len(indices) != 1 || len(m.Properties) != 2 || m.Routing.Required || (m.Source.Enabled != nil && !*m.Source.Enabled) || len(m.Source.Includes)+len(m.Source.Excludes) != 0 {
		return nil, fmt.Errorf("%w: expected a concrete two-field index with complete _source", vectordata.ErrSchemaMismatch)
	}
	text, v := m.Properties[s.Text], m.Properties[s.Vector]
	if (text.Type != "text" && text.Type != "keyword") || v.Type != "dense_vector" || v.Dims != s.Dimensions || (v.Index != nil && !*v.Index) || (v.Similarity != "" && v.Similarity != "cosine") || (v.ElementType != "" && v.ElementType != "float") {
		return nil, vectordata.ErrSchemaMismatch
	}
	return &NativeIndex{api, s}, nil
}

func pathFor(id string) (string, error) {
	if id == "" || id == "." || id == ".." || len(id) > 512 {
		return "", vectordata.ErrInvalidRecord
	}
	return "/_doc/" + url.PathEscape(id), nil
}

func (c *NativeIndex) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.schema.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}

func (c *NativeIndex) write(ctx context.Context, method, id string, body any) error {
	path, err := pathFor(id)
	if err != nil {
		return err
	}
	var reply struct {
		ID     string `json:"_id"`
		Result string
	}
	if err := c.api.Request(ctx, method, path+"?refresh=wait_for", body, &reply, true); err != nil {
		return err
	}
	if reply.ID != id {
		return ErrNativeOutcomeUnknown
	}
	if method == http.MethodDelete && reply.Result == "deleted" || method == http.MethodPut && (reply.Result == "created" || reply.Result == "updated") {
		return nil
	}
	return ErrNativeOutcomeUnknown
}

// Upsert replaces one document and waits for its search visibility.
func (c *NativeIndex) Upsert(ctx context.Context, p NativeDocument) error {
	if err := c.validate(p.Vector); err != nil {
		return err
	}
	return c.write(ctx, http.MethodPut, p.ID, map[string]any{c.schema.Text: p.Text, c.schema.Vector: p.Vector})
}

// Delete removes a document and waits for refresh. A missing ID returns HTTP 404.
func (c *NativeIndex) Delete(ctx context.Context, id string) error {
	return c.write(ctx, http.MethodDelete, id, nil)
}

// Get performs a realtime document lookup.
func (c *NativeIndex) Get(ctx context.Context, id string) (NativeDocument, error) {
	path, err := pathFor(id)
	if err != nil {
		return NativeDocument{}, err
	}
	var doc struct {
		ID     string                     `json:"_id"`
		Source map[string]json.RawMessage `json:"_source"`
	}
	if err := c.api.Request(ctx, http.MethodGet, path, nil, &doc, false); err != nil {
		return NativeDocument{}, err
	}
	var text *string
	var vector []float32
	if doc.ID != id || json.Unmarshal(doc.Source[c.schema.Text], &text) != nil || text == nil || json.Unmarshal(doc.Source[c.schema.Vector], &vector) != nil {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	if err := c.validate(vector); err != nil {
		return NativeDocument{}, err
	}
	return NativeDocument{id, *text, vector}, nil
}

// Search performs native kNN retrieval. Elasticsearch limits numCandidates to
// 10,000 and requires at least top candidates.
func (c *NativeIndex) Search(ctx context.Context, v []float32, top, numCandidates int) ([]NativeHit, error) {
	if top < 1 || numCandidates < top || numCandidates > 10000 {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	query := map[string]any{"size": top, "_source": false, "knn": map[string]any{"field": c.schema.Vector, "query_vector": v, "k": top, "num_candidates": numCandidates}}
	var result struct {
		TimedOut bool                 `json:"timed_out"`
		Shards   struct{ Failed int } `json:"_shards"`
		Hits     struct {
			Hits []struct {
				ID    string   `json:"_id"`
				Score *float64 `json:"_score"`
			}
		}
	}
	if err := c.api.Request(ctx, http.MethodPost, "/_search?allow_partial_search_results=false", query, &result, false); err != nil {
		return nil, err
	}
	if result.TimedOut || result.Shards.Failed > 0 {
		return nil, errors.New("incomplete Elasticsearch search")
	}
	if result.Hits.Hits == nil {
		return nil, vectordata.ErrInvalidRecord
	}
	hits := make([]NativeHit, 0, len(result.Hits.Hits))
	for _, h := range result.Hits.Hits {
		if h.ID == "" || h.Score == nil {
			return nil, vectordata.ErrInvalidRecord
		}
		hits = append(hits, NativeHit{h.ID, *h.Score})
	}
	return hits, nil
}
