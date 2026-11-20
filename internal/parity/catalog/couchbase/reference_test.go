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
	// Vector and keyword changes can become searchable at different times.
	// Poll reads only, until both reflect the same acknowledged document update.
	for deadline := time.Now().Add(90 * time.Second); ; {
		all, allErr := c.Search(ctx, query, 3, nil)
		filtered, filterErr := c.Search(ctx, query, 3, &p.Text)
		allReady := allErr == nil && len(all) == 3 && all[0].ID == "excluded" && all[1].ID == "go" && all[2].ID == "seed"
		filterReady := filterErr == nil && len(filtered) == 2 && filtered[0].ID == "go" && filtered[1].ID == "seed" && filtered[0].Score > filtered[1].Score
		if allReady && filterReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("search visibility: all=%+v (%v), filtered=%+v (%v)", all, allErr, filtered, filterErr)
		}
		time.Sleep(time.Second)
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
