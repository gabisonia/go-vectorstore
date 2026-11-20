// Package mongodb accesses native MongoDB Atlas Vector Search collections.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// NativeSchema maps a named vector search index, one indexed string field
// and a cosine vector stored as an array of BSON doubles.
type NativeSchema struct {
	Index, Text, Vector string
	Dimensions          int
}

// NativeCollection uses the caller's MongoDB client and its connection settings.
type NativeCollection struct {
	collection *mongo.Collection
	schema     NativeSchema
}

// NativeDocument has an explicit string _id and a dense float vector.
type NativeDocument struct {
	ID, Text string
	Vector   []float32
}

// NativeHit reports MongoDB's native normalized cosine score.
type NativeHit struct {
	ID    string  `bson:"_id"`
	Score float64 `bson:"score"`
}

// ErrNativeOutcomeUnknown marks a write for which the adapter could not confirm success.
var ErrNativeOutcomeUnknown = errors.New("MongoDB native write outcome unknown")

var fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

type indexDescription struct {
	Name, Type, Status string
	Queryable          bool
	Definition         struct {
		Fields []struct {
			Type, Path, Similarity string
			Dimensions             int `bson:"numDimensions"`
		}
	} `bson:"latestDefinition"`
}

func (s NativeSchema) checkIndex(idx indexDescription) error {
	if idx.Name != s.Index || idx.Type != "vectorSearch" || !idx.Queryable || idx.Status != "READY" {
		return fmt.Errorf("%w: index %q is not ready for vector search", vectordata.ErrSchemaMismatch, s.Index)
	}
	var vector, filter bool
	for _, f := range idx.Definition.Fields {
		if f.Type == "vector" && f.Path == s.Vector && f.Similarity == "cosine" && f.Dimensions == s.Dimensions {
			vector = true
		}
		if f.Type == "filter" && f.Path == s.Text {
			filter = true
		}
	}
	if !vector || !filter {
		return fmt.Errorf("%w: expected cosine vector %q and filter %q", vectordata.ErrSchemaMismatch, s.Vector, s.Text)
	}
	return nil
}

// AttachNative verifies that the named index is ready and supports both the
// configured vector and text filter. Collection and index creation are external.
func AttachNative(ctx context.Context, c *mongo.Collection, s NativeSchema) (*NativeCollection, error) {
	if c == nil || s.Index == "" || !utf8.ValidString(s.Index) || s.Dimensions < 1 || !fieldName.MatchString(s.Text) || !fieldName.MatchString(s.Vector) || s.Text == s.Vector {
		return nil, vectordata.ErrSchemaMismatch
	}
	cursor, err := c.Aggregate(ctx, mongo.Pipeline{bson.D{{Key: "$listSearchIndexes", Value: bson.M{"name": s.Index}}}})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	if !cursor.Next(ctx) {
		if err := cursor.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: missing index %q", vectordata.ErrSchemaMismatch, s.Index)
	}
	var desc indexDescription
	if err := cursor.Decode(&desc); err != nil {
		return nil, err
	}
	if err := s.checkIndex(desc); err != nil {
		return nil, err
	}
	return &NativeCollection{c, s}, nil
}

func (c *NativeCollection) validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.schema.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}
func validID(id string) bool { return id != "" && utf8.ValidString(id) }

// Upsert sets the mapped fields and preserves unrelated BSON fields.
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
	result, err := c.collection.UpdateOne(ctx, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{c.schema.Text: d.Text, c.schema.Vector: d.Vector}}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return errors.Join(ErrNativeOutcomeUnknown, err)
	}
	if !result.Acknowledged || (result.MatchedCount != 1 && result.UpsertedID != d.ID) {
		return ErrNativeOutcomeUnknown
	}
	return nil
}

// Get returns ErrNotFound for a missing _id.
func (c *NativeCollection) Get(ctx context.Context, id string) (NativeDocument, error) {
	if !validID(id) {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	raw, err := c.collection.FindOne(ctx, bson.M{"_id": id}, options.FindOne().SetProjection(bson.M{c.schema.Text: 1, c.schema.Vector: 1})).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return NativeDocument{}, vectordata.ErrNotFound
	}
	if err != nil {
		return NativeDocument{}, err
	}
	text, ok := raw.Lookup(c.schema.Text).StringValueOK()
	array, hasVector := raw.Lookup(c.schema.Vector).ArrayOK()
	if !ok || !hasVector {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	values, err := array.Values()
	if err != nil || len(values) != c.schema.Dimensions {
		return NativeDocument{}, vectordata.ErrInvalidRecord
	}
	v := make([]float32, len(values))
	for i, value := range values {
		n, ok := value.DoubleOK()
		if !ok {
			return NativeDocument{}, vectordata.ErrInvalidRecord
		}
		v[i] = float32(n)
	}
	if err := c.validate(v); err != nil {
		return NativeDocument{}, err
	}
	return NativeDocument{id, text, v}, nil
}

// Delete removes one matching document, including successful no-op deletes.
func (c *NativeCollection) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return vectordata.ErrInvalidRecord
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := c.collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return errors.Join(ErrNativeOutcomeUnknown, err)
	}
	if !result.Acknowledged {
		return ErrNativeOutcomeUnknown
	}
	return nil
}

// Search uses exact vector search on the asynchronously maintained index.
// A non-nil textEquals applies a native prefilter to the configured text field.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	if top <= 0 || (textEquals != nil && !utf8.ValidString(*textEquals)) {
		return nil, vectordata.ErrInvalidRecord
	}
	if err := c.validate(v); err != nil {
		return nil, err
	}
	query := bson.M{"index": c.schema.Index, "path": c.schema.Vector, "queryVector": v, "limit": top, "exact": true}
	if textEquals != nil {
		query["filter"] = bson.M{c.schema.Text: bson.M{"$eq": *textEquals}}
	}
	cursor, err := c.collection.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$vectorSearch", Value: query}},
		bson.D{{Key: "$project", Value: bson.M{"_id": 1, "score": bson.M{"$meta": "vectorSearchScore"}}}},
	})
	if err != nil {
		return nil, err
	}
	var hits []NativeHit
	if err := cursor.All(ctx, &hits); err != nil {
		return nil, err
	}
	return hits, nil
}
