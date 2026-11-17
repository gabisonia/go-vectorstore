//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

func hybridDefinition(method vectordata.IndexMethod, metric vectordata.DistanceMetric, precision vectordata.VectorPrecision) vectordata.CollectionDefinition {
	return vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: `Body."雪`, Type: vectordata.TypeString, Optional: true, IsFullTextIndexed: true}, {Name: "Group", Type: vectordata.TypeString, IsIndexed: true}, {Name: "JSON", Type: vectordata.TypeJSON}, {Name: "Generation", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Optional: true, Metric: metric, Precision: precision, Index: &vectordata.VectorIndexOptions{Method: method}}}}
}
func hybridRows(generation string) []vectordata.ModelRecord {
	var rows []vectordata.ModelRecord
	for i, key := range []string{"a", "b", "c", "0", "e"} {
		var body any = "apple"
		if key == "a" {
			body = "neutral"
		}
		if key == "e" {
			body = nil
		}
		group := "keep"
		if key == "b" || key == "e" {
			group = "drop"
		}
		row := vectordata.ModelRecord{Key: key, Data: map[string]any{`Body."雪`: body, "Group": group, "JSON": map[string]any{"keep": group == "keep"}, "Generation": generation}, Vectors: map[string][]float32{"Vector": {float32(i) * 3, 1}}}
		if key == "0" {
			row.Vectors = nil
		}
		rows = append(rows, row)
	}
	return rows
}
func hybridCollection(t *testing.T, mode StorageMode, method vectordata.IndexMethod, metric vectordata.DistanceMetric, precision vectordata.VectorPrecision) (*Store, *Collection) {
	t.Helper()
	requireNativeSearch(t)
	s := integrationStore(t, StoreOptions{NativeIndexes: true, StorageMode: mode})
	base, err := s.EnsureModelCollection(context.Background(), "hybrid", hybridDefinition(method, metric, precision), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	if _, err := c.Insert(context.Background(), hybridRows("before")); err != nil {
		t.Fatal(err)
	}
	return s, c
}
func hybridKeys(hits []vectordata.ModelSearchResult) []any {
	out := make([]any, len(hits))
	for i, h := range hits {
		out[i] = h.Record.Key
	}
	return out
}

func TestIntegrationNativeHybridRRF(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodFlat} {
			for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct, vectordata.DistanceL1} {
				for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
					t.Run(string(method)+"/"+string(metric)+"/"+string(precision), func(t *testing.T) {
						ctx := context.Background()
						s, c := hybridCollection(t, mode, method, metric, precision)
						calls := 0
						s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
							if scriptIs(cmd, nativeHybridExecute) {
								calls++
							}
							return next(ctx, cmd)
						}})
						opts := vectordata.HybridSearchOptions{CandidateLimit: 3, ModelSearchOptions: vectordata.ModelSearchOptions{IncludeVectors: true}}
						hits, err := c.HybridSearch(ctx, []float32{0, 1}, "apple", 3, opts)
						if err != nil || !reflect.DeepEqual(hybridKeys(hits), []any{"b", "c", "a"}) {
							t.Fatalf("RRF rank: %+v %v", hits, err)
						}
						want := []float64{1.0/62 + 1.0/61, 1.0/63 + 1.0/62, 1.0 / 61}
						for i, hit := range hits {
							distance := exactsearch.DenseDistance([]float32{0, 1}, hit.Record.Vectors["Vector"], metric)
							if hit.ScoreKind != vectordata.ScoreRRF || math.Abs(hit.Score-want[i]) > 1e-14 || hit.Distance != distance {
								t.Fatalf("RRF score/distance: %+v want %v/%v", hit, want[i], distance)
							}
						}
						if calls == 0 {
							t.Fatal("hybrid bypassed native atomic execution")
						}
						for _, filter := range []vectordata.Filter{vectordata.Eq(vectordata.Column("Group"), "keep"), vectordata.Eq(vectordata.DataPath("JSON", "keep"), true)} {
							o := opts
							o.Filter = filter
							hits, err := c.HybridSearch(ctx, []float32{0, 1}, "apple", 3, o)
							if err != nil || !reflect.DeepEqual(hybridKeys(hits), []any{"c", "a"}) {
								t.Fatalf("both lists must filter before ranking: %+v %v", hits, err)
							}
							if math.Abs(hits[0].Score-(1.0/62+1.0/61)) > 1e-14 {
								t.Fatalf("filter applied after ranking: %+v", hits)
							}
						}
						o := opts
						o.ProviderOptions = SearchTuning{Exact: true}
						exact, err := c.HybridSearch(ctx, []float32{0, 1}, "apple", 3, o)
						if err != nil || !reflect.DeepEqual(hits, exact) {
							t.Fatalf("exact hybrid reference: %+v %v", exact, err)
						}
					})
				}
			}
		}
	})
}

