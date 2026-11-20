// Package cosmosnosql accesses native Cosmos DB for NoSQL containers partitioned by /id.
package cosmosnosql

import (
	"cmp"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema maps a text field and float32 DiskANN cosine vector.
type NativeSchema struct {
	Text, Vector string
	Dimensions   int
}

// NativeDocument is one item with a string id that also serves as its partition key.
type NativeDocument struct {
	ID, Text string
	Vector   []float32
}

// NativeHit reports the service's cosine score.
type NativeHit struct {
	ID    string
	Score float64
}

// NativeCollection uses the caller's gateway client and consistency settings.
type NativeCollection struct {
	container *azcosmos.ContainerClient
	schema    NativeSchema
}

// ErrNativeOutcomeUnknown marks an unconfirmed write.
var ErrNativeOutcomeUnknown = errors.New("Cosmos NoSQL write outcome unknown")
var fieldName = regexp.MustCompile("^[A-Za-z][A-Za-z0-9_]*$")

// AttachNative reads the partition key, embedding policy and index policy.
func AttachNative(ctx context.Context, container *azcosmos.ContainerClient, s NativeSchema) (*NativeCollection, error) {
	return attach(ctx, container, s, nil)
}

// AttachProvisioned accepts the creation policies for gateways that omit vector
// metadata from container reads. The caller must own or verify these policies
// for this container. Visible vector policies always take precedence.
func AttachProvisioned(ctx context.Context, container *azcosmos.ContainerClient, s NativeSchema, creation azcosmos.ContainerProperties) (*NativeCollection, error) {
	return attach(ctx, container, s, &creation)
}

func attach(ctx context.Context, container *azcosmos.ContainerClient, s NativeSchema, creation *azcosmos.ContainerProperties) (*NativeCollection, error) {
	if container == nil || s.Dimensions < 1 || !fieldName.MatchString(s.Text) || !fieldName.MatchString(s.Vector) || s.Text == s.Vector || s.Text == "id" || s.Vector == "id" {
		return nil, vectordata.ErrSchemaMismatch
	}
	resp, err := container.Read(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := s.checkPolicies(resp.ContainerProperties, creation); err != nil {
		return nil, err
	}
	return &NativeCollection{container, s}, nil
}

func (s NativeSchema) checkPolicies(observed, creation *azcosmos.ContainerProperties) error {
	if observed == nil {
		return fmt.Errorf("%w: missing container properties", vectordata.ErrSchemaMismatch)
	}
	p := *observed
	if !slices.Equal(p.PartitionKeyDefinition.Paths, []string{"/id"}) {
		return fmt.Errorf("%w: partition paths %q", vectordata.ErrSchemaMismatch, p.PartitionKeyDefinition.Paths)
	}
	if creation != nil {
		if creation.ID != p.ID || !slices.Equal(creation.PartitionKeyDefinition.Paths, p.PartitionKeyDefinition.Paths) {
			return fmt.Errorf("%w: creation policies identify another container", vectordata.ErrSchemaMismatch)
		}
		if p.VectorEmbeddingPolicy == nil {
			p.VectorEmbeddingPolicy = creation.VectorEmbeddingPolicy
		}
		if p.IndexingPolicy == nil || p.IndexingPolicy.VectorIndexes == nil {
			p.IndexingPolicy = creation.IndexingPolicy
		}
	}
	if p.VectorEmbeddingPolicy == nil || p.IndexingPolicy == nil {
		return fmt.Errorf("%w: missing policy (embedding present %t, indexing present %t)", vectordata.ErrSchemaMismatch, p.VectorEmbeddingPolicy != nil, p.IndexingPolicy != nil)
	}
	path := "/" + s.Vector
	embedded := slices.ContainsFunc(p.VectorEmbeddingPolicy.VectorEmbeddings, func(v azcosmos.VectorEmbedding) bool {
		return v.Path == path && int(v.Dimensions) == s.Dimensions && strings.EqualFold(string(v.DataType), "float32") && strings.EqualFold(string(v.DistanceFunction), "cosine")
	})
	indexed := slices.ContainsFunc(p.IndexingPolicy.VectorIndexes, func(v azcosmos.VectorIndex) bool {
		return v.Path == path && strings.EqualFold(string(v.Type), "diskANN")
	})
	if !embedded || !indexed {
		return fmt.Errorf("%w: expected %s float32 cosine DiskANN dimension %d, found embeddings %+v and indexes %+v", vectordata.ErrSchemaMismatch, path, s.Dimensions, p.VectorEmbeddingPolicy.VectorEmbeddings, p.IndexingPolicy.VectorIndexes)
	}
	return nil
}

func validID(id string) bool {
	return len(id) > 0 && len(id) <= 1023 && utf8.ValidString(id) && !strings.ContainsAny(id, "/\\?#")
}
func (c *NativeCollection) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.schema.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}
func hasStatus(err error, status int) bool {
	var e *azcore.ResponseError
	return errors.As(err, &e) && e.StatusCode == status
}

func writeError(err error) error {
	if err == nil {
		return nil
	}
	var e *azcore.ResponseError
	if errors.As(err, &e) && e.StatusCode >= 400 && e.StatusCode < 500 && e.StatusCode != http.StatusRequestTimeout {
		return err
	}
	return errors.Join(ErrNativeOutcomeUnknown, err)
}

// The generic Azure retry layer can replay a write after a transport loses
// the acknowledgement. Cosmos-specific routing still belongs to the SDK.
func writeContext(ctx context.Context) context.Context {
	return policy.WithRetryOptions(ctx, policy.RetryOptions{MaxRetries: -1})
}

// Upsert replaces the entire item with the three mapped properties.
func (c *NativeCollection) Upsert(ctx context.Context, d NativeDocument) error {
	if !validID(d.ID) || !utf8.ValidString(d.Text) {
		return vectordata.ErrInvalidRecord
	}
	if err := c.validate(d.Vector); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"id": d.ID, c.schema.Text: d.Text, c.schema.Vector: d.Vector})
	if err != nil {
		return err
	}
	_, err = c.container.UpsertItem(writeContext(ctx), azcosmos.NewPartitionKeyString(d.ID), body, nil)
	return writeError(err)
}

