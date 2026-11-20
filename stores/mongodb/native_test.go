package mongodb

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestWriteOutcomes(t *testing.T) {
	rejected := mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 121, Message: "document validation failed"}}}
	if errors.Is(writeError(rejected), ErrNativeOutcomeUnknown) {
		t.Fatal("single-row validation rejection marked uncertain")
	}
	uncertain := writeError(context.DeadlineExceeded)
	if !errors.Is(uncertain, ErrNativeOutcomeUnknown) || !errors.Is(uncertain, context.DeadlineExceeded) {
		t.Fatal(uncertain)
	}
}

func TestValidateBeforeIO(t *testing.T) {
	c := &NativeCollection{schema: NativeSchema{Dimensions: 2}}
	ctx := t.Context()
	if err := c.Upsert(ctx, NativeDocument{ID: "", Vector: []float32{1, 0}}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, []float32{1, 0}, 0, nil); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, ""); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
}

func ExampleAttachNative() {
	// Supply an already-connected client's collection and the physical field names.
	search := func(ctx context.Context, collection *mongo.Collection) ([]NativeHit, error) {
		articles, err := AttachNative(ctx, collection, NativeSchema{
			Index: "vector_index", Text: "body", Vector: "direction", Dimensions: 2,
		})
		if err != nil {
			return nil, err
		}
		body := "guide"
		return articles.Search(ctx, []float32{1, 0}, 5, &body)
	}
	_ = search
}
