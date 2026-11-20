// Package azuresearch accesses existing Azure AI Search vector indexes.
package azuresearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const apiVersion = "2025-09-01"

// NativeSchema maps the three physical fields of a string-keyed dense cosine index.
type NativeSchema struct {
	Key, Text, Vector string
	Dimensions        int
}

// Options configures a connection to an existing service. Token supplies a
// current Search data-plane bearer token for each request.
type Options struct {
	Endpoint  string
	Token     func(context.Context) (string, error)
	Transport http.RoundTripper
}

// NativeIndex operates on one externally managed Azure AI Search index.
type NativeIndex struct {
	base   string
	schema NativeSchema
	client *http.Client
	token  func(context.Context) (string, error)
}

// NativeDocument replaces all three fields of a document on upload.
type NativeDocument struct {
	ID, Text string
	Vector   []float32
}

// NativeHit carries the service score and the selected retrievable text.
type NativeHit struct {
	ID, Text string
	Score    float64
}

// ErrNativeOutcomeUnknown means an indexing action's outcome could not be established.
var ErrNativeOutcomeUnknown = errors.New("Azure AI Search indexing outcome unknown")

var fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
var indexName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

// ResponseError retains the HTTP response status for callers inspecting service errors.
type ResponseError struct{ StatusCode int }

func (e *ResponseError) Error() string {
	return fmt.Sprintf("Azure AI Search returned HTTP %d", e.StatusCode)
}

// AttachNative validates a three-field index and its cosine vector profile.
func AttachNative(ctx context.Context, opts Options, index string, s NativeSchema) (*NativeIndex, error) {
	u, err := url.Parse(opts.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" || opts.Token == nil || !indexName.MatchString(index) {
		return nil, vectordata.ErrSchemaMismatch
	}
	if !fieldName.MatchString(s.Key) || !fieldName.MatchString(s.Text) || !fieldName.MatchString(s.Vector) || s.Key == s.Text || s.Key == s.Vector || s.Text == s.Vector || s.Dimensions < 1 {
		return nil, vectordata.ErrSchemaMismatch
	}
	c := &NativeIndex{base: "https://" + u.Host + "/indexes/" + index, schema: s, token: opts.Token, client: &http.Client{Transport: opts.Transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var definition struct {
		Fields []struct {
			Name, Type, VectorSearchProfile string
			Key, Searchable, Retrievable    bool
			Dimensions                      int
		}
		VectorSearch struct {
			Profiles   []struct{ Name, Algorithm string }
			Algorithms []struct {
				Name, Kind                              string
				HnswParameters, ExhaustiveKnnParameters struct{ Metric string }
			}
		}
	}
	if err := c.request(ctx, http.MethodGet, "", nil, &definition, false); err != nil {
		return nil, err
	}
	key, text, profile := false, false, ""
	for _, f := range definition.Fields {
		switch f.Name {
		case s.Key:
			key = f.Type == "Edm.String" && f.Key && f.Retrievable
		case s.Text:
			text = f.Type == "Edm.String" && f.Retrievable
		case s.Vector:
			if f.Type == "Collection(Edm.Single)" && f.Searchable && f.Dimensions == s.Dimensions {
				profile = f.VectorSearchProfile
			}
		}
	}
	if len(definition.Fields) != 3 || !key || !text || profile == "" {
		return nil, vectordata.ErrSchemaMismatch
	}
	for _, p := range definition.VectorSearch.Profiles {
		if p.Name != profile {
			continue
		}
		for _, a := range definition.VectorSearch.Algorithms {
			if a.Name != p.Algorithm {
				continue
			}
			if (a.Kind == "hnsw" && a.HnswParameters.Metric == "cosine") || (a.Kind == "exhaustiveKnn" && a.ExhaustiveKnnParameters.Metric == "cosine") {
				return c, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: expected a cosine vector search profile", vectordata.ErrSchemaMismatch)
}

func (c *NativeIndex) request(ctx context.Context, method, path string, body, out any, write bool) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	target, err := url.Parse(c.base + path)
	if err != nil {
		return err
	}
	query := target.Query()
	query.Set("api-version", apiVersion)
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("missing Azure Search token")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			err = &ResponseError{StatusCode: resp.StatusCode}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 {
				return err
			}
		} else {
			err = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
		}
	}
	if write && err != nil {
		return errors.Join(ErrNativeOutcomeUnknown, err)
	}
	return err
}

func (c *NativeIndex) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.schema.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}

func (c *NativeIndex) action(ctx context.Context, id string, doc map[string]any) error {
	if id == "" {
		return vectordata.ErrInvalidRecord
	}
	doc[c.schema.Key] = id
	var result struct {
		Value []struct {
			Key        string
			Status     *bool
			StatusCode int
		}
	}
	if err := c.request(ctx, http.MethodPost, "/docs/index", map[string]any{"value": []any{doc}}, &result, true); err != nil {
		return err
	}
	if len(result.Value) != 1 || result.Value[0].Key != id || result.Value[0].Status == nil {
		return ErrNativeOutcomeUnknown
	}
	if !*result.Value[0].Status {
		return fmt.Errorf("Azure AI Search rejected document action with status %d", result.Value[0].StatusCode)
	}
	return nil
}

// Upsert uploads one complete document. An acknowledged upload can take time
// to appear in search results.
func (c *NativeIndex) Upsert(ctx context.Context, p NativeDocument) error {
	if err := c.validate(p.Vector); err != nil {
		return err
	}
	return c.action(ctx, p.ID, map[string]any{"@search.action": "upload", c.schema.Text: p.Text, c.schema.Vector: p.Vector})
}

// Delete requests deletion of a single document.
func (c *NativeIndex) Delete(ctx context.Context, id string) error {
	return c.action(ctx, id, map[string]any{"@search.action": "delete"})
}

// GetText retrieves the selected text field by document key. It does not
// require the vector field to be retrievable.
func (c *NativeIndex) GetText(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", vectordata.ErrInvalidRecord
	}
	var result map[string]json.RawMessage
	path := "/docs/" + url.PathEscape(id) + "?" + url.Values{"$select": {c.schema.Text}}.Encode()
	if err := c.request(ctx, http.MethodGet, path, nil, &result, false); err != nil {
		return "", err
	}
	var value *string
	if err := json.Unmarshal(result[c.schema.Text], &value); err != nil || value == nil {
		return "", vectordata.ErrInvalidRecord
	}
	return *value, nil
}

// Search returns the service's nearest-neighbor ordering and scores.
func (c *NativeIndex) Search(ctx context.Context, v []float32, top int) ([]NativeHit, error) {
	if top <= 0 {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	body := map[string]any{"select": c.schema.Key + "," + c.schema.Text, "top": top, "vectorQueries": []any{map[string]any{"kind": "vector", "vector": v, "fields": c.schema.Vector, "k": top}}}
	var result struct{ Value []map[string]json.RawMessage }
	if err := c.request(ctx, http.MethodPost, "/docs/search", body, &result, false); err != nil {
		return nil, err
	}
	if result.Value == nil {
		return nil, vectordata.ErrInvalidRecord
	}
	hits := make([]NativeHit, 0, len(result.Value))
	for _, row := range result.Value {
		var hit NativeHit
		if json.Unmarshal(row[c.schema.Key], &hit.ID) != nil || hit.ID == "" || json.Unmarshal(row[c.schema.Text], &hit.Text) != nil || json.Unmarshal(row["@search.score"], &hit.Score) != nil || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
			return nil, vectordata.ErrInvalidRecord
		}
		hits = append(hits, hit)
	}
	return hits, nil
}
