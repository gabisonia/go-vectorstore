//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const mixedWorkers = 4
const mixedTop = 8
const mixedSeed = 94

// Integer coordinates keep all squared distances exactly representable for
// these profiles. Tied rows may be returned in either order by PostgreSQL.
func mixedDistance(a, b []float32) float64 {
	var sum float64
	for i, x := range a {
		d := float64(x - b[i])
		sum += d * d
	}
	return math.Sqrt(sum)
}

type mixedQuery struct {
	vector    []float32
	distances []float64
}

func mixedFixture(count, dimensions int) ([]vectordata.ModelRecord, []mixedQuery) {
	rng := rand.New(rand.NewSource(mixedSeed))
	vector := func() []float32 {
		v := make([]float32, dimensions)
		for i := range v {
			v[i] = float32(rng.Intn(127))
		}
		return v
	}
	rows := make([]vectordata.ModelRecord, count)
	for i := range rows {
		rows[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Eligible": i%4 == 0, "Revision": int64(0)}, Vectors: map[string][]float32{"V": vector()}}
	}
	queries := make([]mixedQuery, 16)
	for i := range queries {
		q := mixedQuery{vector: vector()}
		for j := 0; j < count; j += 4 {
			q.distances = append(q.distances, mixedDistance(q.vector, rows[j].Vectors["V"]))
		}
		slices.Sort(q.distances)
		q.distances = q.distances[:mixedTop]
		queries[i] = q
	}
	return rows, queries
}

func (q mixedQuery) check(hits []vectordata.ModelSearchResult, rows []vectordata.ModelRecord) error {
	if len(hits) != mixedTop {
		return fmt.Errorf("got %d hits, want %d", len(hits), mixedTop)
	}
	seen := map[int64]bool{}
	for i, h := range hits {
		key, ok := h.Record.Key.(int64)
		if !ok || key < 0 || key >= int64(len(rows)) || key%4 != 0 || seen[key] || h.Record.Data["Eligible"] != true {
			return fmt.Errorf("invalid hit at rank %d: %v", i, h.Record.Key)
		}
		seen[key] = true
		distance := mixedDistance(q.vector, rows[key].Vectors["V"])
		if distance != q.distances[i] || math.IsNaN(h.Distance) || math.Abs(h.Distance-distance) > 1e-5 {
			return fmt.Errorf("rank %d: key %d reports %g, reference %g, want %g", i, key, h.Distance, distance, q.distances[i])
		}
	}
	return nil
}

type mixedWorker struct {
	latency [3][]time.Duration
	err     error
	writes  int64
}

func (w *mixedWorker) run(ctx context.Context, c vectordata.ModelCollection, rows []vectordata.ModelRecord, queries []mixedQuery, id int, end time.Time) {
	// Each writer owns one ineligible key. Reads and search reference rows remain immutable.
	write := rows[4*id+1]
	write.Data = map[string]any{"Eligible": false, "Revision": int64(0)}
	opts := vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("Eligible"), true)}
	for cycle := 0; cycle == 0 || time.Now().Before(end); cycle++ {
		for n := range 2 {
			key := int64(4 * ((2*cycle + id + n) % (len(rows) / 4)))
			start := time.Now()
			got, err := c.Get(ctx, key, vectordata.RetrievalOptions{})
			w.latency[0] = append(w.latency[0], time.Since(start))
			if err != nil {
				w.err = err
				return
			}
			if got.Key != key || got.Data["Eligible"] != true || got.Data["Revision"] != int64(0) {
				w.err = fmt.Errorf("unexpected point read for %d: %+v", key, got)
				return
			}
		}
		q := queries[(cycle+id)%len(queries)]
		start := time.Now()
		hits, err := c.Search(ctx, q.vector, mixedTop, opts)
		w.latency[1] = append(w.latency[1], time.Since(start))
		if err != nil {
			w.err = err
			return
		}
		if err = q.check(hits, rows); err != nil {
			w.err = err
			return
		}
		write.Data["Revision"] = w.writes + 1
		batch := []vectordata.ModelRecord{write}
		start = time.Now()
		keys, err := c.Upsert(ctx, batch)
		w.latency[2] = append(w.latency[2], time.Since(start))
		if err != nil {
			w.err = err
			return
		}
		if len(keys) != 1 || keys[0] != write.Key {
			w.err = fmt.Errorf("unexpected write keys: %v", keys)
			return
		}
		w.writes++
	}
}

