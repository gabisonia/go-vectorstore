//go:build integration

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
	goredis "github.com/redis/go-redis/v9"
)

func requireNativeSearch(t *testing.T) {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: integrationAddr, Protocol: 2, MaxRetries: -1})
	defer client.Close()
	info, err := client.Do(context.Background(), "COMMAND", "INFO", "FT.CREATE").Slice()
	if err != nil {
		t.Fatal(err)
	}
	if len(info) == 0 || info[0] == nil {
		if os.Getenv("REDIS_REQUIRE_SEARCH") == "true" {
			t.Fatal("Redis Search is required")
		}
		t.Skip("Redis Search module required")
	}
}

func TestIntegrationNativeModelContract(t *testing.T) {
	requireNativeSearch(t)
	conformance.Run(t, func(t *testing.T) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{NativeIndexes: true})
	})
}
func TestIntegrationNativeEmbeddingConfiguration(t *testing.T) {
	requireNativeSearch(t)
	conformance.RunEmbeddingConfiguration(t, func(t *testing.T, o vectordata.ModelCollectionOptions) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{NativeIndexes: true, Embedder: o.Embedder, InputEmbedder: o.InputEmbedder, EmbeddingBatchSize: o.EmbeddingBatchSize})
	})
}
func TestIntegrationNativeFilterParity(t *testing.T) {
	requireNativeSearch(t)
	conformance.RunFilterParity(t, func(t *testing.T) vectordata.ModelStore { return memory.New() }, func(t *testing.T) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{NativeIndexes: true})
	})
}