func TestIntegrationNativeHybridPagingThresholdsAndKeywords(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		ctx := context.Background()
		_, c := hybridCollection(t, mode, vectordata.IndexMethodFlat, vectordata.DistanceL2, vectordata.VectorFloat32)
		base := vectordata.HybridSearchOptions{CandidateLimit: 3}
		for _, tc := range []struct {
			name, keywords string
			top            int
			change         func(*vectordata.HybridSearchOptions)
			keys           []any
		}{
			{name: "page", top: 1, change: func(o *vectordata.HybridSearchOptions) { o.Skip = 1 }, keys: []any{"c"}},
			{name: "candidate bound", top: 2, change: func(o *vectordata.HybridSearchOptions) { o.CandidateLimit = 2 }, keys: []any{"b", "a"}},
			{name: "RRF threshold", top: 3, change: func(o *vectordata.HybridSearchOptions) { v := 0.02; o.ScoreThreshold = &v }, keys: []any{"b", "c"}},
			{name: "distance threshold", top: 3, change: func(o *vectordata.HybridSearchOptions) { v := 3.0; o.DistanceThreshold = &v }, keys: []any{"b", "a"}},
			{name: "constant", top: 3, change: func(o *vectordata.HybridSearchOptions) { o.RRFConstant = 1 }, keys: []any{"b", "c", "a"}},
			{name: "large constant", top: 3, change: func(o *vectordata.HybridSearchOptions) { o.RRFConstant = math.MaxInt }, keys: []any{"b", "c", "a"}},
			{name: "no keyword matches", keywords: "absent", top: 3, keys: []any{"a", "b", "c"}},
			{name: "punctuation only", keywords: "|*:()@", top: 3, keys: []any{"a", "b", "c"}},
			{name: "AND plain tokens", keywords: "apple absent", top: 3, keys: []any{"a", "b", "c"}},
			{name: "no query operator injection", keywords: "apple | absent", top: 3, keys: []any{"a", "b", "c"}},
			{name: "no negative operator", keywords: "apple -absent", top: 3, keys: []any{"a", "b", "c"}},
			{name: "case folding", keywords: "APPLE", top: 3, keys: []any{"b", "c", "a"}},
			{name: "stopwords", keywords: "the", top: 3, keys: []any{"a", "b", "c"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				o := base
				if tc.change != nil {
					tc.change(&o)
				}
				keywords := tc.keywords
				if keywords == "" {
					keywords = "apple"
				}
				hits, err := c.HybridSearch(ctx, []float32{0, 1}, keywords, tc.top, o)
				if err != nil || !reflect.DeepEqual(hybridKeys(hits), tc.keys) {
					t.Fatalf("hybrid options: %+v %v want %v", hits, err, tc.keys)
				}
				for _, hit := range hits {
					if hit.Record.Vectors != nil || hit.Score <= 0 || math.IsInf(hit.Score, 0) {
						t.Fatalf("projection/score: %+v", hit)
					}
				}
			})
		}
	})
}

