//go:build integration

package redis

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
	goredis "github.com/redis/go-redis/v9"
)

func TestIntegrationHashModelContract(t *testing.T) {
	conformance.Run(t, func(t *testing.T) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{StorageMode: StorageHash})
	})
}
func TestIntegrationHashEmbeddingConfiguration(t *testing.T) {
	conformance.RunEmbeddingConfiguration(t, func(t *testing.T, o vectordata.ModelCollectionOptions) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{StorageMode: StorageHash, Embedder: o.Embedder, InputEmbedder: o.InputEmbedder, EmbeddingBatchSize: o.EmbeddingBatchSize})
	})
}
func TestIntegrationHashFilterParity(t *testing.T) {
	conformance.RunFilterParity(t, func(t *testing.T) vectordata.ModelStore { return memory.New() }, func(t *testing.T) vectordata.ModelStore {
		return integrationStore(t, StoreOptions{StorageMode: StorageHash})
	})
}

type hashFailureHook struct {
	process  func(context.Context, goredis.Cmder, goredis.ProcessHook) error
	pipeline func(context.Context, []goredis.Cmder, goredis.ProcessPipelineHook) error
}

func (h hashFailureHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }
func (h hashFailureHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	if h.process == nil {
		return next
	}
	return func(ctx context.Context, cmd goredis.Cmder) error { return h.process(ctx, cmd, next) }
}
func (h hashFailureHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	if h.pipeline == nil {
		return next
	}
	return func(ctx context.Context, cmds []goredis.Cmder) error { return h.pipeline(ctx, cmds, next) }
}
func scriptIs(cmd goredis.Cmder, script string) bool {
	return cmd.Name() == "eval" && len(cmd.Args()) > 1 && cmd.Args()[1] == script
}

func currentManifest(t *testing.T, c *Collection) (hashManifest, []byte) {
	t.Helper()
	raw, err := c.store.client.Get(context.Background(), c.store.key(c.name)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var m hashManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m, raw
}
func auxiliaryKeys(t *testing.T, s *Store, name string) []string {
	t.Helper()
	var out []string
	var cursor uint64
	for {
		keys, next, err := s.client.Scan(context.Background(), cursor, s.hashPrefix(name)+"*", 128).Result()
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
func assertPersistentGeneration(t *testing.T, c *Collection) {
	t.Helper()
	m, _ := currentManifest(t, c)
	if m.Format != 2 {
		t.Fatalf("not format 2: %+v", m)
	}
	for i, key := range c.hashKeys(m)[1:] {
		ttl, err := c.store.client.PTTL(context.Background(), key).Result()
		if err != nil || ttl != -1 {
			t.Fatalf("published key %s TTL %s/%v", key, ttl, err)
		}
		if i > 0 {
			kind, err := c.store.client.Type(context.Background(), key).Result()
			if err != nil || kind != "hash" {
				t.Fatalf("record not a hash: %s %v", kind, err)
			}
		}
	}
}

func TestIntegrationHashUpgradeImmutableFixtureAndReopen(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	c := fixtureCollection(t, s)
	if err := s.client.Set(ctx, s.key(c.name), fixtureBytes(t), 0).Err(); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(ctx, nil, 2, vectordata.QueryOptions{IncludeVectors: true})
	if err != nil || len(rows) != 1 || rows[0].Key != int64(math.MinInt64) {
		t.Fatalf("old snapshot read: %+v %v", rows, err)
	}
	if _, err = c.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if state, _, err := c.load(ctx); err != nil || state.Format != 1 {
		t.Fatalf("store option silently migrated old data: %+v %v", state, err)
	}
	if err = c.MigrateToHashStorage(ctx); err != nil {
		t.Fatal(err)
	}
	assertPersistentGeneration(t, c)
	m, before := currentManifest(t, c)
	if m.Next != math.MaxInt64 {
		t.Fatal("migration narrowed sequence")
	}
	if err = c.MigrateToHashStorage(ctx); err != nil {
		t.Fatal(err)
	}
	_, after := currentManifest(t, c)
	if string(before) != string(after) {
		t.Fatal("idempotent upgrade rewrote storage")
	}
	// Default-mode clients infer existing format 2 and preserve it on writes.
	reopened, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again := fixtureCollection(t, reopened)
	got, err := again.Get(ctx, int64(math.MinInt64), vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || got.Data["JSON"].(map[string]any)["number"] != json.Number("18446744073709551615") || got.Data["Null"] != nil || len(got.Vectors["Vector"]) != 2 {
		t.Fatalf("migrated precision: %+v %v", got, err)
	}
	keys, err := again.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"JSON": vectordata.JSONNull{}, "Null": nil}}})
	if err != nil || keys[0] != int64(math.MaxInt64) {
		t.Fatalf("migrated sequence: %v %v", keys, err)
	}
	if _, err = again.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"JSON": vectordata.JSONNull{}}}}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("sequence exhaustion: %v", err)
	}
	assertPersistentGeneration(t, again)
}

