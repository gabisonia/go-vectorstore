//go:build referenceparity

package chromaparity

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/chroma"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSharedCollection(t *testing.T) {
	const base = "http://localhost:8000/api/v2/tenants/default_tenant/databases/default_database/collections"
	client := http.Client{Timeout: 15 * time.Second}
	for _, name := range []string{"parity-go", "parity-csharp"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ref := func(phase string) {
				t.Helper()
				out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput()
				if err != nil {
					t.Fatalf("C# %s: %s %v", phase, out, err)
				}
			}
			if name == "parity-go" {
				data, err := json.Marshal(map[string]any{"name": name, "configuration": map[string]any{"hnsw": map[string]string{"space": "cosine"}}})
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
					t.Fatalf("create: %s %s", resp.Status, raw)
				}
			}
			ref("seed")
			schema := chroma.NativeSchema{Collection: name, Dimensions: 2}
			wrong := schema
			wrong.Dimensions = 3
			if _, err := chroma.AttachNative(ctx, "http://localhost:8000", wrong, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("wrong dimensions: %v", err)
			}
			c, err := chroma.AttachNative(ctx, "http://localhost:8000", schema, nil)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := c.Get(ctx, "seed")
			if err != nil || seed.Document != "reference" || seed.Metadata["category"] != "guide" || !slices.Equal(seed.Embedding, []float32{0, 1}) {
				t.Fatalf("C# seed: %+v %v", seed, err)
			}
			point := chroma.NativePoint{ID: "go", Document: "original", Metadata: map[string]any{"category": "sample"}, Embedding: []float32{1, 0}}
			if err := c.Upsert(ctx, point); err != nil {
				t.Fatal(err)
			}
			if err := c.UpdateDocument(ctx, "go", "Go update"); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, point.Embedding, 2)
			if err != nil || len(hits) != 2 || hits[0].ID != "go" || hits[1].ID != "seed" || math.Abs(hits[0].Distance) > 1e-6 || math.Abs(hits[1].Distance-1) > 1e-6 {
				t.Fatalf("native query: %+v %v", hits, err)
			}
			ref("verify")
			if _, err := c.Get(ctx, "go"); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("C# delete: %v", err)
			}
			if err := c.Delete(ctx, "seed"); err != nil {
				t.Fatal(err)
			}
			ref("verify-delete")
		})
	}
}
