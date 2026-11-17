package exactsearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/stores/sqlitevec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func stores(t *testing.T) map[string]vectordata.ModelStore {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "search.db"), sqlite.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	projected, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "projected.db"), sqlite.StoreOptions{VectorProjections: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projected.Close() })
	native, err := sqlitevec.Open(context.Background(), filepath.Join(t.TempDir(), "native.db"), sqlitevec.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { native.Close() })
	return map[string]vectordata.ModelStore{"memory": memory.New(), "sqlite": s, "sqlite_projected": projected, "sqlitevec": native}
}
func denseValues(r vectordata.ModelRecord, p vectordata.VectorProperty) []float32 {
	if p.Kind != vectordata.VectorSparse {
		return r.Vectors[p.Name]
	}
	out := make([]float32, p.Dimensions)
	v := r.SparseVectors[p.Name]
	for i, index := range v.Indices {
		out[index] = v.Values[i]
	}
	return out
}

// Independent distance oracle: sparse coordinates are expanded only in these
// small fixtures; binary distance counts differing bits directly.
func oracleDistance(a, b []float32, metric vectordata.DistanceMetric) float64 {
	var dot, aa, bb, squared, l1 float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		delta := x - y
		squared += delta * delta
		l1 += math.Abs(delta)
		dot += x * y
		aa += x * x
		bb += y * y
	}
	switch metric {
	case vectordata.DistanceL2Squared:
		return squared
	case vectordata.DistanceL2:
		return math.Sqrt(squared)
	case vectordata.DistanceL1:
		return l1
	case vectordata.DistanceInnerProduct:
		return -dot
	default:
		return 1 - math.Max(-1, math.Min(1, dot/(math.Sqrt(aa)*math.Sqrt(bb))))
	}
}
func fullSort(ctx context.Context, c vectordata.ModelCollection, p vectordata.VectorProperty, query []float32, bits vectordata.BinaryVector, top int, o vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	rows, err := c.Query(ctx, o.Filter, int(^uint(0)>>1), vectordata.QueryOptions{IncludeVectors: true})
	if err != nil {
		return nil, err
	}
	all := []vectordata.ModelSearchResult{}
	for _, r := range rows {
		if !vectordata.HasModelVector(r, p) {
			continue
		}
		var distance float64
		if p.Kind == vectordata.VectorBinary {
			b := r.BinaryVectors[p.Name]
			for i := 0; i < bits.Dimensions; i++ {
				if (bits.Bytes[i/8]^b.Bytes[i/8])&(1<<uint(7-i%8)) != 0 {
					distance++
				}
			}
		} else {
			distance = oracleDistance(query, denseValues(r, p), p.Metric)
		}
		score := vectordata.ScoreFromDistance(p.Metric, distance)
		if o.DistanceThreshold != nil && distance > *o.DistanceThreshold || o.ScoreThreshold != nil && score < *o.ScoreThreshold {
			continue
		}
		if !o.IncludeVectors {
			r.Vectors = nil
			r.BinaryVectors = nil
			r.SparseVectors = nil
		}
		all = append(all, vectordata.ModelSearchResult{Record: r, Distance: distance, Score: score, ScoreKind: vectordata.ScoreSimilarity})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Distance < all[j].Distance })
	start := min(o.Skip, len(all))
	return all[start : start+min(top, len(all)-start)], nil
}
func querySearch(ctx context.Context, c vectordata.ModelCollection, p vectordata.VectorProperty, q []float32, b vectordata.BinaryVector, top int, o vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	switch p.Kind {
	case vectordata.VectorBinary:
		return c.(vectordata.BinarySearcher).SearchBinary(ctx, b, top, o)
	case vectordata.VectorSparse:
		var indices []int
		var values []float32
		for i, x := range q {
			if x != 0 {
				indices = append(indices, i)
				values = append(values, x)
			}
		}
		sparse, err := vectordata.NewSparseVector(len(q), indices, values)
		if err != nil {
			return nil, err
		}
		return c.(vectordata.SparseSearcher).SearchSparse(ctx, sparse, top, o)
	default:
		return c.Search(ctx, q, top, o)
	}
}
func TestProvidersMatchFullSort(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			index := 0
			for _, kind := range []vectordata.VectorKind{vectordata.VectorDense, vectordata.VectorBinary, vectordata.VectorSparse} {
				metrics := []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct, vectordata.DistanceL1}
				if kind == vectordata.VectorBinary {
					metrics = []vectordata.DistanceMetric{vectordata.DistanceHamming}
				}
				for _, metric := range metrics {
					precisions := []vectordata.VectorPrecision{vectordata.VectorFloat32}
					if kind == vectordata.VectorDense {
						precisions = append(precisions, vectordata.VectorFloat16)
					}
					if kind == vectordata.VectorBinary {
						precisions = []vectordata.VectorPrecision{""}
					}
					for _, precision := range precisions {
						t.Run(fmt.Sprintf("%s_%s_%s", kind, metric, precision), func(t *testing.T) {
							ctx := context.Background()
							p := vectordata.VectorProperty{Name: "V", Kind: kind, Dimensions: 4, Metric: metric, Precision: precision, Optional: true}
							d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeInt64}, {Name: "Tags", Type: vectordata.TypeStringArray}, {Name: "JSON", Type: vectordata.TypeJSON}}, Vectors: []vectordata.VectorProperty{p}}
							index++
							c, err := store.EnsureModelCollection(ctx, fmt.Sprintf("search_%d", index), d, vectordata.ModelCollectionOptions{})
							if err != nil {
								t.Fatal(err)
							}
							p = c.Definition().Vectors[0]
							rows := make([]vectordata.ModelRecord, 120)
							for i := range rows {
								rows[i] = vectordata.ModelRecord{Key: int64(9007199254740993 + len(rows) - i), Data: map[string]any{"Group": int64(i % 3), "Tags": []string{fmt.Sprint(i % 2)}, "JSON": map[string]any{"n": json.Number("9007199254740993")}}}
								if i%9 == 0 {
									continue
								}
								v := []float32{1, float32(i%7-3) / 7, float32(i%3) / 3, 0}
								switch kind {
								case vectordata.VectorBinary:
									b, _ := vectordata.NewBinaryVector([]byte{byte(i%16) << 4}, 4)
									rows[i].BinaryVectors = map[string]vectordata.BinaryVector{"V": b}
								case vectordata.VectorSparse:
									var indices []int
									var values []float32
									for j, x := range v {
										if x != 0 {
											indices = append(indices, j)
											values = append(values, x)
										}
									}
									s, _ := vectordata.NewSparseVector(4, indices, values)
									rows[i].SparseVectors = map[string]vectordata.SparseVector{"V": s}
								default:
									rows[i].Vectors = map[string][]float32{"V": v}
								}
							}
							if _, err := c.Insert(ctx, rows); err != nil {
								t.Fatal(err)
							}
							query := []float32{1, 0, -0.5, 0.25}
							if kind == vectordata.VectorDense {
								query, err = vectordata.NormalizeModelVector(query, p)
								if err != nil {
									t.Fatal(err)
								}
							}
							bits, _ := vectordata.ParseBinaryVector("1010")
							distance, score := 1.3, 0.25
							for _, tt := range []struct {
								top  int
								opts vectordata.ModelSearchOptions
							}{
								{7, vectordata.ModelSearchOptions{}}, {7, vectordata.ModelSearchOptions{Skip: 5, IncludeVectors: true}},
								{7, vectordata.ModelSearchOptions{Skip: 200}}, {int(^uint(0) >> 1), vectordata.ModelSearchOptions{}},
								{11, vectordata.ModelSearchOptions{Filter: vectordata.And(vectordata.Eq(vectordata.Column("Group"), 1), vectordata.ContainsAny(vectordata.Column("Tags"), "0", "1"), vectordata.Gt(vectordata.DataPath("JSON", "n"), json.Number("9007199254740992")))}},
								{10, vectordata.ModelSearchOptions{DistanceThreshold: &distance}}, {10, vectordata.ModelSearchOptions{ScoreThreshold: &score, Skip: 3}},
							} {
								want, err := fullSort(ctx, c, p, query, bits, tt.top, tt.opts)
								if err != nil {
									t.Fatal(err)
								}
								got, err := querySearch(ctx, c, p, query, bits, tt.top, tt.opts)
								if err != nil {
									t.Fatal(err)
								}
								if !reflect.DeepEqual(got, want) {
									t.Fatalf("top=%d options=%+v\ngot=%+v\nwant=%+v", tt.top, tt.opts, got, want)
								}
								if len(got) > 0 {
									got[0].Record.Data["Tags"].([]string)[0] = "changed"
									if tt.opts.IncludeVectors {
										r := got[0].Record
										switch kind {
										case vectordata.VectorBinary:
											r.BinaryVectors["V"].Bytes[0] ^= 0xff
										case vectordata.VectorSparse:
											r.SparseVectors["V"].Values[0] = 9
										default:
											r.Vectors["V"][0] = 9
										}
									}
									again, err := querySearch(ctx, c, p, query, bits, tt.top, tt.opts)
									if err != nil || !reflect.DeepEqual(again, want) {
										t.Fatalf("search results alias stored records: %v", err)
									}
								}
							}
						})
					}
				}
			}
		})
	}
}