func TestIntegrationHashFailedStagingAndUncertainPublication(t *testing.T) {
	for _, phase := range []string{"staging", "publication"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
			c := generatedCollection(t, s)
			_, before := currentManifest(t, c)
			calls := 0
			hook := hashFailureHook{}
			if phase == "staging" {
				hook.pipeline = func(ctx context.Context, cmds []goredis.Cmder, next goredis.ProcessPipelineHook) error {
					err := next(ctx, cmds)
					if len(cmds) > 0 && scriptIs(cmds[0], stageHash) && err == nil {
						calls++
						return context.DeadlineExceeded
					}
					return err
				}
			} else {
				hook.process = func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
					err := next(ctx, cmd)
					if scriptIs(cmd, publishHash) {
						calls++
						if err == nil {
							return context.DeadlineExceeded
						}
					}
					return err
				}
			}
			s.client.AddHook(hook)
			keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "once"}}})
			if !errors.Is(err, context.DeadlineExceeded) || keys != nil || calls != 1 {
				t.Fatalf("uncertain operation replayed: %v %v %d", keys, err, calls)
			}
			state, _, err := c.load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "staging" {
				_, after := currentManifest(t, c)
				if string(after) != string(before) || len(state.Records) != 0 || state.Next != 1 {
					t.Fatal("staging failure published data")
				}
			} else {
				if len(state.Records) != 1 || state.Next != 2 {
					t.Fatalf("lost successful reply: %+v", state)
				}
			}
			if _, err = s.PruneHashStorage(ctx, c.name); err != nil {
				t.Fatal(err)
			}
			assertPersistentGeneration(t, c)
			m, _ := currentManifest(t, c)
			for _, key := range auxiliaryKeys(t, s, c.name) {
				if !strings.HasPrefix(key, c.generationPrefix(m.Revision)) {
					t.Fatalf("obsolete key retained after pruning: %s", key)
				}
			}
		})
	}
}

func TestIntegrationHashPublishErrorAfterPersistPreservesManifest(t *testing.T) {
	ctx := context.Background()
	admin := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	c := generatedCollection(t, admin)
	_, before := currentManifest(t, c)
	id, err := vectordata.NewUUIDKey()
	if err != nil {
		t.Fatal(err)
	}
	user := "hash_test_" + id
	if err = admin.client.Do(ctx, "ACL", "SETUSER", user, "on", ">test-only-password", "~*", "+@all").Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.client.Do(ctx, "ACL", "DELUSER", user).Err(); err != nil {
			t.Error(err)
		}
	})
	s, err := New(StoreOptions{Addr: integrationAddr, Namespace: admin.metadata.Namespace, Username: user, Password: "test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base, err := s.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishHash) {
			denied = true
			if err := admin.client.Do(ctx, "ACL", "SETUSER", user, "-set").Err(); err != nil {
				return err
			}
		}
		return next(ctx, cmd)
	}})
	if keys, err := base.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "not published"}}}); err == nil || keys != nil || !denied {
		t.Fatalf("expected publication permission failure: %v %v", keys, err)
	}
	_, after := currentManifest(t, c)
	if string(after) != string(before) {
		t.Fatal("publication error changed manifest")
	}
	orphans := 0
	m, _ := currentManifest(t, c)
	for _, key := range auxiliaryKeys(t, admin, c.name) {
		if !strings.HasPrefix(key, c.generationPrefix(m.Revision)) {
			ttl, err := admin.client.PTTL(ctx, key).Result()
			if err != nil {
				t.Fatal(err)
			}
			if ttl == -1 {
				orphans++
			}
		}
	}
	if orphans == 0 {
		t.Fatal("test did not reach PERSIST before the failing SET")
	}
	if _, err := admin.PruneHashStorage(ctx, c.name); err != nil {
		t.Fatal(err)
	}
	assertPersistentGeneration(t, c)
}