func TestIntegrationNativeScalarPrecisionAndNulls(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	for _, kind := range []vectordata.KeyType{vectordata.KeyString, vectordata.KeyInt64, vectordata.KeyUint64, vectordata.KeyUUID} {
		t.Run(string(kind), func(t *testing.T) {
			s := integrationStore(t, StoreOptions{NativeIndexes: true})
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: kind}, Data: []vectordata.DataProperty{{Name: `text."雪`, Type: vectordata.TypeString, Optional: true, IsIndexed: true}, {Name: "Int", Type: vectordata.TypeInt64, Optional: true, IsIndexed: true}, {Name: "Float", Type: vectordata.TypeFloat64, Optional: true, IsIndexed: true}, {Name: "Bool", Type: vectordata.TypeBool, Optional: true, IsIndexed: true}, {Name: "JSON", Type: vectordata.TypeJSON, Optional: true}, {Name: "Decimal", Type: vectordata.TypeDecimal, Optional: true}}}
			keys := []any{"a", "comma, space", "zero\x00byte", "雪", "\ufffd", "z", "quote'", "last"}
			if kind == vectordata.KeyInt64 {
				keys = []any{int64(math.MinInt64), int64(-9007199254740993), int64(-1), int64(0), int64(2), int64(9007199254740992), int64(9007199254740993), int64(math.MaxInt64)}
			}
			if kind == vectordata.KeyUint64 {
				keys = []any{uint64(0), uint64(2), uint64(9007199254740992), uint64(9007199254740993), uint64(math.MaxInt64), uint64(math.MaxInt64) + 1, uint64(math.MaxUint64) - 1, uint64(math.MaxUint64)}
			}
			if kind == vectordata.KeyUUID {
				keys = []any{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "ffffffff-ffff-ffff-ffff-ffffffffffff"}
			}
			texts := []any{nil, "a,b", "zero\x00byte", "雪", "a b", strings.Repeat("long", 300), "", "quote'"}
			ints := []any{nil, int64(math.MinInt64), int64(-9007199254740993), int64(-1), int64(0), int64(9007199254740992), int64(9007199254740993), int64(math.MaxInt64)}
			floats := []any{nil, -math.MaxFloat64, -1.0, math.SmallestNonzeroFloat64, math.Copysign(0, -1), 1.0, math.Nextafter(1, 2), math.MaxFloat64}
			var records []vectordata.ModelRecord
			for i, key := range keys {
				var b any = i%2 == 0
				if i%3 == 0 {
					b = nil
				}
				records = append(records, vectordata.ModelRecord{Key: key, Data: map[string]any{d.Data[0].Name: texts[i], "Int": ints[i], "Float": floats[i], "Bool": b, "JSON": map[string]any{"n": 1}, "Decimal": "0.10000000000000000001"}})
			}
			base, err := s.EnsureModelCollection(ctx, "precision", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			c := base.(*Collection)
			if _, err = c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			reference, err := c.engine(ctx)
			if err != nil {
				t.Fatal(err)
			}
			col := vectordata.Column(d.Data[0].Name)
			filters := []vectordata.Filter{nil, vectordata.Eq(col, nil), vectordata.Eq(col, "a,b"), vectordata.Eq(col, "zero\x00byte"), vectordata.Eq(col, ""), vectordata.Eq(col, texts[5]), vectordata.Not(vectordata.Eq(col, "a,b")), vectordata.Not(vectordata.In(col, "a,b", nil)), vectordata.In(col, nil, "雪", "quote'"), vectordata.Exists(col), vectordata.Eq(vectordata.Column("Bool"), false), vectordata.Eq(vectordata.Column("Bool"), nil), vectordata.Not(vectordata.Eq(vectordata.Column("Bool"), true)), vectordata.In(vectordata.Column("Float"), 0.0, math.MaxFloat64, nil), vectordata.Eq(vectordata.Column("Float"), math.SmallestNonzeroFloat64), vectordata.Gt(vectordata.Column("Float"), 1.0), vectordata.Eq(vectordata.DataPath("JSON", "n"), 1), vectordata.Eq(vectordata.Column("Decimal"), "0.10000000000000000001"), vectordata.And(vectordata.Exists(col), vectordata.Or(vectordata.Eq(vectordata.Column("Bool"), true), vectordata.Eq(col, "a b")))}
			for _, value := range ints[1:] {
				filters = append(filters, vectordata.Gt(vectordata.Column("Int"), value), vectordata.Gte(vectordata.Column("Int"), value), vectordata.Lt(vectordata.Column("Int"), value), vectordata.Lte(vectordata.Column("Int"), value), vectordata.Not(vectordata.Gt(vectordata.Column("Int"), value)))
			}
			for _, value := range keys {
				filters = append(filters, vectordata.Eq(vectordata.Column("ID"), value), vectordata.Gt(vectordata.Column("ID"), value), vectordata.Lte(vectordata.Column("ID"), value))
			}
			filters = append(filters, vectordata.Not(vectordata.In(vectordata.Column("ID"), keys[0], nil)))
			if kind == vectordata.KeyString {
				filters = append(filters, vectordata.Eq(vectordata.Column("ID"), string([]byte{0xff})), vectordata.Not(vectordata.Eq(vectordata.Column("ID"), string([]byte{0xff}))))
			}
			for i, filter := range filters {
				got, err := c.Count(ctx, filter)
				want, wantErr := reference.Count(ctx, filter)
				if err != nil || wantErr != nil || got != want {
					t.Fatalf("count %d %T: %d/%v want %d/%v", i, filter, got, err, want, wantErr)
				}
				for _, opts := range []vectordata.QueryOptions{{}, {OrderBy: []vectordata.SortField{{Property: "Int", Descending: true}, {Property: d.Data[0].Name}}, Skip: 1}, {OrderBy: []vectordata.SortField{{Property: "Float"}}}, {OrderBy: []vectordata.SortField{{Property: "ID", Descending: true}}, Skip: 2}} {
					got, err := c.Query(ctx, filter, 3, opts)
					want, wantErr := reference.Query(ctx, filter, 3, opts)
					if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("query %d %T: %+v/%v want %+v/%v", i, filter, got, err, want, wantErr)
					}
				}
			}
		})
	}
}

