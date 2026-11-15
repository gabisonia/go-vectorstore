package exactsearch_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// referenceSearch retains the former Query-all, score-all, stable-sort strategy.
// Both paths measure identical distances; the full-sort oracle tests separately
// verify distance/ranking correctness independently of this benchmark.
func referenceSearch(ctx context.Context, c vectordata.ModelCollection, p vectordata.VectorProperty, measure exactsearch.Measure, top, skip int) ([]vectordata.ModelSearchResult, error) {
	records, err := c.Query(ctx, nil, int(^uint(0)>>1), vectordata.QueryOptions{IncludeVectors: true})
	if err != nil {
		return nil, err
	}
	hits := []vectordata.ModelSearchResult{}
	for _, r := range records {
		d, err := measure(r)
		if err != nil {
			return nil, err
		}
		r.Vectors = nil
		r.BinaryVectors = nil
		r.SparseVectors = nil
		hits = append(hits, vectordata.ModelSearchResult{Record: r, Distance: d, Score: vectordata.ScoreFromDistance(p.Metric, d), ScoreKind: vectordata.ScoreSimilarity})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Distance < hits[j].Distance })
	start := min(skip, len(hits))
	return hits[start : start+min(top, len(hits)-start)], nil
}

func BenchmarkExactSearch(b *testing.B) {
	for _, backend := range []string{"memory", "sqlite"} {
		b.Run(backend, func(b *testing.B) {
			for _, size := range []int{100, 10000} {
				b.Run(fmt.Sprintf("records%d", size), func(b *testing.B) {
					ctx := context.Background()
					var store vectordata.ModelStore
					if backend == "memory" {
						store = memory.New()
					} else {
						s, err := sqlite.Open(ctx, filepath.Join(b.TempDir(), "search.db"), sqlite.StoreOptions{})
						if err != nil {
							b.Fatal(err)
						}
						b.Cleanup(func() { s.Close() })
						store = s
					}
					d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{
						{Name: "Dense", Dimensions: 128, Metric: vectordata.DistanceL2},
						{Name: "Half", Dimensions: 128, Precision: vectordata.VectorFloat16, Metric: vectordata.DistanceL2},
						{Name: "Binary", Dimensions: 128, Kind: vectordata.VectorBinary},
						{Name: "Sparse", Dimensions: 128, Kind: vectordata.VectorSparse, Metric: vectordata.DistanceL2},
					}}
					c, err := store.EnsureModelCollection(ctx, "bench", d, vectordata.ModelCollectionOptions{})
					if err != nil {
						b.Fatal(err)
					}
					records := make([]vectordata.ModelRecord, size)
					for i := range records {
						v := make([]float32, 128)
						for j := range v {
							v[j] = float32((i+j)%7) / 7
						}
						bytes := make([]byte, 16)
						for j := range bytes {
							if (i+j)%3 == 0 {
								bytes[j] = 255
							}
						}
						bits, _ := vectordata.NewBinaryVector(bytes, 128)
						sparse, _ := vectordata.NewSparseVector(128, []int{0, 64, 127}, []float32{1, float32(i%7+1) / 7, float32(i%3+1) / 3})
						records[i] = vectordata.ModelRecord{Key: fmt.Sprintf("key-%06d", i), Data: map[string]any{"Title": "sample document"}, Vectors: map[string][]float32{"Dense": v, "Half": v}, BinaryVectors: map[string]vectordata.BinaryVector{"Binary": bits}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": sparse}}
					}
					if _, err := c.Insert(ctx, records); err != nil {
						b.Fatal(err)
					}
					query := make([]float32, 128)
					query[0] = 1
					bits, _ := vectordata.NewBinaryVector(make([]byte, 16), 128)
					sparse, _ := vectordata.NewSparseVector(128, []int{0}, []float32{1})
					for _, p := range c.Definition().Vectors {
						b.Run(p.Name, func(b *testing.B) {
							var measure exactsearch.Measure
							switch p.Kind {
							case vectordata.VectorBinary:
								measure = func(r vectordata.ModelRecord) (float64, error) {
									d, err := vectordata.HammingDistance(bits, r.BinaryVectors[p.Name])
									return float64(d), err
								}
							case vectordata.VectorSparse:
								measure = func(r vectordata.ModelRecord) (float64, error) {
									return vectordata.SparseDistance(sparse, r.SparseVectors[p.Name], p.Metric)
								}
							default:
								measure = func(r vectordata.ModelRecord) (float64, error) {
									return exactsearch.DenseDistance(query, r.Vectors[p.Name], p.Metric), nil
								}
							}
							opts := vectordata.ModelSearchOptions{VectorProperty: p.Name, Skip: 7}
							for _, strategy := range []string{"topk", "full_sort"} {
								b.Run(strategy, func(b *testing.B) {
									b.ReportAllocs()
									for b.Loop() {
										var hits []vectordata.ModelSearchResult
										var err error
										if strategy == "full_sort" {
											hits, err = referenceSearch(ctx, c, p, measure, 10, 7)
										} else {
											switch p.Kind {
											case vectordata.VectorBinary:
												hits, err = c.(vectordata.BinarySearcher).SearchBinary(ctx, bits, 10, opts)
											case vectordata.VectorSparse:
												hits, err = c.(vectordata.SparseSearcher).SearchSparse(ctx, sparse, 10, opts)
											default:
												hits, err = c.Search(ctx, query, 10, opts)
											}
										}
										if err != nil || len(hits) != 10 {
											b.Fatalf("search: %d %v", len(hits), err)
										}
									}
								})
							}
						})
					}
				})
			}
		})
	}
}
