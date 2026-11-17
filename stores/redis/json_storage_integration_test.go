//go:build integration

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
	goredis "github.com/redis/go-redis/v9"
)

func requireRedisJSON(t *testing.T) {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: integrationAddr, Protocol: 2, MaxRetries: -1})
	defer client.Close()
	info, err := client.Do(context.Background(), "COMMAND", "INFO", "JSON.SET").Slice()
	if err != nil {
		t.Fatal(err)
	}
	if len(info) == 0 || info[0] == nil {
		if os.Getenv("REDIS_REQUIRE_JSON") == "true" {
			t.Fatal("RedisJSON is required")
		}
		t.Skip("RedisJSON module required")
	}
}

func TestIntegrationJSONModelContract(t *testing.T) {
	requireRedisJSON(t)
	conformance.Run(t, func(t *testing.T) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{StorageMode: StorageJSON})
	})
}
func TestIntegrationJSONEmbeddingConfiguration(t *testing.T) {
	requireRedisJSON(t)
	conformance.RunEmbeddingConfiguration(t, func(t *testing.T, o vectordata.ModelCollectionOptions) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{StorageMode: StorageJSON, Embedder: o.Embedder, InputEmbedder: o.InputEmbedder, EmbeddingBatchSize: o.EmbeddingBatchSize})
	})
}
func TestIntegrationJSONFilterParity(t *testing.T) {
	requireRedisJSON(t)
	conformance.RunFilterParity(t, func(t *testing.T) vectordata.ModelStore { return memory.New() }, func(t *testing.T) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{StorageMode: StorageJSON})
	})
}

func currentJSONManifest(t *testing.T, c *Collection) (jsonManifest, []byte) {
	t.Helper()
	raw, err := c.store.client.Get(context.Background(), c.store.key(c.name)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var m jsonManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Format != 3 {
		t.Fatalf("not a JSON manifest: %+v %v", m, err)
	}
	return m, raw
}
func jsonAuxiliaryKeys(t *testing.T, c *Collection) []string {
	t.Helper()
	var out []string
	var cursor uint64
	for {
		keys, next, err := c.store.client.Scan(context.Background(), cursor, c.store.jsonPrefix(c.name)+"*", 128).Result()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, keys...)
		cursor = next
		if cursor == 0 {
			return out
		}
	}
}
func assertPersistentJSON(t *testing.T, c *Collection) {
	t.Helper()
	m, _ := currentJSONManifest(t, c)
	for i, key := range c.jsonKeys(m)[1:] {
		ttl, err := c.store.client.PTTL(context.Background(), key).Result()
		if err != nil || ttl != -1 {
			t.Fatalf("published JSON key TTL: %s %v", ttl, err)
		}
		if i > 0 {
			kind, err := c.store.client.Do(context.Background(), "JSON.TYPE", key, ".").Result()
			if err != nil || kind != "object" && !reflect.DeepEqual(kind, []any{"object"}) {
				t.Fatalf("record is not a RedisJSON object: %+v %v", kind, err)
			}
		}
	}
}

func TestIntegrationJSONFixtureUpgradeAndReopen(t *testing.T) {
	requireRedisJSON(t)
	ctx := context.Background()
	for _, format := range []int{1, 2} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			s := integrationStore(t, StoreOptions{})
			c := fixtureCollection(t, s)
			if err := s.client.Set(ctx, s.key(c.name), fixtureBytes(t), 0).Err(); err != nil {
				t.Fatal(err)
			}
			if format == 2 {
				if err := c.MigrateToHashStorage(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.MigrateToJSONStorage(ctx); err != nil {
				t.Fatal(err)
			}
			m, before := currentJSONManifest(t, c)
			if m.Next != math.MaxInt64 || m.Definition != c.registration {
				t.Fatalf("conversion changed registration/sequence: %+v", m)
			}
			if err := c.MigrateToJSONStorage(ctx); err != nil {
				t.Fatal(err)
			}
			_, after := currentJSONManifest(t, c)
			if string(before) != string(after) {
				t.Fatal("repeated conversion wrote the manifest")
			}
			reopened, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			again := fixtureCollection(t, reopened)
			r, err := again.Get(ctx, int64(math.MinInt64), vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || r.Data["JSON"].(map[string]any)["number"] != json.Number("18446744073709551615") {
				t.Fatalf("RedisJSON rounded canonical JSON: %+v %v", r, err)
			}
			keys, err := again.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"JSON": vectordata.JSONNull{}, "Null": nil}}})
			if err != nil || !reflect.DeepEqual(keys, []any{int64(math.MaxInt64)}) {
				t.Fatalf("converted integer sequence: %v %v", keys, err)
			}
			if _, err := again.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"JSON": vectordata.JSONNull{}}}}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("sequence exhaustion: %v", err)
			}
			if err := c.MigrateToHashStorage(ctx); !errors.Is(err, vectordata.ErrUnsupported) {
				t.Fatalf("JSON-to-hash downgrade accepted: %v", err)
			}
			assertPersistentJSON(t, c)
			if len(auxiliaryKeys(t, s, c.name)) != 0 {
				t.Fatal("acknowledged conversion retained hash generation")
			}
		})
	}
}