func TestIntegrationNativeHNSWRecallAndMetrics(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct} {
		for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
			t.Run(string(metric)+"/"+string(precision), func(t *testing.T) {
				s := integrationStore(t, StoreOptions{NativeIndexes: true})
				d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeString, IsIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 16, Metric: metric, Precision: precision, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}}}
				base, err := s.EnsureModelCollection(ctx, "recall", d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c := base.(*Collection)
				rng := rand.New(rand.NewSource(44))
				rows := make([]vectordata.ModelRecord, 512)
				for i := range rows {
					v := make([]float32, 16)
					for j := range v {
						v[j] = rng.Float32()*2 - 1
					}
					group := "other"
					if i%3 == 0 {
						group = "selected"
					}
					rows[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Group": group}, Vectors: map[string][]float32{"Vector": v}}
				}
				if _, err = c.Insert(ctx, rows); err != nil {
					t.Fatal(err)
				}
				hits, total := 0, 0
				for qi := range 6 {
					q := rows[qi*7].Vectors["Vector"]
					want, err := c.Search(ctx, q, 10, vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{Exact: true}, IncludeVectors: true})
					if err != nil {
						t.Fatal(err)
					}
					got, err := c.Search(ctx, q, 10, vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{EFRuntime: 200}, IncludeVectors: true})
					if err != nil || len(got) != 10 {
						t.Fatalf("ANN results: %d %v", len(got), err)
					}
					wanted := map[any]bool{}
					for _, hit := range want {
						wanted[hit.Record.Key] = true
					}
					for i, hit := range got {
						if wanted[hit.Record.Key] {
							hits++
						}
						normalized, err := vectordata.NormalizeModelVector(q, c.definition.Vectors[0])
						if err != nil {
							t.Fatal(err)
						}
						distance := exactsearch.DenseDistance(normalized, hit.Record.Vectors["Vector"], metric)
						if hit.Distance != distance || hit.Score != vectordata.ScoreFromDistance(metric, distance) || hit.ScoreKind != vectordata.ScoreSimilarity || i > 0 && hit.Distance < got[i-1].Distance {
							t.Fatalf("distance/score/order: %+v", hit)
						}
					}
					total += len(want)
				}
				if recall := float64(hits) / float64(total); recall < 0.90 {
					t.Fatalf("recall@10 %.3f", recall)
				} else {
					t.Logf("recall@10 %.3f", recall)
				}
				q := rows[0].Vectors["Vector"]
				filter := vectordata.Eq(vectordata.Column("Group"), "selected")
				want, err := c.Search(ctx, q, 5, vectordata.ModelSearchOptions{Filter: filter, Skip: 2, ProviderOptions: SearchTuning{Exact: true}})
				if err != nil {
					t.Fatal(err)
				}
				got, err := c.Search(ctx, q, 5, vectordata.ModelSearchOptions{Filter: filter, Skip: 2})
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("filtered paging: %+v/%v want %+v", got, err, want)
				}
				threshold := want[2].Distance
				got, err = c.Search(ctx, q, 10, vectordata.ModelSearchOptions{Filter: filter, DistanceThreshold: &threshold})
				reference, refErr := c.Search(ctx, q, 10, vectordata.ModelSearchOptions{Filter: filter, DistanceThreshold: &threshold, ProviderOptions: SearchTuning{Exact: true}})
				if err != nil || refErr != nil || !reflect.DeepEqual(got, reference) {
					t.Fatalf("thresholds: %+v/%v want %+v/%v", got, err, reference, refErr)
				}
			})
		}
	}
}

func nativeLifecycleCollection(t *testing.T, s *Store, methods ...vectordata.IndexMethod) *Collection {
	t.Helper()
	method := vectordata.IndexMethodHNSW
	if len(methods) > 0 {
		method = methods[0]
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64, AutoGenerated: true}, Data: []vectordata.DataProperty{{Name: "Value", Type: vectordata.TypeString, IsIndexed: true, IsFullTextIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2Squared, Optional: true, Index: &vectordata.VectorIndexOptions{Method: method}}}}
	base, err := s.EnsureModelCollection(context.Background(), "lifecycle", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return base.(*Collection)
}
func nativeLifecycleRows(value string) []vectordata.ModelRecord {
	return []vectordata.ModelRecord{{Key: int64(1), Data: map[string]any{"Value": value}, Vectors: map[string][]float32{"Vector": {1, 0}}}, {Key: int64(2), Data: map[string]any{"Value": value}, Vectors: map[string][]float32{"Vector": {0, 1}}}}
}