func TestIntegrationNativeHybridGenerationAndEmbedding(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		for _, when := range []string{"before", "after"} {
			t.Run(when, func(t *testing.T) {
				ctx := context.Background()
				s, c := hybridCollection(t, mode, vectordata.IndexMethodHNSW, vectordata.DistanceL2, vectordata.VectorFloat32)
				other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace, NativeIndexes: true, StorageMode: mode})
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				writer, err := other.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				changed, calls, embeddings := false, 0, 0
				c.options.Embedder = vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { embeddings++; return [][]float32{{0, 1}}, nil })
				s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
					if !scriptIs(cmd, nativeHybridExecute) {
						return next(ctx, cmd)
					}
					calls++
					if changed {
						return next(ctx, cmd)
					}
					changed = true
					write := func() error {
						rows := hybridRows("after")
						rows[0].Data[`Body."雪`] = "apple"
						rows[0].Vectors["Vector"] = []float32{9, 1}
						rows[1].Data[`Body."雪`] = "neutral"
						rows[1].Vectors["Vector"] = []float32{0, 1}
						rows[2].Data[`Body."雪`] = "neutral"
						rows[2].Vectors["Vector"] = []float32{3, 1}
						_, err := writer.Upsert(ctx, rows)
						return err
					}
					if when == "before" {
						if err := write(); err != nil {
							return err
						}
						return next(ctx, cmd)
					}
					if err := next(ctx, cmd); err != nil {
						return err
					}
					return write()
				}})
				hits, err := c.HybridSearchText(ctx, "embedding query", "apple", 3, vectordata.HybridSearchOptions{CandidateLimit: 3})
				want, first := "before", any("b")
				if when == "before" {
					want, first = "after", "a"
				}
				if err != nil || len(hits) != 3 || !changed || embeddings != 1 || hits[0].Record.Key != first {
					t.Fatalf("atomic hybrid: %+v %v changed=%v calls=%d embeddings=%d", hits, err, changed, calls, embeddings)
				}
				for _, hit := range hits {
					if hit.Record.Data["Generation"] != want {
						t.Fatalf("mixed generation: %+v", hits)
					}
				}
			})
		}
	})
}

func TestIntegrationNativeHybridIndexReadinessAndInput(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		ctx := context.Background()
		s, c := hybridCollection(t, mode, vectordata.IndexMethodFlat, vectordata.DistanceL2, vectordata.VectorFloat32)
		calls := 0
		c.options.InputEmbedder = vectordata.InputEmbeddingFunc[[]byte](func(context.Context, [][]byte) ([][]float32, error) { calls++; return [][]float32{{0, 1}}, nil })
		o := vectordata.HybridSearchOptions{CandidateLimit: 3}
		hits, err := c.HybridSearchInput(ctx, []byte{1}, "apple", 3, o)
		if err != nil || !reflect.DeepEqual(hybridKeys(hits), []any{"b", "c", "a"}) || calls != 1 {
			t.Fatalf("custom input: %+v %v calls=%d", hits, err, calls)
		}
		m, _ := currentManifest(t, c)
		if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.HybridSearchInput(ctx, []byte{1}, "apple", 3, o); !errors.Is(err, vectordata.ErrUnsupported) || calls != 1 {
			t.Fatalf("missing index embedded/returned partial hybrid: %v calls=%d", err, calls)
		}
		if err := c.RebuildNativeIndexes(ctx); err != nil {
			t.Fatal(err)
		}
		m, _ = currentManifest(t, c)
		if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
			t.Fatal(err)
		}
		keytype, path := "HASH", "wrong"
		if mode == StorageJSON {
			keytype, path = "JSON", "$.__wrong"
		}
		if err := s.client.Do(ctx, "FT.CREATE", m.Index, "ON", keytype, "PREFIX", 1, "wrong-prefix:", "SCHEMA", path, "AS", "wrong", "TEXT").Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.HybridSearchInput(ctx, []byte{1}, "apple", 3, o); !errors.Is(err, vectordata.ErrSchemaMismatch) || calls != 1 {
			t.Fatalf("drifted index embedded/escaped: %v calls=%d", err, calls)
		}
	})
}

func TestIntegrationNativeHybridTiesAndKeyPrecision(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		for _, kind := range []vectordata.KeyType{vectordata.KeyString, vectordata.KeyInt64, vectordata.KeyUint64, vectordata.KeyUUID} {
			t.Run(string(kind), func(t *testing.T) {
				ctx := context.Background()
				s := integrationStore(t, StoreOptions{NativeIndexes: true, StorageMode: mode})
				d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: kind}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}}}
				base, err := s.EnsureModelCollection(ctx, "ties", d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c := base.(*Collection)
				rows := make([]vectordata.ModelRecord, 192)
				for i := range rows {
					var key any = fmt.Sprintf("%03d", i)
					switch kind {
					case vectordata.KeyInt64:
						key = int64(math.MinInt64) + int64(i)
					case vectordata.KeyUint64:
						key = uint64(math.MaxUint64) - 192 + uint64(i)
					case vectordata.KeyUUID:
						key = fmt.Sprintf("00000000-0000-0000-0000-%012x", i+1)
					}
					rows[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": "apple"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
				}
				if _, err := c.Insert(ctx, rows); err != nil {
					t.Fatal(err)
				}
				scans := 0
				s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
					if scriptIs(cmd, nativeHybridExecute) && cmd.Args()[2].(int) > 3 {
						scans++
					}
					return next(ctx, cmd)
				}})
				hits, err := c.HybridSearch(ctx, []float32{0, 0}, "apple", 3, vectordata.HybridSearchOptions{CandidateLimit: 5, ModelSearchOptions: vectordata.ModelSearchOptions{Skip: 1, IncludeVectors: true}})
				want := []any{rows[1].Key, rows[2].Key, rows[3].Key}
				if err != nil || !reflect.DeepEqual(hybridKeys(hits), want) || scans != 1 {
					t.Fatalf("tie/key precision: %+v %v scans=%d want %v", hits, err, scans, want)
				}
				for i, hit := range hits {
					if hit.Distance != 1 || math.Abs(hit.Score-2.0/float64(62+i)) > 1e-14 {
						t.Fatalf("stable RRF ties: %+v", hits)
					}
				}
			})
		}
	})
}

