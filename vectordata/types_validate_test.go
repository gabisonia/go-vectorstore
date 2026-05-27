package vectordata

import (
	"errors"
	"testing"
)

func TestDefaultProjectionIncludesMetadataAndContentOnly(t *testing.T) {
	projection := DefaultProjection()

	if projection.IncludeVector {
		t.Fatal("default projection should not include vector")
	}
	if !projection.IncludeMetadata {
		t.Fatal("default projection should include metadata")
	}
	if !projection.IncludeContent {
		t.Fatal("default projection should include content")
	}
}

func TestScoreFromDistance(t *testing.T) {
	tests := []struct {
		name     string
		metric   DistanceMetric
		distance float64
		want     float64
	}{
		{name: "cosine", metric: DistanceCosine, distance: 0.25, want: 0.75},
		{name: "l2", metric: DistanceL2, distance: 3, want: 0.25},
		{name: "inner product", metric: DistanceInnerProduct, distance: -0.8, want: 0.8},
		{name: "unknown", metric: "custom", distance: 2, want: -2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScoreFromDistance(tt.metric, tt.distance); got != tt.want {
				t.Fatalf("ScoreFromDistance() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDistanceMetricValidate(t *testing.T) {
	for _, metric := range []DistanceMetric{DistanceCosine, DistanceL2, DistanceInnerProduct} {
		t.Run(string(metric), func(t *testing.T) {
			if err := metric.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}

	err := DistanceMetric("unknown").Validate()
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("expected ErrSchemaMismatch, got %v", err)
	}
}

func TestNormalizeMetricAndModeDefaults(t *testing.T) {
	if got := normalizeMetric(""); got != DistanceCosine {
		t.Fatalf("normalizeMetric empty = %q", got)
	}
	if got := normalizeMetric(DistanceL2); got != DistanceL2 {
		t.Fatalf("normalizeMetric l2 = %q", got)
	}
	if got := normalizeMode(""); got != EnsureStrict {
		t.Fatalf("normalizeMode empty = %q", got)
	}
	if got := normalizeMode(EnsureAutoMigrate); got != EnsureAutoMigrate {
		t.Fatalf("normalizeMode auto_migrate = %q", got)
	}
}
