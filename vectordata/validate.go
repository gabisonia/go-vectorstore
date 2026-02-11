package vectordata

import "fmt"

func (m DistanceMetric) Validate() error {
	switch m {
	case DistanceCosine, DistanceL2, DistanceInnerProduct:
		return nil
	default:
		return fmt.Errorf("%w: unsupported distance metric %q", ErrSchemaMismatch, m)
	}
}

func normalizeMetric(metric DistanceMetric) DistanceMetric {
	if metric == "" {
		return DistanceCosine
	}
	return metric
}

func normalizeMode(mode EnsureMode) EnsureMode {
	if mode == "" {
		return EnsureStrict
	}
	return mode
}
