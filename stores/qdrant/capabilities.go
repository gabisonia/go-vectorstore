package qdrant

import (
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.CapabilityProvider = (*Store)(nil)
var _ vectordata.StoreMetadataProvider = (*Store)(nil)
var _ vectordata.CollectionMetadataProvider = (*Collection)(nil)

// Capabilities describes canonical execution, not native Qdrant indexes.
func (s *Store) Capabilities() vectordata.ProviderCapabilities {
	canonical := memory.New().Capabilities()
	return vectordata.ProviderCapabilities{
		Backend: "qdrant", Metadata: true, ReadIterators: true,
		InputEmbedding: true, OptionalVectors: true,
		KeyTypes: canonical.KeyTypes, GeneratedKeyTypes: canonical.GeneratedKeyTypes,
		DataTypes: canonical.DataTypes, VectorPrecisions: canonical.VectorPrecisions,
		VectorKinds:     []vectordata.VectorKind{vectordata.VectorDense},
		DistanceMetrics: []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct, vectordata.DistanceL1},
	}
}