func TestIntegrationNativeFallbacksAndOptionalVectors(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	rows := nativeLifecycleRows("kept")
	rows = append(rows, vectordata.ModelRecord{Key: int64(3), Data: map[string]any{"Value": "kept"}})
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	for _, exact := range []bool{false, true} {
		hits, err := c.Search(ctx, []float32{1, 0}, 10, vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{Exact: exact}})
		if err != nil || len(hits) != 2 || hits[0].Record.Key != int64(1) || hits[1].Distance != 2 {
			t.Fatalf("optional vectors: %+v %v", hits, err)
		}
	}
	m, _ := currentManifest(t, c)
	if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Value"), "kept")); err != nil || n != 3 {
		t.Fatalf("missing index fallback: %d %v", n, err)
	}
	if err := c.RebuildNativeIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	rows[0].Vectors["Vector"] = []float32{1e20, 0}
	if _, err := c.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	m, _ = currentManifest(t, c)
	if len(m.IndexVectors) != 0 {
		t.Fatal("unsafe generation advertised an HNSW field")
	}
	hits, err := c.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{})
	reference, refErr := c.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{ProviderOptions: SearchTuning{Exact: true}})
	if err != nil || refErr != nil || !reflect.DeepEqual(hits, reference) {
		t.Fatalf("extreme-vector fallback: %+v/%v %+v/%v", hits, err, reference, refErr)
	}
}

func TestIntegrationNativeTiesExpandCandidates(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	rows := make([]vectordata.ModelRecord, 200)
	for i := range rows {
		rows[i] = vectordata.ModelRecord{Key: int64(200 - i), Data: map[string]any{"Value": "tie"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(ctx, []float32{1, 0}, 3, vectordata.ModelSearchOptions{Skip: 2})
	if err != nil || len(got) != 3 || got[0].Record.Key != int64(3) || got[2].Record.Key != int64(5) {
		t.Fatalf("native key tie boundary: %+v %v", got, err)
	}
}

func TestIntegrationNativeReadsRemainOnOneGeneration(t *testing.T) {
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodFlat} {
		t.Run(string(method), func(t *testing.T) { testNativeReadsRemainOnOneGeneration(t, method) })
	}
}

func testNativeReadsRemainOnOneGeneration(t *testing.T, method vectordata.IndexMethod) {
	requireNativeSearch(t)
	for _, operation := range []string{"query", "count", "search"} {
		for _, when := range []string{"before", "after"} {
			t.Run(operation+"/"+when, func(t *testing.T) {
				ctx := context.Background()
				s := integrationStore(t, StoreOptions{NativeIndexes: true})
				c := nativeLifecycleCollection(t, s, method)
				if _, err := c.Insert(ctx, nativeLifecycleRows("before")); err != nil {
					t.Fatal(err)
				}
				other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace, NativeIndexes: true})
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				base, err := other.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				changed := false
				s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
					if !scriptIs(cmd, nativeExecute) || changed {
						return next(ctx, cmd)
					}
					changed = true
					write := func() error { _, err := base.Upsert(ctx, nativeLifecycleRows("after")); return err }
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
				want := "before"
				if when == "before" {
					want = "after"
				}
				filter := vectordata.Exists(vectordata.Column("Value"))
				switch operation {
				case "count":
					n, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Value"), "before"))
					expected := int64(2)
					if when == "before" {
						expected = 0
					}
					if err != nil || n != expected {
						t.Fatalf("mixed count: %d %v", n, err)
					}
				case "query":
					rows, err := c.Query(ctx, filter, 2, vectordata.QueryOptions{})
					if err != nil || len(rows) != 2 || rows[0].Data["Value"] != want || rows[1].Data["Value"] != want {
						t.Fatalf("mixed query: %+v %v", rows, err)
					}
				case "search":
					hits, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{Filter: filter})
					if err != nil || len(hits) != 1 || hits[0].Record.Data["Value"] != want {
						t.Fatalf("mixed search: %+v %v", hits, err)
					}
				}
				if !changed {
					t.Fatal("test did not enter native read")
				}
			})
		}
	}
}