func TestIntegrationJSONPrecisionBeyondRedisNumbers(t *testing.T) {
	requireRedisJSON(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageJSON, DB: 1})
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUint64}, Data: []vectordata.DataProperty{{Name: "JSON", Type: vectordata.TypeJSON}, {Name: "Decimal", Type: vectordata.TypeDecimal}, {Name: "Text", Type: vectordata.TypeString}}}
	base, err := s.EnsureModelCollection(ctx, "precision", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	input := vectordata.ModelRecord{Key: uint64(math.MaxUint64), Data: map[string]any{"JSON": map[string]any{"integer": json.Number("18446744073709551615"), "huge": json.Number("1e9999")}, "Decimal": "0.1000000000000000000000001", "Text": "nul\x00雪\"<>&"}}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{input}); err != nil {
		t.Fatal(err)
	}
	r, err := c.Get(ctx, input.Key, vectordata.RetrievalOptions{})
	if err != nil || r.Key != input.Key || r.Data["Text"] != input.Data["Text"] || !reflect.DeepEqual(r.Data["JSON"], input.Data["JSON"]) || r.Data["Decimal"] != vectordata.Decimal("0.1000000000000000000000001") {
		t.Fatalf("JSON precision round trip: %+v %v", r, err)
	}
	assertPersistentJSON(t, c)
}

func TestIntegrationJSONConcurrentWritesAndCorruption(t *testing.T) {
	requireRedisJSON(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageJSON})
	c := generatedCollection(t, s)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "concurrent"}}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n, err := c.Count(ctx, nil); err != nil || n != 12 {
		t.Fatalf("lost concurrent JSON writes: %d %v", n, err)
	}
	m, _ := currentJSONManifest(t, c)
	if err := s.client.Do(ctx, "JSON.DEL", c.jsonKeys(m)[2], "$.__vs_payload").Err(); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, err := range c.QueryIter(ctx, nil, 12, vectordata.QueryOptions{}) {
		seen++
		if !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("corrupt JSON generation: %v", err)
		}
	}
	if seen != 1 {
		t.Fatalf("partial JSON page escaped: %d", seen)
	}
	if n, err := s.PruneJSONStorage(ctx, c.name); err != nil || n != 0 {
		t.Fatalf("prune removed current JSON documents: %d %v", n, err)
	}
	if err := c.EnsureDeleted(ctx); err != nil || len(jsonAuxiliaryKeys(t, c)) != 0 {
		t.Fatalf("JSON deletion cleanup: %v", err)
	}
}

func TestIntegrationJSONModuleUnavailableDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageJSON})
	info, err := s.client.Do(ctx, "COMMAND", "INFO", "JSON.SET").Slice()
	if err != nil {
		t.Fatal(err)
	}
	if len(info) > 0 && info[0] != nil {
		t.Skip("requires standalone Redis without RedisJSON")
	}
	native := integrationStore(t, StoreOptions{StorageMode: StorageJSON, NativeIndexes: true})
	if _, err := native.EnsureModelCollection(ctx, "unsupported", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("missing JSON module in native mode: %v", err)
	}
	if n, err := native.client.Exists(ctx, native.key("unsupported")).Result(); err != nil || n != 0 {
		t.Fatalf("native missing-module creation published: %d %v", n, err)
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	if _, err := s.EnsureModelCollection(ctx, "unsupported", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) || !strings.Contains(strings.ToLower(err.Error()), "unknown command") {
		t.Fatalf("missing JSON module: %v", err)
	}
	if exists, err := s.CollectionExists(ctx, "unsupported"); err != nil || exists {
		t.Fatalf("empty JSON collection published without module: %t %v", exists, err)
	}
	for _, mode := range []StorageMode{StorageSnapshot, StorageHash} {
		plain := integrationStore(t, StoreOptions{StorageMode: mode})
		c := generatedCollection(t, plain)
		before, err := plain.client.Get(ctx, plain.key(c.name)).Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.MigrateToJSONStorage(ctx); !errors.Is(err, vectordata.ErrUnsupported) {
			t.Fatalf("missing module conversion: %v", err)
		}
		after, err := plain.client.Get(ctx, plain.key(c.name)).Bytes()
		if err != nil || string(before) != string(after) || len(jsonAuxiliaryKeys(t, c)) != 0 {
			t.Fatal("missing module conversion changed source/staged keys")
		}
	}
}

