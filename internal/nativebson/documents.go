// Package nativebson implements shared string-key BSON document operations.
package nativebson

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Collection struct {
	Mongo        *mongo.Collection
	Text, Vector string
	Dimensions   int
	Unknown      error
}
type Document struct {
	ID, Text string
	Vector   []float32
}

func (c *Collection) Validate(v []float32) error {
	_, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.Dimensions, Metric: vectordata.DistanceCosine})
	return err
}
func validID(id string) bool { return id != "" && utf8.ValidString(id) }

// A per-document write rejection settles this single-document operation.
// A write-concern or transport error can arrive after the write was applied.
func writeError(unknown, err error) error {
	var rejected mongo.WriteException
	if errors.As(err, &rejected) && len(rejected.WriteErrors) > 0 && rejected.WriteConcernError == nil {
		return err
	}
	return errors.Join(unknown, err)
}

// Upsert sets the mapped fields and preserves unrelated BSON fields.
func (c *Collection) Upsert(ctx context.Context, d Document) error {
	if !validID(d.ID) || !utf8.ValidString(d.Text) {
		return vectordata.ErrInvalidRecord
	}
	if err := c.Validate(d.Vector); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := c.Mongo.UpdateOne(ctx, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{c.Text: d.Text, c.Vector: d.Vector}}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return writeError(c.Unknown, err)
	}
	if !result.Acknowledged || (result.MatchedCount != 1 && result.UpsertedID != d.ID) {
		return c.Unknown
	}
	return nil
}

// Get returns ErrNotFound for a missing _id.
func (c *Collection) Get(ctx context.Context, id string) (Document, error) {
	if !validID(id) {
		return Document{}, vectordata.ErrInvalidRecord
	}
	raw, err := c.Mongo.FindOne(ctx, bson.M{"_id": id}, options.FindOne().SetProjection(bson.M{c.Text: 1, c.Vector: 1})).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Document{}, vectordata.ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	text, ok := raw.Lookup(c.Text).StringValueOK()
	array, hasVector := raw.Lookup(c.Vector).ArrayOK()
	if !ok || !hasVector {
		return Document{}, vectordata.ErrInvalidRecord
	}
	values, err := array.Values()
	if err != nil || len(values) != c.Dimensions {
		return Document{}, vectordata.ErrInvalidRecord
	}
	v := make([]float32, len(values))
	for i, value := range values {
		n, ok := value.DoubleOK()
		if !ok {
			return Document{}, vectordata.ErrInvalidRecord
		}
		v[i] = float32(n)
	}
	if err := c.Validate(v); err != nil {
		return Document{}, err
	}
	return Document{id, text, v}, nil
}

// Delete removes one matching document, including successful no-op deletes.
func (c *Collection) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return vectordata.ErrInvalidRecord
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := c.Mongo.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return writeError(c.Unknown, err)
	}
	if !result.Acknowledged {
		return c.Unknown
	}
	return nil
}