func TestIntegrationNativeIndexDriftAndReadiness(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	if _, err := c.Insert(ctx, nativeLifecycleRows("kept")); err != nil {
		t.Fatal(err)
	}
	m, _ := currentManifest(t, c)
	if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.client.Do(ctx, "FT.CREATE", m.Index, "ON", "HASH", "PREFIX", 1, "wrong-prefix:", "SCHEMA", "__vs_revision", "AS", "revision", "TAG", "CASESENSITIVE").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Count(ctx, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("index drift not detected: %v", err)
	}
	if err := c.RebuildNativeIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	// An unexpected indexed document makes the index incomplete with respect to
	// the manifest. Native reads must fall back to canonical manifest records.
	m, _ = currentManifest(t, c)
	foreign := c.generationPrefix(m.Revision) + "r:unexpected"
	if err := s.client.HSet(ctx, foreign, "__vs_revision", m.Revision).Err(); err != nil {
		t.Fatal(err)
	}
	fields, err := c.nativeFields(m)
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := json.Marshal(fields)
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		reply, err := s.client.Eval(wait, nativeIndexStatus, []string{m.Index}, c.generationPrefix(m.Revision), string(schema), 3).Slice()
		if err != nil {
			t.Fatal(err)
		}
		status, err := nativeStatus(reply)
		if err != nil {
			t.Fatal(err)
		}
		if status == 1 {
			break
		}
		select {
		case <-wait.Done():
			t.Fatal("foreign document was not indexed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	full := 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, readHash) {
			full++
		}
		return next(ctx, cmd)
	}})
	if n, err := c.Count(ctx, nil); err != nil || n != 2 {
		t.Fatalf("not-ready index returned wrong count: %d %v", n, err)
	}
	if full == 0 {
		t.Fatal("not-ready index was used without canonical fallback")
	}
	if err := s.client.Unlink(ctx, foreign).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationNativeUnknownPublicationReplyAndPruning(t *testing.T) {
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodFlat} {
		t.Run(string(method), func(t *testing.T) { testNativeUnknownPublicationReplyAndPruning(t, method) })
	}
}

func testNativeUnknownPublicationReplyAndPruning(t *testing.T, method vectordata.IndexMethod) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s, method)
	calls := 0
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		err := next(ctx, cmd)
		if scriptIs(cmd, publishHash) {
			calls++
			if err == nil {
				return context.DeadlineExceeded
			}
		}
		return err
	}})
	if keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "once"}, Vectors: map[string][]float32{"Vector": {1, 0}}}}); keys != nil || !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("publication replayed: %v %v %d", keys, err, calls)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 1 {
		t.Fatalf("lost indexed publication: %d %v", n, err)
	}
	if n, err := s.PruneNativeIndexes(ctx, c.name); err != nil || n < 1 {
		t.Fatalf("retired index pruning: %d %v", n, err)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 1 {
		t.Fatalf("prune removed current index: %d %v", n, err)
	}
}

func TestIntegrationNativePruningProtectsActiveStaging(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	m, raw := currentManifest(t, c)
	revision, err := vectordata.NewUUIDKey()
	if err != nil {
		t.Fatal(err)
	}
	m.Revision, m.Index, m.IndexVectors, m.Tokens = revision, c.indexName(revision), nil, []string{}
	lease := c.generationPrefix(revision) + "lease"
	if err := s.client.Set(ctx, lease, revision, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if err := c.createNativeIndex(ctx, m); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneNativeIndexes(ctx, c.name); err != nil || n != 0 {
		t.Fatalf("active staging pruned: %d %v", n, err)
	}
	// Redis can return PTTL zero for a still-live lease in its last millisecond.
	// Override only this clock-dependent reply to exercise the same real Lua guard
	// deterministically; publishing can still succeed while the lease exists.
	zeroTTL := `local actual=redis; local redis={call=function(cmd,...) if cmd=='PTTL' then return 0 end return actual.call(cmd,...) end};` + nativePruneGuard
	if ok, err := s.client.Eval(ctx, zeroTTL, []string{s.key(c.name), lease}, "present", raw).Int(); err != nil || ok != 0 {
		t.Fatalf("zero-TTL lease unprotected: %d %v", ok, err)
	}
	if err := s.client.Del(ctx, lease).Err(); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneNativeIndexes(ctx, c.name); err != nil || n != 1 {
		t.Fatalf("abandoned staging not pruned: %d %v", n, err)
	}
}

func TestIntegrationNativeBuildFailureDoesNotPublish(t *testing.T) {
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodFlat} {
		t.Run(string(method), func(t *testing.T) { testNativeBuildFailureDoesNotPublish(t, method) })
	}
}

