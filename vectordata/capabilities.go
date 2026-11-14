package vectordata

// CapabilityProvider optionally describes a connector's model API support.
// ModelStore does not require it, so third-party providers can adopt it separately.
// Discovery is local and does not check server versions, permissions or health.
type CapabilityProvider interface {
	Capabilities() ProviderCapabilities
}

// ProviderCapabilities describes implemented model features, not whether a
// particular collection or deployment enables them. Empty slices mean no support
// for that feature. Built-in providers return independent, caller-owned slices.
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
	IndexedDataTypes   []DataType
	// FullTextIndexing applies to string properties.
	FullTextIndexing bool
	HybridSearch     bool
	// MigrationPlanning describes plan generation, not migration execution.
	MigrationPlanning bool
	// MigrationApplication supports optional-property and index additions only.
	MigrationApplication bool
}