func TestIntegrationNativeHybridHNSWRecall(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		ctx := context.Background()
		s := integrationStore(t, StoreOptions{NativeIndexes: true, StorageMode: mode})
		d := hybridDefinition(vectordata.IndexMethodHNSW, vectordata.DistanceL2, vectordata.VectorFloat32)
		d.Vectors[0].Dimensions = 16
		base, err := s.EnsureModelCollection(ctx, "recall", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		c := base.(*Collection)
		rng := rand.New(rand.NewSource(77))
		rows := make([]vectordata.ModelRecord, 512)
		for i := range rows {
			v := make([]float32, 16)
			for j := range v {
				v[j] = rng.Float32()*2 - 1
			}
			rows[i] = vectordata.ModelRecord{Key: fmt.Sprintf("%04d", i), Data: map[string]any{`Body."雪`: "neutral", "Group": "keep", "JSON": map[string]any{}, "Generation": "seed"}, Vectors: map[string][]float32{"Vector": v}}
		}
		if _, err := c.Insert(ctx, rows); err != nil {
			t.Fatal(err)
		}
		calls, scans, hits, total := 0, 0, 0, 0
		s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
			if scriptIs(cmd, nativeHybridExecute) {
				calls++
				if cmd.Args()[2].(int) > 3 {
					scans++
				}
			}
			return next(ctx, cmd)
		}})
		for qi := range 3 {
			q := rows[qi*7].Vectors["Vector"]
			want, err := c.Search(ctx, q, 10, vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{Exact: true}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.HybridSearch(ctx, q, "absent", 10, vectordata.HybridSearchOptions{CandidateLimit: 10, ModelSearchOptions: vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{EFRuntime: 200}, IncludeVectors: true}})
			if err != nil || len(got) != 10 {
				t.Fatalf("hybrid recall: %+v %v", got, err)
			}
			expected := map[any]bool{}
			for _, hit := range want {
				expected[hit.Record.Key] = true
			}
			for i, hit := range got {
				if expected[hit.Record.Key] {
					hits++
				}
				if hit.Score != 1.0/float64(61+i) || hit.Distance != exactsearch.DenseDistance(q, hit.Record.Vectors["Vector"], vectordata.DistanceL2) {
					t.Fatalf("remeasure/RRF: %+v", hit)
				}
			}
			total += len(want)
		}
		if recall := float64(hits) / float64(total); recall < .9 || calls != 3 || scans != 0 {
			t.Fatalf("hybrid recall %.3f calls=%d scans=%d", recall, calls, scans)
		} else {
			t.Logf("hybrid recall@10 %.3f", recall)
		}
	})
}