func TestIntegrationHashConflictDoesNotRepeatEmbedding(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	calls := 0
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64, AutoGenerated: true}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, SourceProperty: "Body"}}}
	base, err := s.EnsureModelCollection(ctx, "embedding", d, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(ctx context.Context, input []string) ([][]float32, error) {
		calls++
		if _, err := s.ListCollectionNames(ctx); err != nil {
			return nil, err
		}
		return [][]float32{{1, 0}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writer, err := other.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conflicted := false
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishHash) && !conflicted {
			conflicted = true
			if _, err := writer.Upsert(ctx, []vectordata.ModelRecord{{Key: int64(50), Data: map[string]any{"Body": "writer"}, Vectors: map[string][]float32{"Vector": {0, 1}}}}); err != nil {
				return err
			}
		}
		return next(ctx, cmd)
	}})
	keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Body": "embed once"}}})
	if err != nil || len(keys) != 1 || keys[0] != int64(1) || calls != 1 || !conflicted {
		t.Fatalf("conflict: %v %v calls %d", keys, err, calls)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 2 {
		t.Fatalf("lost writer: %d %v", n, err)
	}
	assertPersistentGeneration(t, c)
}

func TestIntegrationHashReaderRetriesRetiredGeneration(t *testing.T) {
	for _, when := range []string{"before_read", "after_read"} {
		t.Run(when, func(t *testing.T) {
			ctx := context.Background()
			s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
			c := generatedCollection(t, s)
			rows := func(value string) []vectordata.ModelRecord {
				return []vectordata.ModelRecord{{Key: int64(1), Data: map[string]any{"Value": value}}, {Key: int64(2), Data: map[string]any{"Value": value}}}
			}
			if _, err := c.Upsert(ctx, rows("before")); err != nil {
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
				if !scriptIs(cmd, readHash) || changed {
					return next(ctx, cmd)
				}
				changed = true
				write := func() error { _, err := writer.Upsert(ctx, rows("after")); return err }
				if when == "before_read" {
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
			want := "after"
			if when == "after_read" {
				want = "before"
			}
			if err != nil || len(page) != 2 || page[0].Data["Value"] != want || page[1].Data["Value"] != want || !changed {
				t.Fatalf("mixed/retired snapshot: %+v %v", page, err)
			}
		})
	}
}

func TestIntegrationHashConcurrentWritesAndCorruption(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
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
		t.Fatalf("lost concurrent writes: %d %v", n, err)
	}
	m, _ := currentManifest(t, c)
	keys := c.hashKeys(m)
	if err := s.client.HDel(ctx, keys[2], "__vs_payload").Err(); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, err := range c.QueryIter(ctx, nil, 12, vectordata.QueryOptions{}) {
		seen++
		if !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("damaged generation: %v", err)
		}
	}
	if seen != 1 {
		t.Fatalf("partial page escaped: %d", seen)
	}
	if _, err := s.PruneHashStorage(ctx, c.name); err != nil {
		t.Fatal(err)
	}
	if exists, err := s.client.Exists(ctx, keys[1:]...).Result(); err != nil || exists != int64(len(keys)-1) {
		t.Fatal("prune removed current generation")
	}
	if err := c.EnsureDeleted(ctx); err != nil {
		t.Fatal(err)
	}
	if remaining := auxiliaryKeys(t, s, c.name); len(remaining) != 0 {
		t.Fatalf("delete retained committed hashes: %v", remaining)
	}
}

