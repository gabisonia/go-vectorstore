//go:build integration

package redis

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

func TestIntegrationNativeFlatExactReference(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct} {
		for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
			t.Run(string(metric)+"/"+string(precision), func(t *testing.T) {
				s := integrationStore(t, StoreOptions{NativeIndexes: true})
				d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeString, Optional: true, IsIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 16, Metric: metric, Precision: precision, Optional: true, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodFlat}}, {Name: "Approx", Dimensions: 2, Optional: true, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}}}
				base, err := s.EnsureModelCollection(ctx, "flat", d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c := base.(*Collection)
				rng := rand.New(rand.NewSource(45))
				rows := make([]vectordata.ModelRecord, 512)
				for i := range rows {
					v := make([]float32, 16)
					for j := range v {
						v[j] = rng.Float32()*2 - 1
					}
					var group any = "other"
					if i%3 == 0 {
						group = "selected"
					}
					if i%9 == 0 {
						group = nil
					}
					rows[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Group": group}, Vectors: map[string][]float32{"Vector": v, "Approx": {1, 0}}}
					if i%17 == 0 {
						delete(rows[i].Vectors, "Vector")
					}
				}
				if _, err = c.Insert(ctx, rows); err != nil {
					t.Fatal(err)
				}
				reference, err := c.engine(ctx)
				if err != nil {
					t.Fatal(err)
				}
				native, full := 0, 0
				s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
					if scriptIs(cmd, nativeExecute) {
						native++
						// The query and parameters are arguments to the atomic script.
						for _, arg := range cmd.Args()[2:] {
							if text, ok := arg.(string); ok && strings.Contains(text, "=>[KNN") && strings.Contains(text, "EF_RUNTIME") {
								t.Fatal("FLAT query received HNSW tuning")
							}
						}
					}
					if scriptIs(cmd, readHash) {
						full++
					}
					return next(ctx, cmd)
				}})
				for _, qi := range []int{1, 8, 22} {
					query := rows[qi].Vectors["Vector"]
					baseline, err := reference.Search(ctx, query, 7, vectordata.ModelSearchOptions{VectorProperty: "Vector"})
					if err != nil || len(baseline) != 7 {
						t.Fatalf("reference: %+v %v", baseline, err)
					}
					threshold, score := baseline[3].Distance, baseline[3].Score
					for i, options := range []vectordata.ModelSearchOptions{
						{IncludeVectors: true},
						{Filter: vectordata.Eq(vectordata.Column("Group"), "selected"), Skip: 3},
						{Filter: vectordata.Not(vectordata.Eq(vectordata.Column("Group"), "selected"))},
						{Filter: vectordata.Eq(vectordata.Column("Group"), "missing")},
						{Filter: vectordata.Gt(vectordata.Column("ID"), int64(400)), Skip: 200},
						{DistanceThreshold: &threshold, Skip: 1},
						{ScoreThreshold: &score, IncludeVectors: true},
					} {
						options.VectorProperty = "Vector"
						got, err := c.Search(ctx, query, 7, options)
						want, refErr := reference.Search(ctx, query, 7, options)
						if err != nil || refErr != nil || !reflect.DeepEqual(got, want) {
							t.Fatalf("query %d options %d: %+v/%v want %+v/%v", qi, i, got, err, want, refErr)
						}
					}
				}
				if native != 21 || full != 0 {
					t.Fatalf("FLAT queries did not stay native: native=%d full=%d", native, full)
				}
			})
		}
	}
}

