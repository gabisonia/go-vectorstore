package vectordata

import "context"

// DistanceMetric selects the similarity distance function used by a collection.
type DistanceMetric string

const (
	DistanceCosine       DistanceMetric = "cosine"
	DistanceL2           DistanceMetric = "l2"
	DistanceInnerProduct DistanceMetric = "inner_product"
)

// EnsureMode controls how schema checks are enforced when ensuring collections.
type EnsureMode string

const (
	// EnsureStrict fails when the existing schema does not match CollectionSpec.
	EnsureStrict EnsureMode = "strict"
	// EnsureAutoMigrate creates missing optional columns where possible.
	EnsureAutoMigrate EnsureMode = "auto_migrate"
)

// CollectionSpec defines physical collection requirements.
type CollectionSpec struct {
	Name      string
	Dimension int
	Metric    DistanceMetric
	Mode      EnsureMode
}

// Record is the base storage model for a vector collection.
type Record struct {
	ID       string
	Vector   []float32
	Metadata map[string]any
	Content  *string
}

// SearchResult contains a matched record plus ranking values.
type SearchResult struct {
	Record   Record
	Distance float64
	Score    float64
}

// Projection configures which optional fields are returned by search operations.
type Projection struct {
	IncludeVector   bool
	IncludeMetadata bool
	IncludeContent  bool
}

// DefaultProjection returns the default projection used by SearchByVector.
func DefaultProjection() Projection {
	return Projection{IncludeMetadata: true, IncludeContent: true}
}

// SearchOptions configures similarity search behavior.
type SearchOptions struct {
	Filter     Filter
	Projection *Projection
	Threshold  *float64
}

// IndexMethod selects a vector index implementation.
type IndexMethod string

const (
	IndexMethodHNSW    IndexMethod = "hnsw"
	IndexMethodIVFFlat IndexMethod = "ivfflat"
)

// HNSWOptions configures HNSW index tuning.
type HNSWOptions struct {
	M              int
	EfConstruction int
}

// IVFFlatOptions configures IVFFlat index tuning.
type IVFFlatOptions struct {
	Lists int
}

// VectorIndexOptions configures creation of a vector index.
type VectorIndexOptions struct {
	Name    string
	Method  IndexMethod
	Metric  DistanceMetric
	HNSW    HNSWOptions
	IVFFlat IVFFlatOptions
}

// MetadataIndexOptions configures creation of a metadata JSONB index.
type MetadataIndexOptions struct {
	Name       string
	UsePathOps bool
}

// IndexOptions configures collection index creation.
type IndexOptions struct {
	Vector   *VectorIndexOptions
	Metadata *MetadataIndexOptions
}

// VectorStore creates and resolves vector collections.
type VectorStore interface {
	EnsureCollection(ctx context.Context, spec CollectionSpec) (Collection, error)
	Collection(name string, dimension int, metric DistanceMetric) Collection
}

// Collection represents an operational vector collection.
type Collection interface {
	Name() string
	Dimension() int
	Metric() DistanceMetric

	Insert(ctx context.Context, records []Record) error
	Upsert(ctx context.Context, records []Record) error
	Get(ctx context.Context, id string) (Record, error)
	Delete(ctx context.Context, ids []string) (int64, error)
	Count(ctx context.Context, filter Filter) (int64, error)

	SearchByVector(ctx context.Context, vector []float32, topK int, opts SearchOptions) ([]SearchResult, error)
	EnsureIndexes(ctx context.Context, opts IndexOptions) error
}

// ScoreFromDistance converts backend distance into a monotonic score (higher is better).
func ScoreFromDistance(metric DistanceMetric, distance float64) float64 {
	switch metric {
	case DistanceCosine:
		return 1 - distance
	case DistanceL2:
		return 1 / (1 + distance)
	case DistanceInnerProduct:
		return -distance
	default:
		return -distance
	}
}
