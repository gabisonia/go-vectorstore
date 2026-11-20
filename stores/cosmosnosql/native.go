// Package cosmosnosql accesses native Cosmos DB for NoSQL containers partitioned by /id.
package cosmosnosql

import (
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
	if container == nil || s.Dimensions < 1 || !fieldName.MatchString(s.Text) || !fieldName.MatchString(s.Vector) || s.Text == s.Vector || s.Text == "id" || s.Vector == "id" {
		return nil, vectordata.ErrSchemaMismatch
	}
	resp, err := container.Read(ctx, nil)
	if err != nil {
		return nil, err
	}
	p := resp.ContainerProperties
	if p == nil || !slices.Equal(p.PartitionKeyDefinition.Paths, []string{"/id"}) || p.VectorEmbeddingPolicy == nil || p.IndexingPolicy == nil {
		return nil, vectordata.ErrSchemaMismatch
	}
	path := "/" + s.Vector
	embedded := slices.ContainsFunc(p.VectorEmbeddingPolicy.VectorEmbeddings, func(v azcosmos.VectorEmbedding) bool {
		return v.Path == path && int(v.Dimensions) == s.Dimensions && v.DataType == "float32" && v.DistanceFunction == "cosine"
	})
	indexed := slices.ContainsFunc(p.IndexingPolicy.VectorIndexes, func(v azcosmos.VectorIndex) bool { return v.Path == path && v.Type == "diskANN" })
	if !embedded || !indexed {
		return nil, vectordata.ErrSchemaMismatch
	}
	return &NativeCollection{container, s}, nil
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
	_, err = c.container.UpsertItem(ctx, azcosmos.NewPartitionKeyString(d.ID), body, nil)
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
	_, err := c.container.DeleteItem(ctx, azcosmos.NewPartitionKeyString(id), id, nil)
	if hasStatus(err, 404) {
		return nil
	}
	return writeError(err)
}

// Search requests a server-ranked page, optionally matching the text field.
// Gateway cross-partition query errors are returned without partial results.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	if top < 1 || (textEquals != nil && !utf8.ValidString(*textEquals)) {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	score := fmt.Sprintf("VectorDistance(c[%q], @v)", c.schema.Vector)
	params := []azcosmos.QueryParameter{{Name: "@v", Value: v}}
	where := ""
	if textEquals != nil {
		where = fmt.Sprintf(" WHERE c[%q] = @text", c.schema.Text)
		params = append(params, azcosmos.QueryParameter{Name: "@text", Value: *textEquals})
	}
	query := fmt.Sprintf("SELECT TOP %d c.id, %s AS score FROM c%s ORDER BY %s", top, score, where, score)
	pager := c.container.NewQueryItemsPager(query, azcosmos.NewPartitionKey(), &azcosmos.QueryOptions{QueryParameters: params})
	var hits []NativeHit
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			var hit struct {
				ID    string
				Score *float64
			}
			if err := json.Unmarshal(item, &hit); err != nil {
				return nil, err
			}
			if !validID(hit.ID) || hit.Score == nil || len(hits) >= top {
				return nil, vectordata.ErrInvalidRecord
			}
			hits = append(hits, NativeHit{hit.ID, *hit.Score})
		}
	}
	return hits, nil
}