func TestIntegrationJSONStagingAndPublicationFailures(t *testing.T) {
	requireRedisJSON(t)
	for _, phase := range []string{"partial_pipeline", "lost_stage_reply", "expired_record", "lost_publication_reply", "persist_error"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			s := integrationStore(t, StoreOptions{StorageMode: StorageJSON})
			c := generatedCollection(t, s)
			_, before := currentJSONManifest(t, c)
			calls := 0
			hook := hashFailureHook{}
			if phase == "partial_pipeline" || phase == "lost_stage_reply" {
				hook.pipeline = func(ctx context.Context, cmds []goredis.Cmder, next goredis.ProcessPipelineHook) error {
					if len(cmds) > 0 && scriptIs(cmds[0], stageJSON) {
						calls++
						if phase == "partial_pipeline" && calls == 2 {
							return context.DeadlineExceeded
						}
						if phase == "lost_stage_reply" {
							if err := next(ctx, cmds); err != nil {
								return err
							}
							return context.DeadlineExceeded
						}
					}
					return next(ctx, cmds)
				}
			} else {
				hook.process = func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
					if !scriptIs(cmd, publishJSON) {
						return next(ctx, cmd)
					}
					calls++
					switch phase {
					case "expired_record":
						key := cmd.Args()[5].(string)
						if !strings.HasPrefix(key, s.jsonPrefix(c.name)) {
							return errors.New("fault key escaped JSON namespace")
						}
						if err := s.client.Del(ctx, key).Err(); err != nil {
							return err
						}
					case "persist_error":
						// A real Redis error after PERSIST, before the only publishing
						// SET, leaves non-expiring staging but no logical change.
						cmd.Args()[1] = strings.Replace(publishJSON, "redis.call('SET',KEYS[1],ARGV[3])", "redis.call('HSET',KEYS[1],'fault','value')", 1)
					}
					err := next(ctx, cmd)
					if phase == "lost_publication_reply" && err == nil {
						return context.DeadlineExceeded
					}
					return err
				}
			}
			s.client.AddHook(hook)
			rows := []vectordata.ModelRecord{{Data: map[string]any{"Value": "once"}}}
			expectedCalls := 1
			if phase == "partial_pipeline" {
				rows = make([]vectordata.ModelRecord, hashBatchSize+1)
				for i := range rows {
					rows[i] = vectordata.ModelRecord{Data: map[string]any{"Value": "staged"}}
				}
				expectedCalls = 2
			}
			keys, err := c.Insert(ctx, rows)
			if keys != nil || err == nil || calls != expectedCalls {
				t.Fatalf("failed JSON write replayed: %v %v calls=%d", keys, err, calls)
			}
			switch phase {
			case "expired_record":
				if !errors.Is(err, vectordata.ErrInvalidRecord) {
					t.Fatalf("expired staging: %v", err)
				}
			case "persist_error":
				var native goredis.Error
				if !errors.As(err, &native) || !strings.Contains(err.Error(), "WRONGTYPE") {
					t.Fatalf("native publication error lost: %v", err)
				}
			default:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline cause lost: %v", err)
				}
			}
			m, after := currentJSONManifest(t, c)
			want := int64(0)
			if phase == "lost_publication_reply" {
				want = 1
				if m.Next != 2 || string(before) == string(after) {
					t.Fatal("lost reply did not preserve committed generation")
				}
			} else if string(before) != string(after) {
				t.Fatal("failed JSON staging/publication changed manifest/sequence")
			}
			if count, err := c.Count(ctx, nil); err != nil || count != want {
				t.Fatalf("uncertain JSON state: %d %v", count, err)
			}
			removed, err := s.PruneJSONStorage(ctx, c.name)
			if err != nil || (phase == "lost_publication_reply" || phase == "persist_error") && removed == 0 {
				t.Fatalf("orphan pruning: %d %v", removed, err)
			}
			assertPersistentJSON(t, c)
		})
	}
}

func TestIntegrationJSONReadRetirement(t *testing.T) {
	requireRedisJSON(t)
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			ctx := context.Background()
			s := integrationStore(t, StoreOptions{StorageMode: StorageJSON})
			c := generatedCollection(t, s)
			rows := func(value string) []vectordata.ModelRecord {
				return []vectordata.ModelRecord{{Key: int64(1), Data: map[string]any{"Value": value}}, {Key: int64(2), Data: map[string]any{"Value": value}}}
			}
			if _, err := c.Insert(ctx, rows("before")); err != nil {
				t.Fatal(err)
			}
			other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			writer, err := other.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
				if !scriptIs(cmd, readJSON) || changed {
					return next(ctx, cmd)
				}
				changed = true
				write := func() error { _, err := writer.Upsert(ctx, rows("after")); return err }
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
			page, err := c.Query(ctx, nil, 2, vectordata.QueryOptions{})
			want := when
			if when == "before" {
				want = "after"
			} else {
				want = "before"
			}
			if err != nil || len(page) != 2 || page[0].Data["Value"] != want || page[1].Data["Value"] != want || !changed {
				t.Fatalf("mixed/retired JSON read: %+v %v", page, err)
			}
			assertPersistentJSON(t, c)
		})
	}
}