func TestIntegrationNativeHybridOptionsSnapshotAndLegacy(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		ctx := context.Background()
		_, c := hybridCollection(t, mode, vectordata.IndexMethodFlat, vectordata.DistanceL2, vectordata.VectorFloat32)
		values := []any{"keep"}
		threshold := .02
		calls := 0
		c.options.InputEmbedder = vectordata.InputEmbeddingFunc[[]byte](func(ctx context.Context, _ [][]byte) ([][]float32, error) {
			calls++
			values[0] = "drop"
			threshold = .99
			if _, err := c.Count(ctx, nil); err != nil {
				return nil, err
			}
			return [][]float32{{0, 1}}, nil
		})
		o := vectordata.HybridSearchOptions{CandidateLimit: 3, ModelSearchOptions: vectordata.ModelSearchOptions{Filter: vectordata.InFilter{Field: vectordata.Column("Group"), Values: values}, ScoreThreshold: &threshold}}
		hits, err := c.HybridSearchInput(ctx, []byte{1}, "apple", 3, o)
		if err != nil || !reflect.DeepEqual(hybridKeys(hits), []any{"c"}) || calls != 1 {
			t.Fatalf("callback changed request snapshot: %+v %v calls=%d", hits, err, calls)
		}
		old := *c
		old.definition = c.definition.Clone()
		old.definition.Data[0].IsFullTextIndexed = false
		if _, err := old.Upsert(ctx, hybridRows("legacy")); err != nil {
			t.Fatal(err)
		}
		_, before := currentManifest(t, c)
		if _, err := c.HybridSearchInput(ctx, []byte{1}, "apple", 3, vectordata.HybridSearchOptions{CandidateLimit: 3}); !errors.Is(err, vectordata.ErrUnsupported) || calls != 1 {
			t.Fatalf("legacy index implicitly upgraded/embedded: %v calls=%d", err, calls)
		}
		_, after := currentManifest(t, c)
		if string(before) != string(after) {
			t.Fatal("hybrid read rebuilt legacy index")
		}
		if err := c.RebuildNativeIndexes(ctx); err != nil {
			t.Fatal(err)
		}
		hits, err = c.HybridSearch(ctx, []float32{0, 1}, "apple", 3, vectordata.HybridSearchOptions{CandidateLimit: 3})
		if err != nil || !reflect.DeepEqual(hybridKeys(hits), []any{"b", "c", "a"}) {
			t.Fatalf("explicit hybrid rebuild: %+v %v", hits, err)
		}
	})
}

func TestIntegrationNativeHybridIncompleteReplies(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		for _, phase := range []string{"keyword count", "keyword score", "payload count"} {
			t.Run(phase, func(t *testing.T) {
				ctx := context.Background()
				s, c := hybridCollection(t, mode, vectordata.IndexMethodFlat, vectordata.DistanceL2, vectordata.VectorFloat32)
				calls := 0
				s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
					if err := next(ctx, cmd); err != nil {
						return err
					}
					if scriptIs(cmd, nativeHybridExecute) {
						calls++
						reply := cmd.(*goredis.Cmd).Val().([]any)
						switch phase {
						case "keyword count":
							row := reply[2].([]any)
							row[0] = row[0].(int64) + 1
						case "keyword score":
							row := reply[2].([]any)
							row[2] = "+Inf"
						case "payload count":
							row := reply[3].([]any)
							row[0] = row[0].(int64) + 1
						}
					}
					return nil
				}})
				want := vectordata.ErrInvalidRecord
				if phase == "keyword count" {
					want = vectordata.ErrUnsupported
				}
				hits, err := c.HybridSearch(ctx, []float32{0, 1}, "apple", 3, vectordata.HybridSearchOptions{CandidateLimit: 3})
				if !errors.Is(err, want) || len(hits) != 0 || calls != 1 {
					t.Fatalf("incomplete/malformed ranks escaped: %+v %v calls=%d", hits, err, calls)
				}
			})
		}
	})
}

func TestIntegrationNativeHybridFlatRoundoff(t *testing.T) {
	runNativeStorage(t, func(t *testing.T, mode StorageMode) {
		ctx := context.Background()
		s := integrationStore(t, StoreOptions{NativeIndexes: true, StorageMode: mode})
		d := hybridDefinition(vectordata.IndexMethodFlat, vectordata.DistanceL2, vectordata.VectorFloat32)
		d.Key.Type = vectordata.KeyInt64
		base, err := s.EnsureModelCollection(ctx, "roundoff", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		c := base.(*Collection)
		rows := make([]vectordata.ModelRecord, 192)
		for i := range rows {
			rows[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{`Body."雪`: "neutral", "Group": "keep", "JSON": map[string]any{}, "Generation": "fixture"}, Vectors: map[string][]float32{"Vector": {1, 1e-4 * float32(len(rows)-i) / float32(len(rows))}}}
		}
		if _, err := c.Insert(ctx, rows); err != nil {
			t.Fatal(err)
		}
		hits, err := c.HybridSearch(ctx, []float32{0, 0}, "absent", 3, vectordata.HybridSearchOptions{CandidateLimit: 5, ModelSearchOptions: vectordata.ModelSearchOptions{Skip: 2}})
		if err != nil || !reflect.DeepEqual(hybridKeys(hits), []any{int64(189), int64(188), int64(187)}) {
			t.Fatalf("FLOAT32 index rounding discarded exact nearest keys: %+v %v", hits, err)
		}
	})
}
