package vectordata

// CapabilityProvider optionally describes a connector's model API support.
// ModelStore does not require it, so third-party providers can adopt it separately.
// Discovery is local and does not check server versions, permissions or health.
type CapabilityProvider interface {
	Capabilities() ProviderCapabilities
}

// VectorIndexCapability describes one physical index combination. Precision is
// explicit (float32 or float16), and MaxDimensions is the positive index limit.
type VectorIndexCapability struct {
	Method        IndexMethod
	Metric        DistanceMetric
	Precision     VectorPrecision
	MaxDimensions int
}

// ProviderCapabilities describes implemented model features, not whether a
// particular collection or deployment enables them. Unless stated otherwise,
// empty slices mean no support. Built-in providers return caller-owned slices.
type ProviderCapabilities struct {
	Backend  string
	KeyTypes []KeyType
	// GeneratedKeyTypes is the subset of KeyTypes that supports automatic keys.
	GeneratedKeyTypes []KeyType
	DataTypes         []DataType
	DistanceMetrics   []DistanceMetric
	VectorPrecisions  []VectorPrecision
	// Index fields report physical indexes, rather than accepted schema hints.
	VectorIndexMethods []IndexMethod
	// VectorIndexCapabilities lists supported physical combinations. Nil means
	// details are unreported; coarse method/metric lists do not imply every pair.
	VectorIndexCapabilities []VectorIndexCapability
	IndexedDataTypes        []DataType
	// FullTextIndexing applies to string properties.
	FullTextIndexing bool
	HybridSearch     bool
	// MigrationPlanning describes plan generation, not migration execution.
	MigrationPlanning bool
	// MigrationApplication supports optional-property and index additions only.
	MigrationApplication bool
}
