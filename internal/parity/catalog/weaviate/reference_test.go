//go:build referenceparity

package weaviateparity

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

	"github.com/gabisonia/go-vectorstore/stores/weaviate"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/google/uuid"
)

func TestSharedClass(t *testing.T) {
	const endpoint = "http://localhost:8080"
	seed := uuid.MustParse("313939d8-0101-4a0a-8000-000000000001")
	id := uuid.MustParse("313939d8-0101-4a0a-8000-000000000002")
	for _, name := range []string{"FromGo", "FromCSharp"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ref := func(phase string) {
				t.Helper()
				out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput()
				if err != nil {
					t.Fatalf("C# %s: %s %v", phase, out, err)
				}
			}
			if name == "FromGo" {
				def := map[string]any{"class": name, "properties": []any{map[string]any{"name": "body", "dataType": []string{"text"}}}, "vectorConfig": map[string]any{"direction": map[string]any{"vectorizer": map[string]any{"none": map[string]any{}}, "vectorIndexType": "flat", "vectorIndexConfig": map[string]string{"distance": "cosine"}}}}
				data, err := json.Marshal(def)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/schema", bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				r, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Body.Close()
				if r.StatusCode/100 != 2 {
					body, _ := io.ReadAll(r.Body)
					t.Fatalf("create: %s %s", r.Status, body)
				}
			}
			ref("seed")
			s := weaviate.NativeSchema{Class: name, Text: "body", Vector: "direction", Dimensions: 2}
			wrong := s
			wrong.Vector = "absent"
			if _, err := weaviate.AttachNative(ctx, endpoint, wrong, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("wrong selected vector: %v", err)
			}
			c, err := weaviate.AttachNative(ctx, endpoint, s, nil)
			if err != nil {
				t.Fatal(err)
			}
			p, err := c.Get(ctx, seed)
			if err != nil || p.Text != "reference" || !slices.Equal(p.Vector, []float32{0, 1}) {
				t.Fatalf("reference seed: %+v %v", p, err)
			}
			p = weaviate.NativePoint{ID: id, Text: "initial", Vector: []float32{0, 1}}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			p.Text = "Go update"
			p.Vector = []float32{1, 0}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, p.Vector, 2)
			if err != nil || len(hits) != 2 || hits[0].ID != id || hits[1].ID != seed || math.Abs(hits[0].Distance) > 1e-6 || math.Abs(hits[1].Distance-1) > 1e-6 {
				t.Fatalf("Go ranking: %+v %v", hits, err)
			}
			ref("verify")
			p, err = c.Get(ctx, id)
			if err != nil || p.Text != "reference update" {
				t.Fatalf("C# update: %+v %v", p, err)
			}
			var missing *weaviate.ResponseError
			if _, err := c.Get(ctx, seed); !errors.As(err, &missing) || missing.StatusCode != 404 {
				t.Fatalf("C# deletion: %v", err)
			}
			if err := c.Delete(ctx, id); err != nil {
				t.Fatal(err)
			}
			ref("verify-delete")
		})
	}
}
