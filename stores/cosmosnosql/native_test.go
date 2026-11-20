package cosmosnosql

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestValidationBeforeIO(t *testing.T) {
	c := NativeCollection{schema: NativeSchema{Text: "body", Vector: "direction", Dimensions: 2}}
	if err := c.Upsert(t.Context(), NativeDocument{ID: "bad/id", Vector: []float32{1, 0}}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	if _, err := c.Search(t.Context(), []float32{1, 0}, 0, nil); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Delete(ctx, "valid"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrNativeOutcomeUnknown) {
		t.Fatal(err)
	}
}
func TestWriteOutcomes(t *testing.T) {
	err := writeError(context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrNativeOutcomeUnknown) {
		t.Fatal(err)
	}
	reject := &azcore.ResponseError{StatusCode: 400}
	if err := writeError(reject); err != reject {
		t.Fatal(err)
	}
}
func ExampleAttachNative() {
	search := func(ctx context.Context, container *azcosmos.ContainerClient) ([]NativeHit, error) {
		c, err := AttachNative(ctx, container, NativeSchema{Text: "body", Vector: "direction", Dimensions: 2})
		if err != nil {
			return nil, err
		}
		return c.Search(ctx, []float32{1, 0}, 5, nil)
	}
	_ = search
}

func TestProvisionedPolicies(t *testing.T) {
	s := NativeSchema{Text: "body", Vector: "direction", Dimensions: 2}
	creation := azcosmos.ContainerProperties{ID: "articles", PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/id"}},
		VectorEmbeddingPolicy: &azcosmos.VectorEmbeddingPolicy{VectorEmbeddings: []azcosmos.VectorEmbedding{{Path: "/direction", Dimensions: 2, DataType: "float32", DistanceFunction: "cosine"}}},
		IndexingPolicy:        &azcosmos.IndexingPolicy{VectorIndexes: []azcosmos.VectorIndex{{Path: "/direction", Type: "diskANN"}}}}
	observed := creation
	if err := s.checkPolicies(&observed, nil); err != nil {
		t.Fatal(err)
	}
	observed.VectorEmbeddingPolicy = nil
	if err := s.checkPolicies(&observed, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("missing policy accepted: %v", err)
	}
	if err := s.checkPolicies(&observed, &creation); err != nil || observed.VectorEmbeddingPolicy != nil {
		t.Fatalf("creation policy fallback: %v", err)
	}
	observed.IndexingPolicy = &azcosmos.IndexingPolicy{VectorIndexes: []azcosmos.VectorIndex{{Path: "/direction", Type: "flat"}}}
	if err := s.checkPolicies(&observed, &creation); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("observable mismatch accepted: %v", err)
	}
}