func TestIntegrationHashCancellationLimitsAndPruning(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	c := generatedCollection(t, s)
	_, before := currentManifest(t, c)
	s.maxBytes = 1024
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": strings.Repeat("x", 2048)}}}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("size bound: %v", err)
	}
	s.maxBytes = 16 << 20
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishHash) {
			cancel()
		}
		return next(ctx, cmd)
	}})
	if keys, err := c.Insert(canceled, []vectordata.ModelRecord{{Data: map[string]any{"Value": "canceled"}}}); keys != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publish: %v %v", keys, err)
	}
	_, after := currentManifest(t, c)
	if string(after) != string(before) {
		t.Fatal("canceled staging published data")
	}
	active := auxiliaryKeys(t, s, c.name)
	if len(active) < 3 {
		t.Fatal("test did not leave expiring staging")
	}
	if _, err := s.PruneHashStorage(ctx, c.name); err != nil {
		t.Fatal(err)
	}
	if len(auxiliaryKeys(t, s, c.name)) != len(active) {
		t.Fatal("prune removed active TTL staging")
	}
	// Simulate expired/failed staging after TTLs were removed but no manifest
	// committed; current keys must survive reclamation, including a recreated name.
	m, _ := currentManifest(t, c)
	for _, key := range active {
		if !strings.HasPrefix(key, c.generationPrefix(m.Revision)) {
			if err := s.client.Persist(ctx, key).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n, err := s.PruneHashStorage(ctx, c.name); err != nil || n < 2 {
		t.Fatalf("orphan reclamation: %d %v", n, err)
	}
	assertPersistentGeneration(t, c)
	if err := c.EnsureDeleted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureExists(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneHashStorage(ctx, c.name); err != nil {
		t.Fatal(err)
	}
	assertPersistentGeneration(t, c)
}

func TestIntegrationHashFieldsWorkWithRedisSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// RESP2 gives this protocol-level verification stable array replies without
	// depending on the client's evolving typed Redis Search result structs.
	client := goredis.NewClient(&goredis.Options{Addr: integrationAddr, Protocol: 2, MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { client.Close() })
	info, err := client.Do(ctx, "COMMAND", "INFO", "FT.CREATE").Slice()
	if err != nil {
		t.Fatal(err)
	}
	if len(info) == 0 || info[0] == nil {
		if os.Getenv("REDIS_REQUIRE_SEARCH") == "true" {
			t.Fatal("Redis Search was required for the projection proof")
		}
		t.Skip("Redis Search is absent (expected on standalone Redis 7.4)")
	}
	s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Category", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}
	base, err := s.EnsureModelCollection(ctx, "index_proof", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	if _, err = c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Category": "guide"}, Vectors: map[string][]float32{"Vector": {1, 0}}}, {Key: "b", Data: map[string]any{"Category": "other"}, Vectors: map[string][]float32{"Vector": {0, 1}}}}); err != nil {
		t.Fatal(err)
	}
	m, _ := currentManifest(t, c)
	index := "proof_" + m.Revision
	vectorField := "v_" + encodeName("Vector")
	dataField := "d_" + encodeName("Category")
	if err = client.Do(ctx, "FT.CREATE", index, "ON", "HASH", "PREFIX", 1, c.generationPrefix(m.Revision), "SCHEMA", dataField, "TAG", vectorField, "VECTOR", "FLAT", 6, "TYPE", "FLOAT32", "DIM", 2, "DISTANCE_METRIC", "L2").Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Do(context.Background(), "FT.DROPINDEX", index).Err(); err != nil {
			t.Error(err)
		}
	})
	query := make([]byte, 8)
	binary.LittleEndian.PutUint32(query, math.Float32bits(1))
	for {
		result, err := client.Do(ctx, "FT.SEARCH", index, "@"+dataField+":{guide}=>[KNN 1 @"+vectorField+" $q AS distance]", "PARAMS", 2, "q", query, "SORTBY", "distance", "RETURN", 2, "__vs_token", "distance", "DIALECT", 2).Slice()
		if err != nil {
			t.Fatal(err)
		}
		if len(result) > 0 && result[0] == int64(1) {
			if len(result) != 3 || result[1] != c.hashKeys(m)[2] {
				t.Fatalf("indexed key: %+v", result)
			}
			fields, ok := result[2].([]any)
			if !ok || len(fields) != 4 {
				t.Fatalf("indexed fields: %+v", result)
			}
			got := map[string]any{}
			for i := 0; i < len(fields); i += 2 {
				got[fields[i].(string)] = fields[i+1]
			}
			if got["__vs_token"] != token("a") || got["distance"] != "0" {
				t.Fatalf("indexed values: %+v", got)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Redis Search did not index the staged hash generation")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestIntegrationHashPartialPipelineNeverPublishes(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	c := generatedCollection(t, s)
	_, before := currentManifest(t, c)
	batches := 0
	s.client.AddHook(hashFailureHook{pipeline: func(ctx context.Context, cmds []goredis.Cmder, next goredis.ProcessPipelineHook) error {
		if len(cmds) > 0 && scriptIs(cmds[0], stageHash) {
			batches++
			if batches == 2 {
				return context.DeadlineExceeded
			}
		}
		return next(ctx, cmds)
	}})
	rows := make([]vectordata.ModelRecord, hashBatchSize+1)
	for i := range rows {
		rows[i] = vectordata.ModelRecord{Data: map[string]any{"Value": "staged"}}
	}
	if keys, err := c.Insert(ctx, rows); keys != nil || !errors.Is(err, context.DeadlineExceeded) || batches != 2 {
		t.Fatalf("partial staging: %v %v batches %d", keys, err, batches)
	}
	_, after := currentManifest(t, c)
	if string(after) != string(before) {
		t.Fatal("partially staged batch was published")
	}
	m, _ := currentManifest(t, c)
	if keys := auxiliaryKeys(t, s, c.name); len(keys) != 1 || keys[0] != c.hashKeys(m)[1] {
		t.Fatalf("failed staging was not reclaimed: %v", keys)
	}
}

func TestIntegrationHashExpiredStagingCannotPublish(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{StorageMode: StorageHash})
	c := generatedCollection(t, s)
	_, before := currentManifest(t, c)
	// Redis expires one staged record before the publication script begins.
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishHash) {
			key := cmd.Args()[5].(string)
			if !strings.HasPrefix(key, s.hashPrefix(c.name)) {
				return errors.New("staging fault key escaped the test namespace")
			}
			if err := s.client.Del(ctx, key).Err(); err != nil {
				return err
			}
		}
		return next(ctx, cmd)
	}})
	if keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "expired"}}}); keys != nil || !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("incomplete staging published: %v %v", keys, err)
	}
	_, after := currentManifest(t, c)
	if string(after) != string(before) {
		t.Fatal("missing staged record advanced manifest/sequence")
	}
	// The source collection remains readable; remove only the test's unpublished
	// expiring lease after all publication work has ended.
	m, _ := currentManifest(t, c)
	for _, key := range auxiliaryKeys(t, s, c.name) {
		if !strings.HasPrefix(key, c.generationPrefix(m.Revision)) {
			if err := s.client.Unlink(ctx, key).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertPersistentGeneration(t, c)
}

func TestIntegrationHashUpgradeRetriesConcurrentLegacyWrite(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{})
	c := generatedCollection(t, s)
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "before"}}}); err != nil {
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
		if scriptIs(cmd, publishHash) && !changed {
			changed = true
			if _, err := writer.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "legacy write"}}}); err != nil {
				return err
			}
		}
		return next(ctx, cmd)
	}})
	if err := c.MigrateToHashStorage(ctx); err != nil {
		t.Fatal(err)
	}
	state, _, err := c.load(ctx)
	if err != nil || state.Format != 2 || state.Next != 3 || len(state.Records) != 2 || !changed {
		t.Fatalf("upgrade lost concurrent write/sequence: %+v %v", state, err)
	}
	if n, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Value"), "legacy write")); err != nil || n != 1 {
		t.Fatalf("missing legacy record: %d %v", n, err)
	}
	assertPersistentGeneration(t, c)
}

func TestIntegrationHashUpgradeProjectionLimitHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{})
	c := generatedCollection(t, s)
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": strings.Repeat("x", 1000)}}}); err != nil {
		t.Fatal(err)
	}
	_, before := currentManifest(t, c)
	s.maxBytes = len(before) + 16
	if err := c.MigrateToHashStorage(ctx); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("projection size limit: %v", err)
	}
	_, after := currentManifest(t, c)
	if string(after) != string(before) {
		t.Fatal("failed size-limited upgrade rewrote legacy storage")
	}
	if keys := auxiliaryKeys(t, s, c.name); len(keys) != 0 {
		t.Fatalf("upgrade staged before checking full byte budget: %v", keys)
	}
}
