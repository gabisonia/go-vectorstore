//go:build referenceparity

package milvusparity

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os/exec"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/milvus"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

const endpoint = "http://localhost:19530"

func createGoCollection(t *testing.T, name string) {
	t.Helper()
	// The REST creator is independent of the reference client's gRPC schema builder.
	const schema = `{"autoID":false,"enableDynamicField":false,"fields":[
		{"fieldName":"id","dataType":"VarChar","isPrimary":true,"elementTypeParams":{"max_length":"128"}},
		{"fieldName":"body","dataType":"VarChar","nullable":true,"elementTypeParams":{"max_length":"256"}},
		{"fieldName":"direction","dataType":"FloatVector","elementTypeParams":{"dim":"2"}}]}`
	payload, err := json.Marshal(map[string]any{"collectionName": name, "schema": json.RawMessage(schema), "indexParams": []map[string]string{{"fieldName": "direction", "indexName": "direction", "indexType": "FLAT", "metricType": "COSINE"}}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/v2/vectordb/collections/create", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var reply struct {
		Code    *int
		Message string
	}
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil || res.StatusCode != http.StatusOK || reply.Code == nil || *reply.Code != 0 {
		t.Fatalf("create collection: %s %s %v", res.Status, reply.Message, err)
	}
}

func TestSharedCollection(t *testing.T) {
	for _, name := range []string{"parity_go", "parity_csharp"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ref := func(phase string) {
				t.Helper()
				if out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput(); err != nil {
					t.Fatalf("C# %s: %s %v", phase, out, err)
				}
			}
			if name == "parity_go" {
				createGoCollection(t, name)
			}
			ref("seed")
			s := milvus.NativeSchema{Collection: name, Key: "id", Text: "body", Vector: "direction", Dimensions: 2}
			wrong := s
			wrong.Dimensions++
			if _, err := milvus.AttachNative(ctx, endpoint, wrong, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("dimension mismatch: %v", err)
			}
			c, err := milvus.AttachNative(ctx, endpoint, s, nil)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := c.Get(ctx, "seed")
			if err != nil || seed.Text == nil || *seed.Text != "keep" || !slices.Equal(seed.Vector, []float32{0, 1}) {
				t.Fatalf("C# seed: %+v %v", seed, err)
			}
			nullable := milvus.NativeDocument{ID: "null-text", Vector: []float32{-1, 0}}
			if err := c.Upsert(ctx, nullable); err != nil {
				t.Fatal(err)
			}
			gotNull, err := c.Get(ctx, nullable.ID)
			if err != nil || gotNull.Text != nil || !slices.Equal(gotNull.Vector, nullable.Vector) {
				t.Fatalf("NULL text round-trip: %+v %v", gotNull, err)
			}
			initial, keep := "initial", "keep"
			p := milvus.NativeDocument{ID: "go", Text: &initial, Vector: []float32{0, 1}}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			p.Text, p.Vector = &keep, []float32{1, 0}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, p.Vector, 3, &keep)
			if err != nil || len(hits) != 2 || hits[0].ID != "go" || hits[1].ID != "seed" || math.Abs(hits[0].Score-1) > 1e-6 || math.Abs(hits[1].Score) > 1e-6 {
				t.Fatalf("filtered ranking: %+v %v", hits, err)
			}
			literal := `" or body == "keep`
			hits, err = c.Search(ctx, p.Vector, 3, &literal)
			if err != nil || len(hits) != 0 {
				t.Fatalf("literal filter: %+v %v", hits, err)
			}
			ref("verify")
			got, err := c.Get(ctx, "go")
			if err != nil || got.Text == nil || *got.Text != "edited" {
				t.Fatalf("C# update: %+v %v", got, err)
			}
			if _, err = c.Get(ctx, "seed"); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("C# deletion: %v", err)
			}
			if err := c.Delete(ctx, "go"); err != nil {
				t.Fatal(err)
			}
			ref("verify-delete")
		})
	}
}
