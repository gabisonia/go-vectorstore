//go:build qdrantparity

package qdrant

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestNativeDotNetParity exercises the wire mapping against the locked C#
// connector before any native storage contract is added to the public API.
func TestNativeDotNetParity(t *testing.T) {
	url, port := os.Getenv("PARITY_HTTP_URL"), os.Getenv("PARITY_GRPC_PORT")
	if url == "" || port == "" {
		t.Fatal("run scripts/test-qdrant-parity.sh")
	}
	s, err := New(StoreOptions{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	runner := func(t *testing.T, phase, name string) {
		t.Helper()
		project := filepath.Join("..", "..", "internal", "parity", "qdrant")
		output, err := exec.CommandContext(ctx, "dotnet", "run", "--no-build", "--project", project, "--", phase, port, name).CombinedOutput()
		if err != nil {
			t.Fatalf("reference %s: %v\n%s", phase, err, output)
		}
	}
	req := func(t *testing.T, method, path string, body, out any) {
		t.Helper()
		if _, err := s.request(ctx, method, path, body, out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	type point struct {
		ID      uint64
		Payload struct{ Category string }
		Vector  map[string][]float32
		Score   float64
	}
	for _, creator := range []string{"csharp", "go"} {
		t.Run(creator, func(t *testing.T) {
			name := "parity_" + creator
			path := "/collections/" + name
			if creator == "go" {
				req(t, "PUT", path, map[string]any{"vectors": map[string]any{"direction": map[string]any{"size": 2, "distance": "Cosine"}}}, nil)
				req(t, "PUT", path+"/index?wait=true", map[string]string{"field_name": "category", "field_schema": "keyword"}, nil)
			}
			runner(t, "seed", name)
			var seeded response[point]
			req(t, "GET", path+"/points/7", nil, &seeded)
			if seeded.Result.Payload.Category != "reference" || !slices.Equal(seeded.Result.Vector["direction"], []float32{0, 1}) {
				t.Fatalf("C# seed: %+v", seeded.Result)
			}
			id := uint64(math.MaxUint64)
			req(t, "PUT", path+"/points?wait=true", map[string]any{"points": []any{map[string]any{"id": id, "payload": map[string]string{"category": "go"}, "vector": map[string][]float32{"direction": {1, 0}}}}}, nil)
			var hits response[struct{ Points []point }]
			req(t, "POST", path+"/points/query", map[string]any{"using": "direction", "query": []float32{1, 0}, "limit": 2, "filter": map[string]any{"must": []any{map[string]any{"key": "category", "match": map[string]string{"value": "go"}}}}}, &hits)
			if len(hits.Result.Points) != 1 || hits.Result.Points[0].ID != id || math.Abs(hits.Result.Points[0].Score-1) > 1e-6 {
				t.Fatalf("native query: %+v", hits.Result.Points)
			}
			runner(t, "verify", name)
			var updated response[point]
			req(t, "GET", fmt.Sprintf("%s/points/%d", path, id), nil, &updated)
			if updated.Result.Payload.Category != "updated" {
				t.Fatalf("C# update: %+v", updated.Result)
			}
			req(t, "POST", path+"/points/delete?wait=true", map[string]any{"points": []uint64{id}}, nil)
			runner(t, "verify-deletion", name)
		})
	}
}
