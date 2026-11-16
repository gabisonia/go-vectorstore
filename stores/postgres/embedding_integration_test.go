//go:build integration

package postgres

import (
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
)

func TestIntegrationEmbeddingConfiguration(t *testing.T) {
	conformance.RunEmbeddingConfiguration(t, func(t *testing.T, defaults vectordata.ModelCollectionOptions) vectordata.ModelStore {
		base := newTestStore(t, integrationPool(t))
		opts := base.opts
		opts.Embedder = defaults.Embedder
		opts.InputEmbedder = defaults.InputEmbedder
		opts.EmbeddingBatchSize = defaults.EmbeddingBatchSize
		store, err := NewVectorStore(base.pool, opts)
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}
