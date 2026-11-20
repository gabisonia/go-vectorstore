//go:build qdrantparity

package qdrant

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/google/uuid"
)

// lostNativeReply lets the real server complete a mutation before losing its
// HTTP acknowledgement. It counts attempts on this client independently.
type lostNativeReply struct{ attempts atomic.Int32 }

func (r *lostNativeReply) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || req.Method != http.MethodPut {
		return response, err
	}
	r.attempts.Add(1)
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return nil, io.ErrUnexpectedEOF
}

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
	runner := func(t *testing.T, phase, name string) []byte {
		t.Helper()
		project := filepath.Join("..", "..", "internal", "parity", "qdrant")
		output, err := exec.CommandContext(ctx, "dotnet", "run", "--no-build", "--project", project, "--", phase, port, name).CombinedOutput()
		if err != nil {
			t.Fatalf("reference %s: %v\n%s", phase, err, output)
		}
		return bytes.TrimSpace(output)
	}
	req := func(t *testing.T, method, path string, body, out any) {
		t.Helper()
		if _, err := s.request(ctx, method, path, body, out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	seed := uuid.MustParse("12345678-0000-4000-8000-000000000001")
	id := uuid.MustParse("12345678-0000-4000-8000-000000000002")
	dimensions := map[string]int{"title": 2, "content": 3}
	for _, creator := range []string{"go", "csharp"} {
		t.Run("named_"+creator, func(t *testing.T) {
			name := "named_" + creator
			path := "/collections/" + name
			if creator == "go" {
				req(t, "PUT", path, map[string]any{"vectors": map[string]any{"title": map[string]any{"size": 2, "distance": "Cosine"}, "content": map[string]any{"size": 3, "distance": "Cosine"}}}, nil)
				req(t, "PUT", path+"/index?wait=true", map[string]string{"field_name": "body", "field_schema": "text"}, nil)
			}
			runner(t, "named-seed", name)
			c, err := AttachNativePoints[uuid.UUID](ctx, s, name, dimensions)
			if err != nil {
				t.Fatal(err)
			}
			source, err := c.GetPoint(ctx, seed)
			if err != nil || source.ID != seed || source.Payload["body"] != "retrieval guide" || !slices.Equal(source.Vectors["content"], []float32{1, 0, 0}) || !slices.Equal(source.Vectors["title"], []float32{0, 1}) {
				t.Fatalf("C# point: %+v %v", source, err)
			}
			partial := NamedPoint[uuid.UUID]{ID: seed, Vectors: map[string][]float32{"title": {1, 0}}}
			if err := c.UpsertPoint(ctx, partial); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("partial replacement: %v", err)
			}
			point := NamedPoint[uuid.UUID]{ID: id, Payload: map[string]any{"body": "storage guide"}, Vectors: map[string][]float32{"title": {1, 0}, "content": {0, 1, 0}}}
			if err := c.UpsertPoint(ctx, point); err != nil {
				t.Fatal(err)
			}
			verify := func(name string) {
				t.Helper()
				c, err := AttachNativePoints[uuid.UUID](ctx, s, name, dimensions)
				if err != nil {
					t.Fatal(err)
				}
				threshold := -0.5
				page, err := c.Search(ctx, "content", []float32{1, 0, 0}, 1, NativeSearchOptions{Skip: 1, ScoreThreshold: &threshold})
				rawPage := runner(t, "named-dense", name)
				var referencePage []NamedHit[uuid.UUID]
				if decodeErr := json.Unmarshal(rawPage, &referencePage); decodeErr != nil {
					t.Fatalf("reference page: %s: %v", rawPage, decodeErr)
				}
				if err != nil || len(page) != 1 || len(referencePage) != 1 || page[0].ID != id || referencePage[0].ID != id || math.Abs(page[0].Score-referencePage[0].Score) > 1e-6 {
					t.Fatalf("dense page: %+v vs %+v: %v", page, referencePage, err)
				}
				threshold = 0.5
				filtered, err := c.Search(ctx, "content", []float32{1, 0, 0}, 2, NativeSearchOptions{ScoreThreshold: &threshold})
				if err != nil || len(filtered) != 1 || filtered[0].ID != seed {
					t.Fatalf("score threshold: %+v %v", filtered, err)
				}
				hits, err := c.HybridSearch(ctx, "title", []float32{1, 0}, "body", []string{"retrieval"}, 2)
				var reference []NamedHit[uuid.UUID]
				raw := runner(t, "named-verify", name)
				if err := json.Unmarshal(raw, &reference); err != nil {
					t.Fatalf("reference ranking: %s, %v", raw, err)
				}
				if err != nil || len(hits) != 2 || len(reference) != 2 || hits[0].ID != seed || hits[1].ID != id {
					t.Fatalf("rankings: %+v vs %+v: %v", hits, reference, err)
				}
				for i, h := range hits {
					if h.ID != reference[i].ID || math.Abs(h.Score-reference[i].Score) > 1e-6 {
						t.Fatalf("hit %d: %+v vs %+v", i, h, reference[i])
					}
				}
			}
			verify(name)
			// Recover into a different collection and verify both clients against its restored schema and points.
			var snap response[struct{ Name string }]
			req(t, "POST", path+"/snapshots", nil, &snap)
			file := filepath.Join(t.TempDir(), "collection.snapshot")
			curl := func(args ...string) {
				t.Helper()
				out, err := exec.CommandContext(ctx, "curl", append([]string{"-fsS"}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("snapshot transfer: %s, %v", out, err)
				}
			}
			curl("-o", file, url+path+"/snapshots/"+snap.Result.Name)
			restored := name + "_restored"
			curl("-F", "snapshot=@"+file, url+"/collections/"+restored+"/snapshots/upload?priority=snapshot&wait=true")
			verify(restored)
			runner(t, "named-delete", name)
			if _, err := c.GetPoint(ctx, id); !statusIs(err, http.StatusNotFound) {
				t.Fatalf("read after C# deletion: %v", err)
			}
			verify(restored)
		})
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
			// Lose a real acknowledgement and verify that the client does not replay the write.
			transport := new(lostNativeReply)
			uncertainStore, err := New(StoreOptions{URL: url, HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			defer uncertainStore.Close()
			uncertain, err := uncertainStore.AttachNativeCosine(ctx, name, "direction", 2)
			if err != nil {
				t.Fatal(err)
			}
			err = uncertain.UpsertPoint(ctx, NativePoint{9, map[string]any{"category": "uncertain"}, []float32{0, 1}})
			if !errors.Is(err, ErrNativeOutcomeUnknown) || transport.attempts.Load() != 1 {
				t.Fatalf("lost reply: attempts=%d, err=%v", transport.attempts.Load(), err)
			}
			observed, err := collection.GetPoint(ctx, 9)
			if err != nil || observed.Payload["category"] != "uncertain" {
				t.Fatalf("committed point after lost reply: %+v, %v", observed, err)
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