func TestIntegrationJSONMigrationConflictAndLimits(t *testing.T) {
	requireRedisJSON(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{})
	c := generatedCollection(t, s)
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": strings.Repeat("x", 1000)}}}); err != nil {
		t.Fatal(err)
	}
	before, err := s.client.Get(ctx, s.key(c.name)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	s.maxBytes = len(before) + 16
	if err := c.MigrateToJSONStorage(ctx); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("JSON projection limit: %v", err)
	}
	after, err := s.client.Get(ctx, s.key(c.name)).Bytes()
	if err != nil || string(before) != string(after) || len(jsonAuxiliaryKeys(t, c)) != 0 {
		t.Fatal("failed JSON conversion changed/staged data")
	}
	s.maxBytes = 16 << 20
	other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writer, err := other.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishJSON) {
			attempts++
			if attempts == 1 {
				if _, err := writer.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "legacy"}}}); err != nil {
					return err
				}
			}
		}
		return next(ctx, cmd)
	}})
	if err := c.MigrateToJSONStorage(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ := currentJSONManifest(t, c)
	if attempts != 2 || m.Next != 3 || len(m.Tokens) != 2 {
		t.Fatalf("conversion lost concurrent records/sequence: %+v attempts=%d", m, attempts)
	}
	if count, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Value"), "legacy")); err != nil || count != 1 {
		t.Fatalf("concurrent record lost: %d %v", count, err)
	}
	assertPersistentJSON(t, c)
}

func TestIntegrationJSONCancellationAndPruning(t *testing.T) {
	requireRedisJSON(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageJSON})
	c := generatedCollection(t, s)
	m, before := currentJSONManifest(t, c)
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishJSON) {
			cancel()
		}
		return next(ctx, cmd)
	}})
	if keys, err := c.Insert(canceled, []vectordata.ModelRecord{{Data: map[string]any{"Value": "canceled"}}}); keys != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled JSON publication: %v %v", keys, err)
	}
	_, after := currentJSONManifest(t, c)
	if string(before) != string(after) {
		t.Fatal("canceled JSON publication changed source")
	}
	active := jsonAuxiliaryKeys(t, c)
	if len(active) < 3 {
		t.Fatal("test did not stage JSON documents")
	}
	if count, err := s.PruneJSONStorage(ctx, c.name); err != nil || count != 0 {
		t.Fatalf("active staging removed: %d %v", count, err)
	}
	for _, key := range active {
		if !strings.HasPrefix(key, c.jsonGenerationPrefix(m.Revision)) {
			if err := s.client.Persist(ctx, key).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count, err := s.PruneJSONStorage(ctx, c.name); err != nil || count < 2 {
		t.Fatalf("abandoned persisted JSON staging not reclaimed: %d %v", count, err)
	}
	assertPersistentJSON(t, c)
}

func TestIntegrationJSONMigrationLostReplyAndNativeFallback(t *testing.T) {
	requireRedisJSON(t)
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	if _, err := c.Insert(ctx, nativeLifecycleRows("retained")); err != nil {
		t.Fatal(err)
	}
	plain, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	base, err := plain.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	plain.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		err := next(ctx, cmd)
		if scriptIs(cmd, publishJSON) {
			calls++
			if err == nil {
				return context.DeadlineExceeded
			}
		}
		return err
	}})
	if err := base.(*Collection).MigrateToJSONStorage(ctx); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("uncertain conversion replayed: %v calls=%d", err, calls)
	}
	assertPersistentJSON(t, c)
	if hits, err := c.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{}); err != nil || len(hits) != 1 || hits[0].Record.Key != int64(1) {
		t.Fatalf("native client did not fall back on JSON: %+v %v", hits, err)
	}
	if count, err := s.PruneHashStorage(ctx, c.name); err != nil || count < 3 {
		t.Fatalf("obsolete hash keys after conversion: %d %v", count, err)
	}
	if count, err := s.PruneNativeIndexes(ctx, c.name); err != nil || count != 1 {
		t.Fatalf("obsolete hash index after conversion: %d %v", count, err)
	}
	if count, err := s.PruneJSONStorage(ctx, c.name); err != nil || count != 0 {
		t.Fatalf("current JSON generation was pruned: %d %v", count, err)
	}
	if err := c.RebuildNativeIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ := currentJSONManifest(t, c)
	if m.IndexVersion != 4 || m.Index == "" {
		t.Fatal("explicit rebuild did not index JSON")
	}
}
