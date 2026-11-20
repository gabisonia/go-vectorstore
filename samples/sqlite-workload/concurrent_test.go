package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	modernsqlite "modernc.org/sqlite"
)

type callWindow struct {
	start, end time.Time
	err        error
}
type workloadStats struct {
	Count, LockErrors, Cancellations, UnexpectedErrors int
	P50us, P95us, CallsPerSecond                       float64
}

func summarizeCalls(calls []callWindow, elapsed time.Duration) workloadStats {
	s := workloadStats{Count: len(calls), CallsPerSecond: float64(len(calls)) / elapsed.Seconds()}
	if len(calls) == 0 {
		return s
	}
	durations := make([]time.Duration, 0, len(calls))
	for _, call := range calls {
		durations = append(durations, call.end.Sub(call.start))
		var dbErr *modernsqlite.Error
		switch {
		case call.err == nil:
		case errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded):
			s.Cancellations++
		case errors.As(call.err, &dbErr) && (dbErr.Code()&255 == 5 || dbErr.Code()&255 == 6):
			s.LockErrors++
		default:
			s.UnexpectedErrors++
		}
	}
	slices.Sort(durations)
	percentile := func(p int) float64 { return float64(durations[(len(calls)*p+99)/100-1]) / float64(time.Microsecond) }
	s.P50us, s.P95us = percentile(50), percentile(95)
	return s
}

func TestConcurrentWAL(t *testing.T) {
	n, dim, rounds := 64, 8, 8
	switch os.Getenv("SQLITE_WAL_PROFILE") {
	case "":
	case "measured":
		n, dim, rounds = 1024, 32, 100
	case "large":
		n, dim, rounds = 10000, 128, 200
	default:
		t.Fatal("unknown SQLITE_WAL_PROFILE")
	}
	for _, mode := range []string{"canonical", "packed_go"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/writes=%t", mode, mixed), func(t *testing.T) {
				ctx := t.Context()
				db := reader{mode: mode, path: filepath.Join(t.TempDir(), "fixture.db"), d: definition(dim)}
				writer := db
				if err := db.open(ctx, true); err != nil {
					t.Fatal(err)
				}
				defer db.close()
				if err := writer.open(ctx, false); err != nil {
					t.Fatal(err)
				}
				defer writer.close()
				const mix = 4
				var query [mix][]float32
				var refs [mix]oracle
				for i := range query {
					query[i] = fixtureVector(int64(-1-i), dim, 0, "random")
					refs[i].limit = 10
				}
				makeRow := func(k int, v []float32, body string) vectordata.ModelRecord {
					return vectordata.ModelRecord{Key: int64(k), Data: map[string]any{"Body": body, "Group": int64(k % 100)}, Vectors: map[string][]float32{"V": v}}
				}
				updates := make([]vectordata.ModelRecord, rounds)
				for start := 0; start < n+rounds; start += 128 {
					var batch []vectordata.ModelRecord
					for k := start; k < min(start+128, n+rounds); k++ {
						v := fixtureVector(int64(k), dim, 0, "random")
						if k < n {
							for i := range query {
								refs[i].add(int64(k), referenceDistance(v, query[i]))
							}
						} else {
							for j := range v {
								v[j] = 10
							}
							updated := slices.Clone(v)
							updated[0] = 20
							updates[k-n] = makeRow(k, updated, "updated")
						}
						batch = append(batch, makeRow(k, v, "seed"))
					}
					if _, err := db.collection.Insert(ctx, batch); err != nil {
						t.Fatal(err)
					}
				}
				var searches, writes []callWindow
				overlap := 0
				started := time.Now()
				for round := range rounds {
					i := round % mix
					start := make(chan struct{})
					result := make(chan callWindow, 1)
					if mixed {
						go func() {
							<-start
							w := callWindow{start: time.Now()}
							_, w.err = writer.collection.Upsert(ctx, updates[round:round+1])
							w.end = time.Now()
							result <- w
						}()
					}
					close(start)
					q := callWindow{start: time.Now()}
					hits, err := db.collection.Search(ctx, query[i], 10, vectordata.ModelSearchOptions{})
					q.end, q.err = time.Now(), err
					searches = append(searches, q)
					if mixed {
						w := <-result
						writes = append(writes, w)
						if w.start.Before(q.end) && q.start.Before(w.end) {
							overlap++
						}
					}
					if err == nil {
						if _, err := validatePage(hits, refs[i].hits, "seed"); err != nil {
							t.Fatal(err)
						}
					}
				}
				elapsed := time.Since(started)
				search, write := summarizeCalls(searches, elapsed), summarizeCalls(writes, elapsed)
				report := map[string]any{"layout": mode, "mixed": mixed, "records": n + rounds, "dimensions": dim, "overlapping_pairs": overlap, "elapsed_ms": float64(elapsed) / float64(time.Millisecond), "search": search, "write": write}
				raw, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("WAL_RESULT %s", raw)
				for _, calls := range [][]callWindow{searches, writes} {
					for _, c := range calls {
						if c.err != nil {
							t.Fatal(c.err)
						}
					}
				}
				if mixed {
					for _, row := range updates {
						got, err := db.collection.Get(ctx, row.Key, vectordata.RetrievalOptions{IncludeVectors: true})
						if err != nil || got.Data["Body"] != "updated" || got.Data["Group"] != row.Data["Group"] || !slices.Equal(got.Vectors["V"], row.Vectors["V"]) {
							t.Fatalf("write verification for %v: %v", row.Key, err)
						}
					}
				}
			})
		}
	}
}
