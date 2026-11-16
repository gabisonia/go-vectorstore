package vectordata

// CapabilityProvider optionally describes a connector's model API support.
// ModelStore does not require it, so third-party providers can adopt it separately.
// Discovery is local and does not check server versions, permissions or health.
type CapabilityProvider interface {
	Capabilities() ProviderCapabilities
}

// VectorIndexCapability describes one physical index combination. Dense entries
// use float32/float16 precision, sparse entries use float32, and binary entries
// omit precision. MaxDimensions is positive; sparse entries also set MaxNonZero.
type VectorIndexCapability struct {
	// Kind defaults to dense for older providers; binary entries omit precision.
	Kind          VectorKind
	Method        IndexMethod
	Metric        DistanceMetric
	Precision     VectorPrecision
	MaxDimensions int
	// MaxNonZero limits stored nonzero components; zero means unreported.
	MaxNonZero int
}

// ProviderCapabilities describes implemented model features, not whether a
// particular collection or deployment enables them. Unless stated otherwise,
// empty slices mean no support. Built-in providers return caller-owned slices.
type ProviderCapabilities struct {
	// Metadata supports optional store and collection identity discovery without I/O.
	Metadata bool
	// ReadIterators adds native collection listing, batch reads and text search
	// iterators. InputEmbedding/HybridSearch also add their input/hybrid iterators.
	// This reports API support, not whether a provider buffers snapshots/results.
	ReadIterators bool
	// InputEmbedding supports transient/custom inputs and InputSearcher for dense vectors.
	InputEmbedding bool
	// OptionalVectors permits SQL NULL / missing values for every supported vector kind.
	OptionalVectors bool
	Backend         string
	KeyTypes        []KeyType
	// GeneratedKeyTypes is the subset of KeyTypes that supports automatic keys.
	GeneratedKeyTypes []KeyType
	DataTypes         []DataType
	DistanceMetrics   []DistanceMetric
	VectorPrecisions  []VectorPrecision
	// VectorKinds reports dense/binary/sparse support. Nil means kinds are unreported.
	VectorKinds []VectorKind
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
