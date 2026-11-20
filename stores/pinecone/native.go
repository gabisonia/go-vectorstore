// Package pinecone accesses native dense indexes through Pinecone's vectors REST API.
package pinecone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/internal/nativehttp"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema binds one namespace and one string metadata property of a dense cosine index.
type NativeSchema struct {
	Index, Namespace, Text string
	Dimensions             int
}

// NativeIndex operates on a caller-selected index host.
type NativeIndex struct {
	api *nativehttp.Client
	s   NativeSchema
}

// NativeDocument maps one complete record. Upsert replaces the entire metadata map.
type NativeDocument struct {
	ID, Text string
	Vector   []float32
}

// NativeHit reports Pinecone's native similarity score.
type NativeHit struct {
	ID    string
	Score float64
}

// ErrNativeOutcomeUnknown marks a mutation that may have reached the index.
var ErrNativeOutcomeUnknown = errors.New("Pinecone native write outcome unknown")

// ResponseError retains the service's HTTP status.
type ResponseError struct{ StatusCode int }

func (e *ResponseError) Error() string { return fmt.Sprintf("Pinecone HTTP %d", e.StatusCode) }

var indexName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,43}[a-z0-9])?$`)

// Keep the REST API version aligned with the reference SDK.
type versionedTransport struct{ base http.RoundTripper }

func (t versionedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("X-Pinecone-API-Version", "2025-01")
	return t.base.RoundTrip(r)
}

// AttachNative verifies that the control plane reports the supplied data host,
// dimensions and cosine metric. The transport supplies API-key authentication.
func AttachNative(ctx context.Context, controlOrigin, dataOrigin string, s NativeSchema, transport http.RoundTripper) (*NativeIndex, error) {
	if !indexName.MatchString(s.Index) || s.Text == "" || strings.HasPrefix(s.Text, "$") || !utf8.ValidString(s.Text) || !utf8.ValidString(s.Namespace) || s.Dimensions <= 0 {
		return nil, vectordata.ErrSchemaMismatch
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	transport = versionedTransport{transport}
	status := func(code int) error { return &ResponseError{code} }
	control, err := nativehttp.New(controlOrigin, "", transport, status, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, err
	}
	api, err := nativehttp.New(dataOrigin, "", transport, status, ErrNativeOutcomeUnknown)
	if err != nil {
		return nil, err
	}
	var d struct {
		Name, Host, Metric string
		Dimension          int
		VectorType         string `json:"vector_type"`
		Status             struct{ Ready bool }
	}
	if err := control.Request(ctx, http.MethodGet, "/indexes/"+s.Index, nil, &d, false); err != nil {
		return nil, err
	}
	u, err := url.Parse(dataOrigin)
	if err != nil {
		return nil, err
	}
	if d.Name != s.Index || !strings.EqualFold(d.Host, u.Host) || d.Metric != "cosine" || d.Dimension != s.Dimensions || !d.Status.Ready || (d.VectorType != "" && d.VectorType != "dense") {
		return nil, fmt.Errorf("%w: index host, readiness, dimension or metric differs", vectordata.ErrSchemaMismatch)
	}
	return &NativeIndex{api, s}, nil
}

func validID(id string) bool { return id != "" && len(id) <= 512 && utf8.ValidString(id) }
func (c *NativeIndex) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.s.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}

// Upsert replaces one record in the bound namespace.
func (c *NativeIndex) Upsert(ctx context.Context, p NativeDocument) error {
	if !validID(p.ID) || !utf8.ValidString(p.Text) {
		return vectordata.ErrInvalidRecord
	}
	if err := c.validate(p.Vector); err != nil {
		return err
	}
	record := map[string]any{"id": p.ID, "values": p.Vector, "metadata": map[string]string{c.s.Text: p.Text}}
	var ack struct{ UpsertedCount int }
	if err := c.api.Request(ctx, http.MethodPost, "/vectors/upsert", map[string]any{"namespace": c.s.Namespace, "vectors": []any{record}}, &ack, true); err != nil {
		return err
	}
	if ack.UpsertedCount != 1 {
		return ErrNativeOutcomeUnknown
	}
	return nil
}

// Get returns ErrNotFound if the namespace has no record for the requested ID.
func (c *NativeIndex) Get(ctx context.Context, id string) (NativeDocument, error) {
	if !validID(id) {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	params := url.Values{"namespace": {c.s.Namespace}, "ids": {id}}
	var reply struct {
		Namespace string
		Vectors   map[string]struct {
			ID       string
			Values   []float32
			Metadata map[string]any
		}
	}
	if err := c.api.Request(ctx, http.MethodGet, "/vectors/fetch?"+params.Encode(), nil, &reply, false); err != nil {
		return NativeDocument{}, err
	}
	if reply.Namespace != c.s.Namespace || reply.Vectors == nil {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	p, ok := reply.Vectors[id]
	if !ok {
		if len(reply.Vectors) != 0 {
			return NativeDocument{}, vectordata.ErrInvalidRecord
		}
		return NativeDocument{}, vectordata.ErrNotFound
	}
	text, ok := p.Metadata[c.s.Text].(string)
	if p.ID != id || !ok {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	if err := c.validate(p.Values); err != nil {
		return NativeDocument{}, err
	}
	return NativeDocument{p.ID, text, p.Values}, nil
}

// Delete requests deletion of exactly one ID in the bound namespace.
func (c *NativeIndex) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return vectordata.ErrInvalidRecord
	}
	var reply json.RawMessage
	if err := c.api.Request(ctx, http.MethodPost, "/vectors/delete", map[string]any{"namespace": c.s.Namespace, "ids": []string{id}}, &reply, true); err != nil {
		return err
	}
	// Pinecone Local encodes its empty response as a JSON string. The REST
	// contract specifies an empty object. Validate either acknowledgement.
	var object map[string]json.RawMessage
	if string(reply) != `""` && (json.Unmarshal(reply, &object) != nil || object == nil || len(object) != 0) {
		return ErrNativeOutcomeUnknown
	}
	return nil
}

// Search applies an optional metadata equality prefilter before cosine ranking.
func (c *NativeIndex) Search(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	if top < 1 || top > 10000 || (textEquals != nil && !utf8.ValidString(*textEquals)) {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	query := map[string]any{"namespace": c.s.Namespace, "vector": v, "topK": top, "includeValues": false, "includeMetadata": false}
	if textEquals != nil {
		query["filter"] = map[string]any{c.s.Text: map[string]string{"$eq": *textEquals}}
	}
	var reply struct {
		Namespace string
		Matches   []struct {
			ID    string
			Score *float64
		}
	}
	if err := c.api.Request(ctx, http.MethodPost, "/query", query, &reply, false); err != nil {
		return nil, err
	}
	if reply.Namespace != c.s.Namespace || reply.Matches == nil {
		return nil, vectordata.ErrInvalidRecord
	}
	hits := make([]NativeHit, 0, len(reply.Matches))
	for _, m := range reply.Matches {
		if !validID(m.ID) || m.Score == nil {
			return nil, vectordata.ErrInvalidRecord
		}
		hits = append(hits, NativeHit{m.ID, *m.Score})
	}
	return hits, nil
}
