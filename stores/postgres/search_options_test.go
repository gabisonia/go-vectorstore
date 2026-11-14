package postgres

import (
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSearchOptionsRejectInvalidLimits(t *testing.T) {
	for _, opts := range []vectordata.SearchOptions{
		{HNSW: &vectordata.HNSWSearchOptions{EfSearch: -1}},
		{HNSW: &vectordata.HNSWSearchOptions{EfSearch: 1001}},
		{HNSW: &vectordata.HNSWSearchOptions{MaxScanTuples: -1}},
		{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: -1}},
		{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 32769}},
		{IVFFlat: &vectordata.IVFFlatSearchOptions{MaxProbes: -1}},
		{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 10, MaxProbes: 5}},
	} {
		if _, err := testCollection().buildSearchPlan([]float32{1, 0, 0}, 5, opts); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
			t.Fatalf("expected ErrInvalidSearchOptions for %#v, got %v", opts, err)
		}
	}
}

func TestIterativeScanVersionRequirement(t *testing.T) {
	for _, version := range []string{"0.7.4", "0.6.0", "invalid"} {
		if supportsIterativeScans(version) {
			t.Fatalf("unsupported version %q", version)
		}
	}
	for _, version := range []string{"0.8.0", "0.9.1", "1.0.0"} {
		if !supportsIterativeScans(version) {
			t.Fatalf("supported version %q rejected", version)
		}
	}
}
