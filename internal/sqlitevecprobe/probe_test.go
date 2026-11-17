//go:build sqlitevecprobe

// Package sqlitevecprobe tests the pinned driver's extension independently of
// the provider. It deliberately exposes no production API.
package sqlitevecprobe

import (
	"context"
	"database/sql"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/vectordata"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, db *sql.DB, query string, args ...any) float64 {
	t.Helper()
	var result float64
	if err := db.QueryRow(query, args...).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDefaultProviderDoesNotLinkExtension(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "github.com/gabisonia/go-vectorstore/stores/sqlite")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("default provider dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if dependency == "modernc.org/sqlite/vec" {
			t.Fatal("default provider unexpectedly links sqlite-vec")
		}
	}
}

func TestBundledVersionAndMetrics(t *testing.T) {
	db := open(t, ":memory:")
	var sqliteVersion, vecVersion string
	if err := db.QueryRow(`SELECT sqlite_version(), vec_version()`).Scan(&sqliteVersion, &vecVersion); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s/%s %s: SQLite %s, sqlite-vec %s", runtime.GOOS, runtime.GOARCH, runtime.Version(), sqliteVersion, vecVersion)
	if vecVersion != "v0.1.9" {
		t.Fatalf("revisit the design/probe for bundled version %q", vecVersion)
	}
	for _, tc := range []struct {
		name, function string
		metric         vectordata.DistanceMetric
	}{
		{"L2", "vec_distance_L2", vectordata.DistanceL2},
		{"cosine", "vec_distance_cosine", vectordata.DistanceCosine},
		{"L1", "vec_distance_L1", vectordata.DistanceL1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scalar(t, db, `SELECT `+tc.function+`(vec_f32(?), vec_f32(?))`, `[1,2,3]`, `[4,6,3]`)
			want := exactsearch.DenseDistance([]float32{1, 2, 3}, []float32{4, 6, 3}, tc.metric)
			if math.Abs(got-want) > 1e-6 {
				t.Fatalf("distance %g, want %g", got, want)
			}
		})
	}
}

func TestPrecisionIsNotTheCanonicalDistanceContract(t *testing.T) {
	db := open(t, ":memory:")
	// Two distinct Go distances collapse to the same float32 extension result.
	// A key tie breaker cannot restore the true nearest neighbor after LIMIT.
	near := scalar(t, db, `SELECT vec_distance_L2('[0,0]', '[1,0]')`)
	far := scalar(t, db, `SELECT vec_distance_L2('[0,0]', '[1,0.0001]')`)
	goNear := exactsearch.DenseDistance([]float32{0, 0}, []float32{1, 0}, vectordata.DistanceL2)
	goFar := exactsearch.DenseDistance([]float32{0, 0}, []float32{1, 0.0001}, vectordata.DistanceL2)
	if near != far || goNear >= goFar {
		t.Fatalf("revisit precision gate: extension %g/%g, Go %.17g/%.17g", near, far, goNear, goFar)
	}
	t.Logf("extension %g/%g, Go %.17g/%.17g", near, far, goNear, goFar)
	execSQL(t, db, `CREATE TABLE points(id TEXT PRIMARY KEY, embedding TEXT NOT NULL)`)
	execSQL(t, db, `INSERT INTO points VALUES ('z','[1,0]'),('a','[1,0.0001]')`)
	var key string
	if err := db.QueryRow(`SELECT id FROM points ORDER BY vec_distance_L2(embedding,'[0,0]'),id LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "a" {
		t.Fatalf("revisit precision gate: native top-1 is %q, expected rounded tie to select a instead of nearer z", key)
	}
}

func TestFilterBeforeKAndStableTies(t *testing.T) {
	db := open(t, ":memory:")
	execSQL(t, db, `CREATE VIRTUAL TABLE vectors USING vec0(id TEXT PRIMARY KEY, embedding float[2], eligible integer)`)
	execSQL(t, db, `INSERT INTO vectors(id,embedding,eligible) VALUES ('excluded','[0,0]',0),('z','[1,0]',1),('a','[1,0]',1)`)
	var key string
	// Filtering inside vec0 must happen before k; filtering the returned top-1
	// would discard the excluded row and lose both eligible neighbors.
	if err := db.QueryRow(`WITH matches AS MATERIALIZED (SELECT id,distance FROM vectors WHERE embedding MATCH '[0,0]' AND k=2 AND eligible=1) SELECT id FROM matches ORDER BY distance,id LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "a" {
		t.Fatalf("got %q, want a", key)
	}
	// For the scalar path SQL filters and full distance/key ordering precede LIMIT.
	if err := db.QueryRow(`SELECT id FROM vectors WHERE eligible=1 ORDER BY vec_distance_L2(embedding,'[0,0]'),id LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "a" {
		t.Fatalf("got %q, want a", key)
	}
}

func TestOlderWriterAndTriggerCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.db")
	db := open(t, path)
	execSQL(t, db, `CREATE TABLE records(id INTEGER PRIMARY KEY, embedding TEXT NOT NULL)`)
	execSQL(t, db, `CREATE TABLE readiness(ready INTEGER NOT NULL)`)
	execSQL(t, db, `INSERT INTO readiness VALUES (1)`)
	execSQL(t, db, `CREATE VIRTUAL TABLE vectors USING vec0(embedding float[2])`)
	// Keep vec0 present to prove its schema does not prevent an unextended
	// process from reading/writing unrelated canonical tables.
	execSQL(t, db, `CREATE TRIGGER invalidate AFTER INSERT ON records BEGIN UPDATE readiness SET ready=0; END`)
	runPlainWriter(t, path, "success")
	if got := scalar(t, db, `SELECT ready FROM readiness`); got != 0 {
		t.Fatalf("ordinary trigger did not invalidate readiness: %g", got)
	}
	execSQL(t, db, `UPDATE readiness SET ready=1`)
	execSQL(t, db, `CREATE TRIGGER project AFTER INSERT ON records BEGIN INSERT INTO vectors(rowid,embedding) VALUES (new.id,vec_f32(new.embedding)); END`)
	runPlainWriter(t, path, "failure")
	if got := scalar(t, db, `SELECT count(*) FROM records`); got != 1 {
		t.Fatalf("failed projection did not roll back canonical write: %g", got)
	}
	if got := scalar(t, db, `SELECT ready FROM readiness`); got != 1 {
		t.Fatalf("failed projection did not roll back invalidation: %g", got)
	}
}

func runPlainWriter(t *testing.T, path, outcome string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// This is a separate binary whose import graph never includes sqlite/vec.
	// Another connection in this test process would inherit auto-registration.
	cmd := exec.CommandContext(ctx, "go", "run", "./testdata/plain-writer", path, outcome)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unextended writer: %v\n%s", err, output)
	}
}
