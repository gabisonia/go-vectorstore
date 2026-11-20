//go:build referenceparity

package elasticsearchparity

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

	"github.com/gabisonia/go-vectorstore/stores/elasticsearch"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestVendorSharedIndex(t *testing.T) {
	const endpoint = "http://localhost:9200"
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
				mapping := map[string]any{"mappings": map[string]any{"properties": map[string]any{"body": map[string]string{"type": "text"}, "direction": map[string]any{"type": "dense_vector", "dims": 2, "index": true, "similarity": "cosine", "index_options": map[string]string{"type": "hnsw"}}}}}
				data, err := json.Marshal(mapping)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+"/"+name, bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				if res.StatusCode/100 != 2 {
					msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
					t.Fatalf("create mapping: %s %s", res.Status, msg)
				}
			}
			ref("seed")
			schema := elasticsearch.NativeSchema{Index: name, Text: "body", Vector: "direction", Dimensions: 2}
			wrong := schema
			wrong.Dimensions = 3
			if _, err := elasticsearch.AttachNative(ctx, endpoint, wrong, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("wrong dimensions: %v", err)
			}
			c, err := elasticsearch.AttachNative(ctx, endpoint, schema, nil)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := c.Get(ctx, "seed")
			if err != nil || seed.Text != "reference" || !slices.Equal(seed.Vector, []float32{0, 1}) {
				t.Fatalf("reference seed: %+v %v", seed, err)
			}
			p := elasticsearch.NativeDocument{ID: "go", Text: "initial", Vector: []float32{0, 1}}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			p.Text = "Go update"
			p.Vector = []float32{1, 0}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, p.Vector, 2, 20)
			if err != nil || len(hits) != 2 || hits[0].ID != "go" || hits[1].ID != "seed" || math.Abs(hits[0].Score-1) > 1e-6 || math.Abs(hits[1].Score-0.5) > 1e-6 {
				t.Fatalf("native ranking: %+v %v", hits, err)
			}
			ref("verify")
			p, err = c.Get(ctx, "go")
			if err != nil || p.Text != "C# update" {
				t.Fatalf("reference update: %+v %v", p, err)
			}
			var status *elasticsearch.ResponseError
			if _, err = c.Get(ctx, "seed"); !errors.As(err, &status) || status.StatusCode != 404 {
				t.Fatalf("reference deletion: %v", err)
			}
			if err := c.Delete(ctx, "go"); err != nil {
				t.Fatal(err)
			}
			ref("verify-delete")
		})
	}
}
