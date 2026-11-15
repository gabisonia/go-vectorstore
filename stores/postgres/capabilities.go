package postgres

import "github.com/gabisonia/go-vectorstore/vectordata"

var _ vectordata.CapabilityProvider = (*PostgresVectorStore)(nil)

// Capabilities reports connector model support without database access. Actual
// availability still depends on pgvector installation, version and permissions.
func (*PostgresVectorStore) Capabilities() vectordata.ProviderCapabilities {
	return vectordata.ProviderCapabilities{
		Backend:              "postgres",
		KeyTypes:             []vectordata.KeyType{vectordata.KeyString, vectordata.KeyInt64, vectordata.KeyUUID},
		GeneratedKeyTypes:    []vectordata.KeyType{vectordata.KeyInt64},
		DataTypes:            []vectordata.DataType{vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool, vectordata.TypeTime, vectordata.TypeStringArray, vectordata.TypeJSON, vectordata.TypeUUID, vectordata.TypeBytes},
		DistanceMetrics:      []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceInnerProduct},
		VectorPrecisions:     []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16},
		VectorIndexMethods:   []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat},
		IndexedDataTypes:     []vectordata.DataType{vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool, vectordata.TypeTime, vectordata.TypeStringArray, vectordata.TypeUUID, vectordata.TypeBytes},
		FullTextIndexing:     true,
		HybridSearch:         true,
		MigrationPlanning:    true,
		MigrationApplication: true,
	}
}
