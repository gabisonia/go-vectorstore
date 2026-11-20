package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type rankedChunk struct {
	ID, Source string
	Score      float64
}

type scores struct{ Recall, MRR, NDCG float64 }
type queryResult struct {
	ID      string
	Sources []string
	Metrics scores
	// Keep exact chunk ranks and scores to compare the restored index.
	Chunks []rankedChunk
}
type quality struct {
	K, CandidateChunks int
	Mean               scores
	Queries            []queryResult
	Seconds            float64
}

// Unjudged sources receive zero gain. These are document-level, graded qrels.
func score(sources []string, relevant map[string]int, k int) scores {
	var result scores
	hits, dcg := 0, 0.0
	seen := map[string]bool{}
	for i, source := range sources[:min(k, len(sources))] {
		if seen[source] {
			continue
		}
		seen[source] = true
		grade := relevant[source]
		if grade > 0 {
			hits++
			if result.MRR == 0 {
				result.MRR = 1 / float64(i+1)
			}
			dcg += (math.Pow(2, float64(grade)) - 1) / math.Log2(float64(i+2))
		}
	}
	grades := make([]int, 0, len(relevant))
	for _, grade := range relevant {
		grades = append(grades, grade)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	idcg := 0.0
	for i, grade := range grades[:min(k, len(grades))] {
		idcg += (math.Pow(2, float64(grade)) - 1) / math.Log2(float64(i+2))
	}
	if len(relevant) > 0 {
		result.Recall = float64(hits) / float64(len(relevant))
	}
	if idcg > 0 {
		result.NDCG = dcg / idcg
	}
	return result
}

func (idx *index) evaluate(ctx context.Context, queries []query, vectors [][]float32, k, candidates int) (quality, error) {
	start := time.Now()
	result := quality{K: k, CandidateChunks: candidates}
	for i, q := range queries {
		hits, err := idx.chunks.Search(ctx, vectors[i], candidates, vectordata.ModelSearchOptions{})
		if err != nil {
			return result, err
		}
		r := queryResult{ID: q.ID, Sources: []string{}, Chunks: []rankedChunk{}}
		seen := map[string]bool{}
		for _, hit := range hits {
			r.Chunks = append(r.Chunks, rankedChunk{hit.Item.ID, hit.Item.Source, hit.Score})
			if !seen[hit.Item.Source] && len(r.Sources) < k {
				r.Sources = append(r.Sources, hit.Item.Source)
				seen[hit.Item.Source] = true
			}
		}
		r.Metrics = score(r.Sources, q.Relevant, k)
		result.Mean.Recall += r.Metrics.Recall / float64(len(queries))
		result.Mean.MRR += r.Metrics.MRR / float64(len(queries))
		result.Mean.NDCG += r.Metrics.NDCG / float64(len(queries))
		result.Queries = append(result.Queries, r)
	}
	result.Seconds = time.Since(start).Seconds()
	return result, nil
}

type loadResult struct {
	Concurrency, Requests, Failures                             int
	Seconds, RequestsPerSecond, P50Millis, P95Millis, MaxMillis float64
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[max(0, int(math.Ceil(float64(len(sorted))*p))-1)]
}

func (idx *index) load(ctx context.Context, vectors [][]float32, candidates, workers, perWorker int, timeout time.Duration) (loadResult, error) {
	type observation struct {
		ms  float64
		err error
	}
	observations := make(chan observation, workers*perWorker)
	var wg sync.WaitGroup
	start := time.Now()
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				callCtx, cancel := context.WithTimeout(ctx, timeout)
				t0 := time.Now()
				_, err := idx.chunks.Search(callCtx, vectors[(worker*perWorker+i)%len(vectors)], candidates, vectordata.ModelSearchOptions{})
				observations <- observation{float64(time.Since(t0).Microseconds()) / 1000, err}
				cancel()
				if ctx.Err() != nil {
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(observations)
	result := loadResult{Concurrency: workers, Seconds: time.Since(start).Seconds()}
	var latencies []float64
	var firstError error
	for o := range observations {
		result.Requests++
		if o.err != nil {
			result.Failures++
			if firstError == nil {
				firstError = o.err
			}
			continue
		}
		latencies = append(latencies, o.ms)
	}
	sort.Float64s(latencies)
	result.P50Millis, result.P95Millis, result.MaxMillis = percentile(latencies, .5), percentile(latencies, .95), percentile(latencies, 1)
	result.RequestsPerSecond = float64(len(latencies)) / result.Seconds
	if firstError != nil {
		return result, fmt.Errorf("%d load failures: %w", result.Failures, firstError)
	}
	return result, nil
}
