//go:build referenceparity

package pineconeparity

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os/exec"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/pinecone"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

const control = "http://localhost:5080"

type localAuth struct{}

func (localAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	req.Header.Set("Api-Key", "local")
	return http.DefaultTransport.RoundTrip(req)
}

func TestSharedIndex(t *testing.T) {
	client := http.Client{Transport: localAuth{}}
	for _, name := range []string{"parity-go", "parity-csharp"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ref := func(phase string) {
				t.Helper()
				if out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput(); err != nil {
					t.Fatalf("C# %s: %s %v", phase, out, err)
				}
			}
			if name == "parity-go" {
				body, err := json.Marshal(map[string]any{"name": name, "metric": "cosine", "dimension": 2, "spec": map[string]any{"serverless": map[string]string{"cloud": "aws", "region": "us-east-1"}}})
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, control+"/indexes", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					t.Fatal(resp.Status)
				}
			} else {
				ref("create")
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, control+"/indexes/"+name, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var desc struct{ Host string }
			if err := json.NewDecoder(resp.Body).Decode(&desc); err != nil {
				t.Fatal(err)
			}
			s := pinecone.NativeSchema{Index: name, Namespace: "articles", Text: "body", Dimensions: 2}
			attach := func(s pinecone.NativeSchema) (*pinecone.NativeIndex, error) {
				return pinecone.AttachNative(ctx, control, "http://"+desc.Host, s, client.Transport)
			}
			wrong := s
			wrong.Dimensions++
			if _, err := attach(wrong); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("wrong dimensions: %v", err)
			}
			c, err := attach(s)
			if err != nil {
				t.Fatal(err)
			}
			ref("seed")
			seed, err := c.Get(ctx, "seed")
			if err != nil || seed.Text != "keep" || !slices.Equal(seed.Vector, []float32{0, 1}) {
				t.Fatalf("C# seed %+v %v", seed, err)
			}
			otherSchema := s
			otherSchema.Namespace = "other"
			other, err := attach(otherSchema)
			if err != nil {
				t.Fatal(err)
			}
			if err := other.Upsert(ctx, pinecone.NativeDocument{ID: "go", Text: "isolated", Vector: []float32{-1, 0}}); err != nil {
				t.Fatal(err)
			}
			p := pinecone.NativeDocument{ID: "go", Text: "initial", Vector: []float32{0, 1}}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			p.Text, p.Vector = "keep", []float32{0.8, 0.6}
			if err := c.Upsert(ctx, p); err != nil {
				t.Fatal(err)
			}
			v := []float32{1, 0}
			all, err := c.Search(ctx, v, 3, nil)
			if err != nil || len(all) != 3 || all[0].ID != "excluded" {
				t.Fatalf("unfiltered: %+v %v", all, err)
			}
			hits, err := c.Search(ctx, v, 3, &p.Text)
			if err != nil || len(hits) != 2 || hits[0].ID != "go" || hits[1].ID != "seed" || math.Abs(hits[0].Score-0.8) > 1e-5 || math.Abs(hits[1].Score) > 1e-5 {
				t.Fatalf("filtered: %+v %v", hits, err)
			}
			ref("verify")
			got, err := c.Get(ctx, "go")
			if err != nil || got.Text != "edited" {
				t.Fatalf("C# update %+v %v", got, err)
			}
			if _, err := c.Get(ctx, "seed"); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("C# delete: %v", err)
			}
			if err := c.Delete(ctx, "go"); err != nil {
				t.Fatal(err)
			}
			ref("verify-delete")
			got, err = other.Get(ctx, "go")
			if err != nil || got.Text != "isolated" {
				t.Fatalf("other namespace %+v %v", got, err)
			}
		})
	}
}