func TestSelectorHugePagingAndErrors(t *testing.T) {
	d, _ := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 1, Metric: vectordata.DistanceL2}}}).Normalize()
	p := d.Vectors[0]
	source := func(yield func(vectordata.ModelRecord, error) bool) {
		for _, key := range []string{"b", "a"} {
			if !yield(vectordata.ModelRecord{Key: key, Vectors: map[string][]float32{"V": {1}}}, nil) {
				return
			}
		}
	}
	measure := func(vectordata.ModelRecord) (float64, error) { return 0, nil }
	maxInt := int(^uint(0) >> 1)
	got, err := exactsearch.Run(context.Background(), d, p, maxInt, vectordata.ModelSearchOptions{}, source, measure)
	if err != nil || len(got) != 2 || got[0].Record.Key != "a" {
		t.Fatalf("huge top allocated or tied incorrectly: %+v %v", got, err)
	}
	got, err = exactsearch.Run(context.Background(), d, p, 1, vectordata.ModelSearchOptions{Skip: maxInt - 1}, source, measure)
	if err != nil || len(got) != 0 {
		t.Fatalf("huge skip: %+v %v", got, err)
	}
	if _, err := exactsearch.Run(context.Background(), d, p, maxInt, vectordata.ModelSearchOptions{Skip: 1}, nil, measure); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
		t.Fatalf("overflow accepted: %v", err)
	}
	failure := errors.New("scan failure")
	broken := func(yield func(vectordata.ModelRecord, error) bool) {
		source(yield)
		yield(vectordata.ModelRecord{}, failure)
	}
	if rows, err := exactsearch.Run(context.Background(), d, p, 1, vectordata.ModelSearchOptions{}, broken, measure); rows != nil || !errors.Is(err, failure) {
		t.Fatalf("partial results escaped: %+v %v", rows, err)
	}
	if rows, err := exactsearch.Run(context.Background(), d, p, 1, vectordata.ModelSearchOptions{}, source, func(vectordata.ModelRecord) (float64, error) { return 0, failure }); rows != nil || !errors.Is(err, failure) {
		t.Fatalf("distance error lost: %+v %v", rows, err)
	}
	inflight, stop := context.WithCancel(context.Background())
	defer stop()
	if rows, err := exactsearch.Run(inflight, d, p, 1, vectordata.ModelSearchOptions{}, source, func(vectordata.ModelRecord) (float64, error) { stop(); return 0, nil }); rows != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation lost: %+v %v", rows, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := exactsearch.Run(canceled, d, p, 1, vectordata.ModelSearchOptions{}, nil, measure); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestMemorySearchSnapshotDuringBatchReplacement(t *testing.T) {
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Version", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 1, Metric: vectordata.DistanceL2}}}
	c, err := memory.New().EnsureModelCollection(ctx, "snapshot", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	batch := func(version int64) []vectordata.ModelRecord {
		records := make([]vectordata.ModelRecord, 50)
		for i := range records {
			records[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Version": version}, Vectors: map[string][]float32{"V": {float32(version)}}}
		}
		return records
	}
	if _, err := c.Insert(ctx, batch(0)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for version := int64(1); version <= 50; version++ {
			if _, err := c.Upsert(ctx, batch(version)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 100; i++ {
		hits, err := c.Search(ctx, []float32{0}, 50, vectordata.ModelSearchOptions{IncludeVectors: true})
		if err != nil || len(hits) != 50 {
			t.Fatalf("search: %d %v", len(hits), err)
		}
		version := hits[0].Record.Data["Version"].(int64)
		for _, hit := range hits {
			if hit.Record.Data["Version"] != version || hit.Distance != float64(version) || hit.Record.Vectors["V"][0] != float32(version) {
				t.Fatalf("search mixed atomic batches: first version %d, hit %+v", version, hit)
			}
		}
		// Returned data must remain independent of current and future snapshots.
		hits[0].Record.Data["Version"] = int64(-1)
		hits[0].Record.Vectors["V"][0] = -1
	}
}
