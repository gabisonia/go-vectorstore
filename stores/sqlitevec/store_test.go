package sqlitevec_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/stores/sqlitevec"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
)

func open(t *testing.T, path string) *sqlitevec.Store {
	t.Helper()
	s, err := sqlitevec.Open(context.Background(), path, sqlitevec.StoreOptions{NativeIndexes: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestModelContract(t *testing.T) {
	conformance.Run(t, func(t *testing.T) vectordata.ModelStore {
		return open(t, filepath.Join(t.TempDir(), "native.db"))
	})
}

func TestEmbeddingConfiguration(t *testing.T) {
	conformance.RunEmbeddingConfiguration(t, func(t *testing.T, opts vectordata.ModelCollectionOptions) vectordata.ModelStore {
		s, err := sqlitevec.Open(t.Context(), filepath.Join(t.TempDir(), "outputs.db"), sqlitevec.StoreOptions{InputEmbedder: opts.InputEmbedder, Embedder: opts.Embedder, EmbeddingBatchSize: opts.EmbeddingBatchSize})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

// Raw scalar calls consume host-order blobs. Persisted provider projections use
// little-endian storage; production native execution is gated to little-endian.
func nativePacked(vector []float32) []byte {
	result := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.NativeEndian.PutUint32(result[4*i:], math.Float32bits(value))
	}
	return result
}

func TestL1ScalarMatchesCanonicalBits(t *testing.T) {
	// Source-reviewed scalar L1 uses float64 throughout. Exercise full exponent
	// range, signs, subnormals, maximum finite inputs, and long reductions.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rng := rand.New(rand.NewPCG(7, 19))
	for _, dimensions := range []int{1, 2, 3, 127, 128, 769, 4097} {
		for iteration := range 16 {
			a, b := make([]float32, dimensions), make([]float32, dimensions)
			for i := range a {
				a[i] = finiteFloat(rng.Uint32())
				b[i] = finiteFloat(rng.Uint32())
			}
			if iteration == 0 {
				a[0] = math.MaxFloat32
				b[0] = -math.MaxFloat32
			}
			if iteration == 1 {
				a[0] = math.SmallestNonzeroFloat32
				b[0] = 0
			}
			var got float64
			if err := db.QueryRow(`SELECT vec_distance_L1(?,?)`, nativePacked(a), nativePacked(b)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			want := exactsearch.DenseDistance(a, b, vectordata.DistanceL1)
			if math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("dimensions=%d iteration=%d native=%.17g canonical=%.17g", dimensions, iteration, got, want)
			}
		}
	}
}

func finiteFloat(bits uint32) float32 {
	if bits&0x7f800000 == 0x7f800000 {
		bits ^= 0x00800000
	}
	return math.Float32frombits(bits)
}

func TestNativeL1BoundariesAndMixedWriters(t *testing.T) {
	for _, keyCase := range []struct {
		kind vectordata.KeyType
		keys []any
	}{
		{vectordata.KeyInt64, []any{int64(-9007199254740993), int64(2), int64(11), int64(9007199254740993)}},
		{vectordata.KeyUint64, []any{uint64(2), uint64(11), uint64(9007199254740993), uint64(math.MaxUint64)}},
		{vectordata.KeyString, []any{"a", "literal'\"雪", "y", "z"}},
		{vectordata.KeyUUID, []any{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003", "00000000-0000-4000-8000-000000000004"}},
	} {
		t.Run(string(keyCase.kind), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "ties.db")
			s := open(t, path)
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: keyCase.kind}, Data: []vectordata.DataProperty{{Name: "Version", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "L1'雪", StorageName: "other_name", Dimensions: 2, Metric: vectordata.DistanceL1, Optional: true}, {Name: "L2", Dimensions: 2, Metric: vectordata.DistanceL2, Optional: true}}}
			c, err := s.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			rows := make([]vectordata.ModelRecord, len(keyCase.keys))
			for i, key := range keyCase.keys {
				rows[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Version": "before"}, Vectors: map[string][]float32{"L1'雪": {1, 0}, "L2": {1, 0}}}
			}
			// The first key is slightly farther, but a float32 L1 result would tie.
			// Use a double-visible difference that float32 accumulation loses.
			rows[0].Vectors["L1'雪"][1] = 1e-8
			rows[len(rows)-1].Vectors = nil
			if _, err := c.Insert(ctx, rows); err != nil {
				t.Fatal(err)
			}
			plain, err := sqlite.Open(ctx, path, sqlite.StoreOptions{VectorProjections: true})
			if err != nil {
				t.Fatal(err)
			}
			defer plain.Close()
			other, err := plain.GetModelCollection("records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			distance, score := 1.0, 0.5
			for _, opts := range []vectordata.ModelSearchOptions{
				{VectorProperty: "L1'雪"},
				{VectorProperty: "L1'雪", Skip: 1, IncludeVectors: true},
				{VectorProperty: "L1'雪", Skip: 10},
				{VectorProperty: "L1'雪", DistanceThreshold: &distance},
				{VectorProperty: "L1'雪", ScoreThreshold: &score, Skip: 1},
				{VectorProperty: "L1'雪", Filter: vectordata.Eq(vectordata.Column("ID"), keyCase.keys[0])},
				{VectorProperty: "L2"},
			} {
				want, err := other.Search(ctx, []float32{0, 0}, 1, opts)
				if err != nil {
					t.Fatal(err)
				}
				got, err := c.Search(ctx, []float32{0, 0}, 1, opts)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("options=%+v\ngot=%+v\nwant=%+v err=%v", opts, got, want, err)
				}
			}
			// An unaware writer invalidates the projection; no native query may use
			// its old distance or rebuild implicitly.
			unaware, err := sqlite.Open(ctx, path, sqlite.StoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer unaware.Close()
			writer, err := unaware.GetModelCollection("records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			rows[0].Vectors["L1'雪"] = []float32{0, 0}
			rows[0].Data["Version"] = "after"
			if _, err := writer.Upsert(ctx, rows[:1]); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: "L1'雪"})
			if err != nil || len(hits) != 1 || hits[0].Distance != 0 || hits[0].Record.Data["Version"] != "after" {
				t.Fatalf("dirty fallback: %+v %v", hits, err)
			}
			if err := c.(*sqlite.Collection).RebuildVectorProjections(ctx); err != nil {
				t.Fatal(err)
			}
			again, err := c.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: "L1'雪"})
			if err != nil || !reflect.DeepEqual(again, hits) {
				t.Fatalf("explicit rebuild: %+v %v", again, err)
			}
		})
	}
}

func TestOpenValidationAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.db")
	if _, err := sqlitevec.Open(context.Background(), path, sqlitevec.StoreOptions{VectorExecution: "unknown"}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("mode validation: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sqlitevec.Open(ctx, path, sqlitevec.StoreOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("open cancellation: %v", err)
	}
	s := open(t, path)
	if s.Capabilities().Backend != "sqlite" || len(s.Capabilities().VectorIndexMethods) != 0 {
		t.Fatalf("native index inferred from extension: %+v", s.Capabilities())
	}
}

func TestOpenLockWaitIsCancelable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "locked.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A legacy rollback-journal file held exclusively blocks connection setup's
	// WAL pragma, before extension verification can run. Keep normal retry and
	// cancellation semantics instead of reporting an unavailable extension.
	if _, err := conn.ExecContext(ctx, `PRAGMA journal_mode=DELETE; CREATE TABLE blocker(x); BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if store, err := sqlitevec.Open(deadline, path, sqlitevec.StoreOptions{}); store != nil || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, vectordata.ErrUnsupported) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("blocked native open: store=%v error=%v", store, err)
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	open(t, path)
}

func TestLiteralFilenamePersistence(t *testing.T) {
	// These characters are valid in both Windows and POSIX filenames. In
	// particular # and % must not become a URI fragment or escape sequence.
	path := filepath.Join(t.TempDir(), "literal#%&_pragma=foreign_keys(0) 雪.db")
	ctx := context.Background()
	s, err := sqlitevec.Open(ctx, path, sqlitevec.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 1, Metric: vectordata.DistanceL1}}}
	c, err := s.EnsureModelCollection(ctx, "literal", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "kept", Vectors: map[string][]float32{"V": {1}}}}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("literal filename was not used: %v", err)
	}
	reopened := open(t, path)
	c, err = reopened.GetModelCollection("literal", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := c.Search(ctx, []float32{0}, 1, vectordata.ModelSearchOptions{})
	if err != nil || len(hits) != 1 || hits[0].Record.Key != "kept" || hits[0].Distance != 1 {
		t.Fatalf("literal file reopen: %+v %v", hits, err)
	}
}