func testNativeBuildFailureDoesNotPublish(t *testing.T, method vectordata.IndexMethod) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s, method)
	_, before := currentManifest(t, c)
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if cmd.Name() == "ft.create" {
			return errors.New("injected index creation failure")
		}
		return next(ctx, cmd)
	}})
	if keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "not published"}, Vectors: map[string][]float32{"Vector": {1, 0}}}}); err == nil || keys != nil {
		t.Fatalf("expected native build error: %v %v", keys, err)
	}
	_, after := currentManifest(t, c)
	if string(before) != string(after) {
		t.Fatal("index build failure advanced manifest/sequence")
	}
}

func TestIntegrationNativeExistingHashClientsAndExplicitRebuild(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	plain := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	c := nativeLifecycleCollection(t, plain)
	if _, err := c.Insert(ctx, nativeLifecycleRows("plain")); err != nil {
		t.Fatal(err)
	}
	m, _ := currentManifest(t, c)
	if m.Index != "" {
		t.Fatal("default hash mode created a native index")
	}
	native, err := New(StoreOptions{Addr: integrationAddr, Namespace: plain.metadata.Namespace, NativeIndexes: true})
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	base, err := native.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	indexed := base.(*Collection)
	if n, err := indexed.Count(ctx, nil); err != nil || n != 2 {
		t.Fatalf("plain collection fallback: %d %v", n, err)
	}
	if err := indexed.RebuildNativeIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ = currentManifest(t, indexed)
	if m.Index == "" {
		t.Fatal("explicit rebuild did not create index")
	}
	if _, err := c.Upsert(ctx, nativeLifecycleRows("plain writer")); err != nil {
		t.Fatal(err)
	}
	m, _ = currentManifest(t, c)
	if m.Index != "" {
		t.Fatal("plain writer unexpectedly provisioned native indexes")
	}
	if n, err := indexed.Count(ctx, vectordata.Eq(vectordata.Column("Value"), "plain writer")); err != nil || n != 2 {
		t.Fatalf("plain writer damaged native reader compatibility: %d %v", n, err)
	}
}

func TestIntegrationNativeUnavailableModuleDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	info, err := s.client.Do(ctx, "COMMAND", "INFO", "FT.CREATE").Slice()
	if err != nil {
		t.Fatal(err)
	}
	if len(info) > 0 && info[0] != nil {
		t.Skip("test requires standalone Redis without Search")
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	if _, err := s.EnsureModelCollection(ctx, "unsupported", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("missing module: %v", err)
	}
	if exists, err := s.CollectionExists(ctx, "unsupported"); err != nil || exists {
		t.Fatalf("failed native creation published metadata: %t %v", exists, err)
	}
}

func TestIntegrationNativeReadinessWaitDoesNotPublish(t *testing.T) {
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodFlat} {
		t.Run(string(method), func(t *testing.T) { testNativeReadinessWaitDoesNotPublish(t, method) })
	}
}

