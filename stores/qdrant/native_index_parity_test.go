//go:build qdrantparity

package qdrant

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"
)

// TestNativeIndexedRecall compares filtered Qdrant queries with an independent
// exhaustive cosine ranking on a deterministic point set.
func TestNativeIndexedRecall(t *testing.T) {
	const count = 8192
	const dims = 32
	const k = 10
	const path = "/collections/parity_indexed"
	s, err := New(StoreOptions{URL: os.Getenv("PARITY_HTTP_URL")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := func(method, endpoint string, body, out any) {
		t.Helper()
		if _, err := s.request(t.Context(), method, path+endpoint, body, out); err != nil {
			t.Fatal(err)
		}
	}
	req("PUT", "", map[string]any{"vectors": map[string]any{"direction": map[string]any{"size": dims, "distance": "Cosine"}}, "optimizers_config": map[string]int{"indexing_threshold": 1, "default_segment_number": 1}, "hnsw_config": map[string]int{"full_scan_threshold": 10}}, nil)
	req("PUT", "/index?wait=true", map[string]string{"field_name": "category", "field_schema": "keyword"}, nil)
	rng := rand.New(rand.NewSource(16))
	draw := func() []float32 {
		x := make([]float32, dims)
		var norm float64
		for i := range x {
			x[i] = float32(rng.NormFloat64())
			norm += float64(x[i]) * float64(x[i])
		}
		for i := range x {
			x[i] = float32(float64(x[i]) / math.Sqrt(norm))
		}
		return x
	}
	corpus := make([][]float32, count)
	for start := 0; start < count; start += 128 {
		var points []any
		for id := start; id < start+128; id++ {
			corpus[id] = draw()
			points = append(points, map[string]any{"id": id, "vector": map[string][]float32{"direction": corpus[id]}, "payload": map[string]string{"category": fmt.Sprint(id % 2)}})
		}
		req("PUT", "/points?wait=true", map[string]any{"points": points}, nil)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		var info response[struct {
			Indexed int `json:"indexed_vectors_count"`
		}]
		req("GET", "", nil, &info)
		if info.Result.Indexed >= count*9/10 {
			t.Logf("indexed %d/%d vectors", info.Result.Indexed, count)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("indexed only %d/%d vectors", info.Result.Indexed, count)
		}
		time.Sleep(time.Second)
	}
	collection, err := s.AttachNativeCosine(t.Context(), "parity_indexed", "direction", dims)
	if err != nil {
		t.Fatal(err)
	}
	var found int
	const queries = 32
	for range queries {
		query := draw()
		type candidate struct {
			id    uint64
			score float64
		}
		exact := make([]candidate, 0, count/2)
		for i := 0; i < count; i += 2 {
			var score float64
			for j, v := range query {
				score += float64(v) * float64(corpus[i][j])
			}
			exact = append(exact, candidate{uint64(i), score})
		}
		sort.Slice(exact, func(i, j int) bool { return exact[i].score > exact[j].score })
		hits, err := collection.SearchKeyword(t.Context(), query, k, "category", "0")
		if err != nil || len(hits) != k {
			t.Fatalf("query returned %d: %v", len(hits), err)
		}
		seen := map[uint64]bool{}
		for _, h := range hits {
			if h.ID >= count || h.ID%2 != 0 || seen[h.ID] {
				t.Fatalf("invalid result %d", h.ID)
			}
			seen[h.ID] = true
		}
		for _, v := range exact[:k] {
			if seen[v.id] {
				found++
			}
		}
	}
	recall := float64(found) / (queries * k)
	t.Logf("filtered recall@%d across %d queries: %.4f", k, queries, recall)
	if recall < .9 {
		t.Fatalf("recall %.4f below 0.90", recall)
	}
}