// Get performs a point read and includes the stored vector.
func (c *NativeCollection) Get(ctx context.Context, id string) (NativeDocument, error) {
	if !validID(id) {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	resp, err := c.container.ReadItem(ctx, azcosmos.NewPartitionKeyString(id), id, nil)
	if hasStatus(err, 404) {
		return NativeDocument{}, vectordata.ErrNotFound
	}
	if err != nil {
		return NativeDocument{}, err
	}
	var obj map[string]json.RawMessage
	var gotID string
	var text *string
	var vector []*float32
	if json.Unmarshal(resp.Value, &obj) != nil || json.Unmarshal(obj["id"], &gotID) != nil || gotID != id || json.Unmarshal(obj[c.schema.Text], &text) != nil || text == nil || json.Unmarshal(obj[c.schema.Vector], &vector) != nil {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	v := make([]float32, len(vector))
	for i, n := range vector {
		if n == nil {
			return NativeDocument{}, vectordata.ErrInvalidRecord
		}
		v[i] = *n
	}
	if err := c.validate(v); err != nil {
		return NativeDocument{}, err
	}
	return NativeDocument{id, *text, v}, nil
}

// Delete removes an item. Deleting a missing id succeeds.
func (c *NativeCollection) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return vectordata.ErrInvalidRecord
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.container.DeleteItem(writeContext(ctx), azcosmos.NewPartitionKeyString(id), id, nil)
	if hasStatus(err, 404) {
		return nil
	}
	return writeError(err)
}

// Search requests a server-ranked page, optionally matching the text field.
// Gateway cross-partition query errors are returned without partial results.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	return c.search(ctx, v, top, textEquals, false)
}

// SearchExact projects a brute-force cosine score for every matching item and
// returns the highest scores, breaking ties by ascending ID. The scan consumes
// request units for all eligible rows and follows the client's consistency
// settings across pages. Concurrent writes can change what the scan observes.
func (c *NativeCollection) SearchExact(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	return c.search(ctx, v, top, textEquals, true)
}

func (c *NativeCollection) search(ctx context.Context, v []float32, top int, textEquals *string, exact bool) ([]NativeHit, error) {
	if top < 1 || (textEquals != nil && !utf8.ValidString(*textEquals)) {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := []azcosmos.QueryParameter{{Name: "@v", Value: slices.Clone(v)}}
	where := ""
	if textEquals != nil {
		where = fmt.Sprintf(" WHERE c[%q] = @text", c.schema.Text)
		params = append(params, azcosmos.QueryParameter{Name: "@text", Value: *textEquals})
	}

	opts := azcosmos.QueryOptions{QueryParameters: params}
	var query string
	if exact {
		query = fmt.Sprintf("SELECT c.id, VectorDistance(c[%q], @v, true) AS score FROM c%s", c.schema.Vector, where)
		opts.PageSizeHint = 128
	} else {
		score := fmt.Sprintf("VectorDistance(c[%q], @v)", c.schema.Vector)
		query = fmt.Sprintf("SELECT TOP %d c.id, %s AS score FROM c%s ORDER BY %s", top, score, where, score)
	}
	pager := c.container.NewQueryItemsPager(query, azcosmos.NewPartitionKey(), &opts)
	return collectHits(ctx, pager, top, exact)
}

type hitPager interface {
	More() bool
	NextPage(context.Context) (azcosmos.QueryItemsResponse, error)
}

func compareHits(a, b NativeHit) int {
	if order := cmp.Compare(b.Score, a.Score); order != 0 {
		return order
	}
	return strings.Compare(a.ID, b.ID)
}

// The root is the worst retained hit, including the ID tie-break.
type hitHeap []NativeHit

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return compareHits(h[i], h[j]) > 0 }
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(x any)        { *h = append(*h, x.(NativeHit)) }
func (h *hitHeap) Pop() any          { i := len(*h) - 1; x := (*h)[i]; *h = (*h)[:i]; return x }

func collectHits(ctx context.Context, pager hitPager, top int, exact bool) ([]NativeHit, error) {
	var hits hitHeap
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var hit struct {
				ID    string
				Score *float64
			}
			if err := json.Unmarshal(item, &hit); err != nil {
				return nil, err
			}
			if !validID(hit.ID) || hit.Score == nil {
				return nil, vectordata.ErrInvalidRecord
			}
			row := NativeHit{hit.ID, *hit.Score}
			if !exact {
				if len(hits) >= top {
					return nil, vectordata.ErrInvalidRecord
				}
				hits = append(hits, row)
			} else if len(hits) < top {
				heap.Push(&hits, row)
			} else if compareHits(row, hits[0]) < 0 {
				hits[0] = row
				heap.Fix(&hits, 0)
			}
		}
	}
	if exact {
		slices.SortFunc(hits, compareHits)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}
