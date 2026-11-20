//go:build qdrantparity

package qdrant

import (
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
	for _, creator := range []string{"csharp", "go"} {
		t.Run(creator, func(t *testing.T) {
			name := "parity_" + creator
			path := "/collections/" + name
			if creator == "go" {
				req(t, "PUT", path, map[string]any{"vectors": map[string]any{"direction": map[string]any{"size": 2, "distance": "Cosine"}}}, nil)
				req(t, "PUT", path+"/index?wait=true", map[string]string{"field_name": "category", "field_schema": "keyword"}, nil)
			}
			runner(t, "seed", name)
			collection, err := s.AttachNativeCosine(ctx, name, "direction", 2)
			if err != nil {
				t.Fatal(err)
			}
			seeded, err := collection.GetPoint(ctx, 7)
			if err != nil || seeded.Payload["category"] != "reference" || !slices.Equal(seeded.Vector, []float32{0, 1}) {
				t.Fatalf("C# seed: %+v, %v", seeded, err)
			}
			id := uint64(math.MaxUint64)
			err = collection.UpsertPoint(ctx, NativePoint{id, map[string]any{"category": "go"}, []float32{1, 0}})
			if err != nil {
				t.Fatal(err)
			}
			hits, err := collection.SearchKeyword(ctx, []float32{1, 0}, 2, "category", "go")
			if err != nil || len(hits) != 1 || hits[0].ID != id || math.Abs(hits[0].Score-1) > 1e-6 {
				t.Fatalf("native query: %+v, %v", hits, err)
			}
			runner(t, "verify", name)
			updated, err := collection.GetPoint(ctx, id)
			if err != nil || updated.Payload["category"] != "updated" {
				t.Fatalf("C# update: %+v, %v", updated, err)
			}
			if err := collection.DeletePoint(ctx, id); err != nil {
				t.Fatal(err)
			}
			runner(t, "verify-deletion", name)
		})
	}
}
