package memory

import "github.com/gabisonia/go-vectorstore/vectordata"

var _ vectordata.CapabilityProvider = (*Store)(nil)

// Capabilities reports model support without accessing stored collections.
// Index hints are accepted, but memory does not build physical indexes.
func (*Store) Capabilities() vectordata.ProviderCapabilities {
	return vectordata.ProviderCapabilities{
		Backend:           "memory",
		KeyTypes:          []vectordata.KeyType{vectordata.KeyString, vectordata.KeyInt64, vectordata.KeyUUID},
		GeneratedKeyTypes: []vectordata.KeyType{vectordata.KeyInt64},
		DataTypes:         []vectordata.DataType{vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool, vectordata.TypeTime, vectordata.TypeStringArray, vectordata.TypeJSON, vectordata.TypeUUID, vectordata.TypeBytes, vectordata.TypeInt64Array, vectordata.TypeFloat64Array, vectordata.TypeBoolArray, vectordata.TypeUUIDArray, vectordata.TypeTimeArray},
		DistanceMetrics:   []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceInnerProduct, vectordata.DistanceL1},
		VectorPrecisions:  []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16},
	}
}