func TestIntegrationNativeFlatTiesAndRoundoff(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s, vectordata.IndexMethodFlat)
	rows := make([]vectordata.ModelRecord, 192)
	for i := range rows {
		rows[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Value": "same"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
	}
	full, native := 0, 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, readHash) {
			full++
		}
		if scriptIs(cmd, nativeExecute) {
			native++
		}
		return next(ctx, cmd)
	}})
	for _, near := range []bool{false, true} {
		if near {
			// FLOAT32 squared distances all round to 1, but Go float64 distance
			// distinguishes these. The true nearest key is outside a small K's
			// arbitrary Redis tie set; every matching vector must be remeasured.
			for i := range rows {
				rows[i].Vectors["Vector"] = []float32{1, 1e-4 * float32(len(rows)-i) / float32(len(rows))}
			}
		}
		if _, err := c.Upsert(ctx, rows); err != nil {
			t.Fatal(err)
		}
		reference, err := c.engine(ctx)
		if err != nil {
			t.Fatal(err)
		}
		full, native = 0, 0
		opts := vectordata.ModelSearchOptions{Skip: 2, IncludeVectors: true}
		got, err := c.Search(ctx, []float32{0, 0}, 3, opts)
		want, refErr := reference.Search(ctx, []float32{0, 0}, 3, opts)
		if err != nil || refErr != nil || !reflect.DeepEqual(got, want) || len(got) != 3 || full != 0 || native != 1 {
			t.Fatalf("near=%t: %+v/%v want %+v/%v full=%d native=%d", near, got, err, want, refErr, full, native)
		}
		if !near && got[0].Record.Key != int64(2) || near && got[0].Record.Key != int64(189) {
			t.Fatalf("native-key/near-distance ordering: %+v", got)
		}
	}
}

func TestIntegrationNativeVectorAlgorithmDrift(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodFlat, vectordata.IndexMethodHNSW} {
		t.Run(string(method), func(t *testing.T) {
			s := integrationStore(t, StoreOptions{NativeIndexes: true})
			c := nativeLifecycleCollection(t, s, method)
			if _, err := c.Insert(ctx, nativeLifecycleRows("kept")); err != nil {
				t.Fatal(err)
			}
			m, _ := currentManifest(t, c)
			wrong := *c
			wrong.definition = c.definition.Clone()
			other := vectordata.IndexMethodFlat
			if method == vectordata.IndexMethodFlat {
				other = vectordata.IndexMethodHNSW
			}
			wrong.definition.Vectors[0].Index = &vectordata.VectorIndexOptions{Method: other}
			var err error
			wrong.definition, err = wrong.definition.Normalize()
			if err != nil {
				t.Fatal(err)
			}
			if err = s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
				t.Fatal(err)
			}
			if err = wrong.createNativeIndex(ctx, m); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("wrong physical algorithm accepted: %v", err)
			}
			if err = c.RebuildNativeIndexes(ctx); err != nil {
				t.Fatal(err)
			}
			if hits, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{}); err != nil || len(hits) != 1 || hits[0].Record.Key != int64(1) {
				t.Fatalf("rebuild did not restore algorithm: %+v %v", hits, err)
			}
		})
	}
}

func TestIntegrationNativeFlatFallbacks(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s, vectordata.IndexMethodFlat)
	if hits, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{}); err != nil || len(hits) != 0 {
		t.Fatalf("empty FLAT collection: %+v %v", hits, err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: int64(10), Data: map[string]any{"Value": "absent"}}}); err != nil {
		t.Fatal(err)
	}
	if hits, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{}); err != nil || len(hits) != 0 {
		t.Fatalf("all vectors absent: %+v %v", hits, err)
	}
	if _, err := c.Insert(ctx, nativeLifecycleRows("kept")); err != nil {
		t.Fatal(err)
	}
	full := 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, readHash) {
			full++
		}
		return next(ctx, cmd)
	}})
	check := func(options vectordata.ModelSearchOptions) {
		t.Helper()
		full = 0
		hits, err := c.Search(ctx, []float32{1, 0}, 1, options)
		if err != nil || len(hits) != 1 || hits[0].Record.Key != int64(1) || full == 0 {
			t.Fatalf("exact fallback: %+v %v reads=%d", hits, err, full)
		}
	}
	check(vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{Exact: true}})
	m, _ := currentManifest(t, c)
	if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
		t.Fatal(err)
	}
	check(vectordata.ModelSearchOptions{})
	if err := c.RebuildNativeIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{{Key: int64(3), Data: map[string]any{"Value": "huge"}, Vectors: map[string][]float32{"Vector": {math.MaxFloat32, 0}}}}); err != nil {
		t.Fatal(err)
	}
	m, _ = currentManifest(t, c)
	if len(m.IndexVectors) != 0 {
		t.Fatal("unsafe FLAT vectors were indexed")
	}
	check(vectordata.ModelSearchOptions{})
}
