//go:build referenceparity

package couchbaseparity

import (
	"errors"
	"net/http"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/couchbase"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type credentials struct{}

func (credentials) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	req.SetBasicAuth("Administrator", "parity-fixture")
	return http.DefaultTransport.RoundTrip(req)
}

func TestSharedIndex(t *testing.T) {
	ctx := t.Context()
	ref := func(phase string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase).CombinedOutput(); err != nil {
			t.Fatalf("C# %s: %s %v", phase, out, err)
		} else {
			t.Log(string(out))
		}
	}
	ref("seed")
	s := couchbase.NativeSchema{Bucket: "parity", Scope: "docs", Collection: "articles", Index: "vectors", Text: "Body", Vector: "Direction", Dimensions: 2}
	attach := func(s couchbase.NativeSchema) (*couchbase.NativeCollection, error) {
		return couchbase.AttachNative(ctx, "http://localhost:8093", "http://localhost:8094", s, credentials{})
	}
	wrong := s
	wrong.Dimensions++
	if _, err := attach(wrong); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("wrong dimension: %v", err)
	}
	c, err := attach(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Get(ctx, "seed")
	if err != nil || p.Text != "keep" || !slices.Equal(p.Vector, []float32{0, 1}) {
		t.Fatalf("C# seed %+v %v", p, err)
	}
	p = couchbase.NativeDocument{ID: "go", Text: "initial", Vector: []float32{0, 1}}
	if err := c.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Text, p.Vector = "keep", []float32{0.8, 0.6}
	if err := c.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	query := []float32{1, 0}
	for deadline := time.Now().Add(time.Minute); ; {
		hits, err := c.Search(ctx, query, 3, nil)
		if err == nil && len(hits) == 3 && hits[0].ID == "excluded" && hits[1].ID == "go" && hits[2].ID == "seed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unfiltered ranking %+v %v", hits, err)
		}
		time.Sleep(time.Second)
	}
	hits, err := c.Search(ctx, query, 3, &p.Text)
	if err != nil || len(hits) != 2 || hits[0].ID != "go" || hits[1].ID != "seed" || hits[0].Score <= hits[1].Score {
		t.Fatalf("filtered ranking %+v %v", hits, err)
	}
	ref("verify")
	p, err = c.Get(ctx, "go")
	if err != nil || p.Text != "edited" {
		t.Fatalf("C# update: %+v %v", p, err)
	}
	if _, err := c.Get(ctx, "seed"); !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatalf("C# delete: %v", err)
	}
	if err := c.Delete(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	ref("verify-delete")
}
