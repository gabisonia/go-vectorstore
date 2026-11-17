package redis

import (
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.CapabilityProvider = (*Store)(nil)
var _ vectordata.StoreMetadataProvider = (*Store)(nil)
var _ vectordata.CollectionMetadataProvider = (*Collection)(nil)

// Capabilities is local configuration discovery. NativeIndexes advertises the
// optional physical index subset without contacting Redis or checking modules.
func (s *Store) Capabilities() vectordata.ProviderCapabilities {
	canonical := memory.New().Capabilities()
	caps := vectordata.ProviderCapabilities{
		Backend: "redis", Metadata: true, ReadIterators: true,
		InputEmbedding: true, OptionalVectors: true,
		KeyTypes: canonical.KeyTypes, GeneratedKeyTypes: canonical.GeneratedKeyTypes,
		DataTypes: canonical.DataTypes, VectorPrecisions: canonical.VectorPrecisions,
		VectorKinds:     []vectordata.VectorKind{vectordata.VectorDense},
		DistanceMetrics: []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct, vectordata.DistanceL1},
	}
	if s.nativeIndexes {
		caps.VectorIndexMethods = []vectordata.IndexMethod{vectordata.IndexMethodHNSW}
		caps.IndexedDataTypes = []vectordata.DataType{vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool}
		for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct} {
			for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
				caps.VectorIndexCapabilities = append(caps.VectorIndexCapabilities, vectordata.VectorIndexCapability{Kind: vectordata.VectorDense, Method: vectordata.IndexMethodHNSW, Metric: metric, Precision: precision, MaxDimensions: nativeMaxDimensions})
			}
		}
	}
	return caps
}
