// Package documentdb accesses Azure DocumentDB collections with a native cosine HNSW index.
package documentdb

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/internal/nativebson"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
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

// NativeHit reports the native cosine similarity.
type NativeHit struct {
	ID    string  `bson:"_id"`
	Score float64 `bson:"score"`
}

// ErrNativeOutcomeUnknown marks a write for which the adapter could not confirm success.
var ErrNativeOutcomeUnknown = errors.New("DocumentDB native write outcome unknown")

var fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// AttachNative checks the physical HNSW index on a caller-managed collection.
func AttachNative(ctx context.Context, c *mongo.Collection, s NativeSchema) (*NativeCollection, error) {
	if c == nil || s.Index == "" || !utf8.ValidString(s.Index) || s.Dimensions < 1 || !fieldName.MatchString(s.Text) || !fieldName.MatchString(s.Vector) || s.Text == s.Vector {
		return nil, vectordata.ErrSchemaMismatch
	}
	cur, err := c.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var vector, text bool
	for cur.Next(ctx) {
		var idx struct {
			Name    string
			Key     bson.M
			Options struct {
				Kind, Similarity string
				Dimensions       int
			} `bson:"cosmosSearchOptions"`
		}
		if err := cur.Decode(&idx); err != nil {
			return nil, err
		}
		if idx.Name == s.Index && len(idx.Key) == 1 && idx.Key[s.Vector] == "cosmosSearch" && idx.Options.Kind == "vector-hnsw" && strings.EqualFold(idx.Options.Similarity, "COS") && idx.Options.Dimensions == s.Dimensions {
			vector = true
		}
		if len(idx.Key) == 1 {
			switch idx.Key[s.Text] {
			case int32(1), int64(1), float64(1):
				text = true
			}
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if vector && text {
		return &NativeCollection{c, s}, nil
	}
	return nil, vectordata.ErrSchemaMismatch
}

func (c *NativeCollection) documents() nativebson.Collection {
	return nativebson.Collection{Mongo: c.collection, Text: c.schema.Text, Vector: c.schema.Vector, Dimensions: c.schema.Dimensions, Unknown: ErrNativeOutcomeUnknown}
}

// Upsert sets the mapped fields and preserves unrelated BSON fields.
func (c *NativeCollection) Upsert(ctx context.Context, d NativeDocument) error {
	docs := c.documents()
	return docs.Upsert(ctx, nativebson.Document(d))
}

// Get returns ErrNotFound for a missing _id.
func (c *NativeCollection) Get(ctx context.Context, id string) (NativeDocument, error) {
	docs := c.documents()
	d, err := docs.Get(ctx, id)
	return NativeDocument(d), err
}

// Delete removes one matching document, including successful no-op deletes.
func (c *NativeCollection) Delete(ctx context.Context, id string) error {
	docs := c.documents()
	return docs.Delete(ctx, id)
}

// Search requests native HNSW candidates in server score order.
// A non-nil textEquals applies a native prefilter to the configured text field.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int, textEquals *string) ([]NativeHit, error) {
	if top <= 0 || (textEquals != nil && !utf8.ValidString(*textEquals)) {
		return nil, vectordata.ErrInvalidRecord
	}
	docs := c.documents()
	if err := docs.Validate(v); err != nil {
		return nil, err
	}
	query := bson.M{"path": c.schema.Vector, "vector": v, "k": top}
	if textEquals != nil {
		query["filter"] = bson.M{c.schema.Text: bson.M{"$eq": *textEquals}}
	}
	cursor, err := c.collection.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$search", Value: bson.M{"cosmosSearch": query}}},
		bson.D{{Key: "$project", Value: bson.M{"_id": 1, "score": bson.M{"$meta": "searchScore"}}}},
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
