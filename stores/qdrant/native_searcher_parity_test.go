//go:build qdrantparity

package qdrant

import (
	"github.com/gabisonia/go-vectorstore/internal/parity/rankedguides"
	"os"
	"testing"
)

// The parity script owns the disposable server and its collections.
func TestNativeRankedGuides(t *testing.T) {
	const name = "parity_ranked_guides"
	s, err := New(StoreOptions{URL: os.Getenv("PARITY_HTTP_URL")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	path := "/collections/" + name
	if _, err := s.request(ctx, "PUT", path, map[string]any{"vectors": map[string]any{"content_vector": map[string]any{"size": 2, "distance": "Cosine"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.request(ctx, "PUT", path+"/index?wait=true", map[string]string{"field_name": "category", "field_schema": "keyword"}, nil); err != nil {
		t.Fatal(err)
	}
	points, err := AttachNativePoints[uint64](ctx, s, name, map[string]int{"content_vector": 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rankedguides.Articles() {
		if err := points.UpsertPoint(ctx, NamedPoint[uint64]{ID: a.ID, Payload: map[string]any{"category": a.Category}, Vectors: map[string][]float32{"content_vector": a.Direction}}); err != nil {
			t.Fatal(err)
		}
	}
	searcher, err := NewNativeModelSearcher[rankedguides.Article](points, nil)
	if err != nil {
		t.Fatal(err)
	}
	rankedguides.Check(t, searcher)
}
