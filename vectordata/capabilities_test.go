package vectordata_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestBuiltInCapabilitiesAreLocalSnapshots(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider vectordata.CapabilityProvider
		indexed  bool
	}{
		{"memory", new(memory.Store), false},
		// A nil pool ensures discovery cannot accidentally access the database.
		{"postgres", new(postgres.PostgresVectorStore), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.provider.Capabilities()
			if !slices.Contains(before.DistanceMetrics, vectordata.DistanceL1) {
				t.Fatal("L1 support missing")
			}
			if before.Backend != tt.name || before.FullTextIndexing != tt.indexed || before.HybridSearch != tt.indexed || before.MigrationPlanning != tt.indexed || before.MigrationApplication != tt.indexed {
				t.Fatalf("unexpected features: %+v", before)
			}
			if !slices.Equal(before.GeneratedKeyTypes, []vectordata.KeyType{vectordata.KeyInt64, vectordata.KeyUUID}) {
				t.Fatalf("generated keys: %v", before.GeneratedKeyTypes)
			}
			if !slices.Equal(before.VectorPrecisions, []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16}) {
				t.Fatalf("vector precisions: %v", before.VectorPrecisions)
			}
			if tt.indexed {
				if len(before.VectorIndexCapabilities) != 20 {
					t.Fatalf("index combinations: %+v", before.VectorIndexCapabilities)
				}
				for _, index := range before.VectorIndexCapabilities {
					if index.Metric == vectordata.DistanceL1 && index.Method != vectordata.IndexMethodHNSW {
						t.Fatalf("unsupported L1 index advertised: %+v", index)
					}
				}
				if !slices.Equal(before.VectorIndexMethods, []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat}) {
					t.Fatalf("indexes: %v", before.VectorIndexMethods)
				}
				if slices.Contains(before.IndexedDataTypes, vectordata.TypeJSON) {
					t.Fatal("generic JSON indexing is not implemented")
				}
			} else if len(before.VectorIndexMethods) != 0 || len(before.VectorIndexCapabilities) != 0 || len(before.IndexedDataTypes) != 0 {
				t.Fatal("memory must not advertise physical indexes")
			}
			mutated := tt.provider.Capabilities()
			for i := range mutated.KeyTypes {
				mutated.KeyTypes[i] = "changed"
			}
			for i := range mutated.GeneratedKeyTypes {
				mutated.GeneratedKeyTypes[i] = "changed"
			}
			for i := range mutated.DataTypes {
				mutated.DataTypes[i] = "changed"
			}
			for i := range mutated.DistanceMetrics {
				mutated.DistanceMetrics[i] = "changed"
			}
			for i := range mutated.VectorPrecisions {
				mutated.VectorPrecisions[i] = "changed"
			}
			for i := range mutated.VectorKinds {
				mutated.VectorKinds[i] = "changed"
			}
			for i := range mutated.VectorIndexMethods {
				mutated.VectorIndexMethods[i] = "changed"
			}
			for i := range mutated.VectorIndexCapabilities {
				mutated.VectorIndexCapabilities[i].Metric = "changed"
				mutated.VectorIndexCapabilities[i].MaxDimensions = 1
				mutated.VectorIndexCapabilities[i].MaxNonZero = 1
			}
			for i := range mutated.IndexedDataTypes {
				mutated.IndexedDataTypes[i] = "changed"
			}
			if after := tt.provider.Capabilities(); !reflect.DeepEqual(before, after) {
				t.Fatalf("caller mutation changed future capabilities: before=%+v, after=%+v", before, after)
			}
		})
	}
}

func TestCapabilitiesRemainOptional(t *testing.T) {
	// Embedding only ModelStore emulates an existing third-party implementation.
	// It remains usable even though capability discovery is unavailable.
	var store vectordata.ModelStore = struct{ vectordata.ModelStore }{memory.New()}
	if _, ok := store.(vectordata.CapabilityProvider); ok {
		t.Fatal("ModelStore unexpectedly requires capability discovery")
	}
}