func testNativeReadinessWaitDoesNotPublish(t *testing.T, method vectordata.IndexMethod) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s, method)
	_, before := currentManifest(t, c)
	waited := 0
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, nativeIndexStatus) {
			waited++
			cmd.(*goredis.Cmd).SetVal([]any{int64(2)})
			return nil
		}
		return next(ctx, cmd)
	}})
	deadline, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if keys, err := c.Insert(deadline, []vectordata.ModelRecord{{Data: map[string]any{"Value": "waiting"}, Vectors: map[string][]float32{"Vector": {1, 0}}}}); keys != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness timeout: %v %v", keys, err)
	}
	_, after := currentManifest(t, c)
	if waited == 0 || string(before) != string(after) {
		t.Fatal("unready index was published")
	}
	m, _ := currentManifest(t, c)
	for _, key := range auxiliaryKeys(t, s, c.name) {
		if !strings.HasPrefix(key, c.generationPrefix(m.Revision)) {
			if err := s.client.Unlink(ctx, key).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n, err := s.PruneNativeIndexes(ctx, c.name); err != nil || n != 1 {
		t.Fatalf("abandoned index reclaim: %d %v", n, err)
	}
}

func TestIntegrationNativeCleanupCannotDropAnotherNamespaceIndex(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	first := integrationStore(t, StoreOptions{NativeIndexes: true})
	a := nativeLifecycleCollection(t, first)
	second := integrationStore(t, StoreOptions{NativeIndexes: true})
	b := nativeLifecycleCollection(t, second)
	if _, err := a.Insert(ctx, nativeLifecycleRows("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Insert(ctx, nativeLifecycleRows("second")); err != nil {
		t.Fatal(err)
	}
	ma, _ := currentManifest(t, a)
	mb, _ := currentManifest(t, b)
	ma.Index = mb.Index
	raw, err := json.Marshal(ma)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.client.Set(ctx, first.key(a.name), raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Count(ctx, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("foreign index pointer accepted: %v", err)
	}
	plain, err := New(StoreOptions{Addr: integrationAddr, Namespace: first.metadata.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	handle, err := plain.GetModelCollection(a.name, a.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handle.Upsert(ctx, nativeLifecycleRows("repaired")); err != nil {
		t.Fatal(err)
	}
	if _, err = first.PruneNativeIndexes(ctx, a.name); err != nil {
		t.Fatal(err)
	}
	if n, err := b.Count(ctx, vectordata.Eq(vectordata.Column("Value"), "second")); err != nil || n != 2 {
		t.Fatalf("foreign index was affected: %d %v", n, err)
	}
}

func TestIntegrationNativeSmoke(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64, AutoGenerated: true}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeString, IsIndexed: true}, {Name: "Rank", Type: vectordata.TypeInt64, IsIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2Squared, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}}}
	base, err := s.EnsureModelCollection(ctx, "native", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	if _, err = c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Group": "guide", "Rank": int64(1)}, Vectors: map[string][]float32{"Vector": {1, 0}}}, {Data: map[string]any{"Group": "guide", "Rank": int64(2)}, Vectors: map[string][]float32{"Vector": {0, 1}}}, {Data: map[string]any{"Group": "other", "Rank": int64(3)}, Vectors: map[string][]float32{"Vector": {3, 4}}}}); err != nil {
		t.Fatal(err)
	}
	native, full := 0, 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, nativeExecute) {
			native++
		}
		if scriptIs(cmd, readHash) {
			full++
		}
		return next(ctx, cmd)
	}})
	if n, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Group"), "guide")); err != nil || n != 2 {
		t.Fatalf("native count: %d %v", n, err)
	}
	if n, err := c.Count(ctx, vectordata.Gt(vectordata.Column("Rank"), int64(1))); err != nil || n != 2 {
		t.Fatalf("native range: %d %v", n, err)
	}
	rows, err := c.Query(ctx, vectordata.Eq(vectordata.Column("Group"), "guide"), 1, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Rank", Descending: true}}})
	if err != nil || len(rows) != 1 || rows[0].Key != int64(2) {
		t.Fatalf("native query: %+v %v", rows, err)
	}
	hits, err := c.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{IncludeVectors: true})
	if err != nil || len(hits) != 2 || hits[0].Record.Key != int64(1) || hits[1].Distance != 2 {
		t.Fatalf("native search: %+v %v", hits, err)
	}
	if native < 4 || full != 0 {
		t.Fatalf("native path not used: %d native, %d snapshots", native, full)
	}
	m, _ := currentManifest(t, c)
	names, err := s.client.Do(ctx, "FT._LIST").StringSlice()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, name := range names {
		if strings.HasPrefix(name, s.indexPrefix(c.name)) {
			found++
			if name != m.Index {
				t.Fatalf("retired index retained: %s", name)
			}
		}
	}
	if found != 1 {
		t.Fatalf("current native index count %d", found)
	}
}
