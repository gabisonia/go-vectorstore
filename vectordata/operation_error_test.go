package vectordata_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestOperationErrorMetadataKeepsTextAndUnwrapCompatibility(t *testing.T) {
	// Existing callers and third-party providers may omit store identity.
	legacy := &vectordata.OperationError{Backend: "postgres", Collection: "docs", Operation: "get", Err: context.Canceled}
	if legacy.Error() != `postgres get "docs": context canceled` || !errors.Is(legacy, context.Canceled) {
		t.Fatalf("legacy error changed: %v", legacy)
	}
	withMetadata := *legacy
	withMetadata.StoreMetadata = vectordata.StoreMetadata{SystemName: "postgresql", DatabaseName: "research", Namespace: "tenant"}
	if withMetadata.Error() != legacy.Error() || !errors.Is(&withMetadata, context.Canceled) {
		t.Fatalf("metadata changed error text or cause: %v", withMetadata)
	}
	var op *vectordata.OperationError
	if !errors.As(errors.Join(errors.New("outer"), &withMetadata), &op) || op.DatabaseName != "research" || op.Namespace != "tenant" {
		t.Fatal("metadata inaccessible through joined error")
	}
}