// Hold the target row on another connection so cancellation occurs after the
// server starts the upsert. Other workers use different keys in the same table.
func checkMixedCancellation(ctx context.Context, c *ModelCollection, row vectordata.ModelRecord) error {
	pool := c.store.pool
	locker, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		return err
	}
	defer locker.Close(ctx)
	tx, err := locker.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	if _, err = tx.Exec(ctx, "SELECT 1 FROM "+c.table()+` WHERE "ID"=$1 FOR UPDATE`, row.Key); err != nil {
		return err
	}
	cfg := pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer single.Close()
	store, err := NewVectorStore(single, c.store.opts)
	if err != nil {
		return err
	}
	probe, err := store.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	var pid uint32
	if err = single.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		return err
	}
	row.Data = map[string]any{"Eligible": false, "Revision": int64(-1)}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := probe.Upsert(op, []vectordata.ModelRecord{row}); done <- err }()
	blocked := waitForBlockedBackend(op, pool, pid)
	cancel()
	outcome := <-done
	if blocked != nil {
		return blocked
	}
	if !errors.Is(outcome, context.Canceled) {
		return fmt.Errorf("blocked upsert returned %v", outcome)
	}
	if err = tx.Rollback(ctx); err != nil {
		return err
	}
	got, err := probe.Get(ctx, row.Key, vectordata.RetrievalOptions{})
	if err == nil && got.Data["Revision"] != int64(0) {
		return fmt.Errorf("canceled upsert changed %v", row.Key)
	}
	return err
}

type mixedOpStats struct {
	Completed int     `json:"completed"`
	PerSecond float64 `json:"ops_per_second"`
	P50Micros float64 `json:"p50_us"`
	P95Micros float64 `json:"p95_us"`
}

func TestIntegrationMixedVectorWorkload(t *testing.T) {
	count, dim, duration := 128, 16, 250*time.Millisecond
	measured := os.Getenv("PGVECTOR_MIXED_MEASURE") == "1"
	if measured {
		count, dim, duration = 4096, 128, 5*time.Second
	}
	ctx := t.Context()
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	def := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Eligible", Type: vectordata.TypeBool}, {Name: "Revision", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: dim, Metric: vectordata.DistanceL2}}}
	base, err := store.EnsureModelCollection(ctx, "mixed", def, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*ModelCollection)
	rows, queries := mixedFixture(count, dim)
	if _, err = c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "ANALYZE "+c.table()); err != nil {
		t.Fatal(err)
	}
	var workers [mixedWorkers]mixedWorker
	var wg sync.WaitGroup
	start := time.Now()
	end := start.Add(duration)
	for id := range workers {
		wg.Go(func() { workers[id].run(ctx, c, rows, queries, id, end) })
	}
	cancelErr := checkMixedCancellation(ctx, c, rows[count-1])
	wg.Wait()
	elapsed := time.Since(start)
	var mem runtime.MemStats
	if measured {
		runtime.ReadMemStats(&mem)
	}
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	for id, w := range workers {
		if w.err != nil {
			t.Fatal(w.err)
		}
		got, err := c.Get(ctx, rows[4*id+1].Key, vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || got.Data["Revision"] != w.writes || !slices.Equal(got.Vectors["V"], rows[4*id+1].Vectors["V"]) {
			t.Fatalf("worker %d final write mismatch (revision %d): %v", id, w.writes, err)
		}
	}
	ops := map[string]mixedOpStats{}
	for op, name := range []string{"get", "search", "upsert"} {
		var samples []time.Duration
		for _, w := range workers {
			samples = append(samples, w.latency[op]...)
		}
		slices.Sort(samples)
		micros := func(p int) float64 { return float64(samples[(len(samples)*p+99)/100-1]) / float64(time.Microsecond) }
		ops[name] = mixedOpStats{len(samples), float64(len(samples)) / elapsed.Seconds(), micros(50), micros(95)}
	}
	if !measured {
		return
	}
	var pg, extension string
	if err = pool.QueryRow(ctx, "SELECT current_setting('server_version'), extversion FROM pg_extension WHERE extname='vector'").Scan(&pg, &extension); err != nil {
		t.Fatal(err)
	}
	sha, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"revision": strings.TrimSpace(string(sha)), "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "logical_cpus": runtime.NumCPU(), "postgres": pg, "pgvector": extension, "records": count, "dimensions": dim, "seed": mixedSeed, "workers": mixedWorkers, "top": mixedTop, "duration_seconds": elapsed.Seconds(), "operations": ops, "errors": 0, "expected_cancellations": 1, "client_heap_alloc_bytes": mem.HeapAlloc}
	if integrationContainer != nil {
		var bytes int64
		if err = pool.QueryRow(ctx, "SELECT pg_read_file('/sys/fs/cgroup/memory.current')::bigint").Scan(&bytes); err != nil {
			t.Fatal(err)
		}
		report["database_cgroup_bytes"] = bytes
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MIXED_RESULT %s", encoded)
}
