package postgres

import "github.com/gabisonia/go-vectorstore/vectordata"

var _ vectordata.CapabilityProvider = (*PostgresVectorStore)(nil)

// Capabilities reports connector model support without database access. Actual
// availability still depends on pgvector installation, version and permissions.
func (*PostgresVectorStore) Capabilities() vectordata.ProviderCapabilities {
	caps := vectordata.ProviderCapabilities{
		Backend:              "postgres",
		KeyTypes:             []vectordata.KeyType{vectordata.KeyString, vectordata.KeyInt64, vectordata.KeyUUID},
		GeneratedKeyTypes:    []vectordata.KeyType{vectordata.KeyInt64},
		DataTypes:            []vectordata.DataType{vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool, vectordata.TypeTime, vectordata.TypeStringArray, vectordata.TypeJSON, vectordata.TypeUUID, vectordata.TypeBytes, vectordata.TypeInt64Array, vectordata.TypeFloat64Array, vectordata.TypeBoolArray, vectordata.TypeUUIDArray, vectordata.TypeTimeArray, vectordata.TypeDateOnly, vectordata.TypeTimeOnly, vectordata.TypeDateOnlyArray, vectordata.TypeTimeOnlyArray, vectordata.TypeDecimal, vectordata.TypeDecimalArray},
		DistanceMetrics:      []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceInnerProduct, vectordata.DistanceL1, vectordata.DistanceHamming},
		VectorKinds:          []vectordata.VectorKind{vectordata.VectorDense, vectordata.VectorBinary},
		VectorPrecisions:     []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16},
		VectorIndexMethods:   []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat},
		IndexedDataTypes:     []vectordata.DataType{vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool, vectordata.TypeTime, vectordata.TypeStringArray, vectordata.TypeUUID, vectordata.TypeBytes, vectordata.TypeInt64Array, vectordata.TypeFloat64Array, vectordata.TypeBoolArray, vectordata.TypeUUIDArray, vectordata.TypeTimeArray, vectordata.TypeDateOnly, vectordata.TypeTimeOnly, vectordata.TypeDateOnlyArray, vectordata.TypeTimeOnlyArray, vectordata.TypeDecimal, vectordata.TypeDecimalArray},
		FullTextIndexing:     true,
		HybridSearch:         true,
		MigrationPlanning:    true,
		MigrationApplication: true,
	}
	for _, precision := range caps.VectorPrecisions {
		for _, metric := range caps.DistanceMetrics {
			for _, method := range caps.VectorIndexMethods {
				if limit, err := vectorIndexLimit(precision, metric, method); err == nil {
					caps.VectorIndexCapabilities = append(caps.VectorIndexCapabilities, vectordata.VectorIndexCapability{Method: method, Metric: metric, Precision: precision, MaxDimensions: limit})
				}
			}
		}
	}
	for _, method := range caps.VectorIndexMethods {
		caps.VectorIndexCapabilities = append(caps.VectorIndexCapabilities, vectordata.VectorIndexCapability{Kind: vectordata.VectorBinary, Method: method, Metric: vectordata.DistanceHamming, MaxDimensions: 64000})
	}
	return caps
}
