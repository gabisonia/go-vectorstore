//go:build qdrantparity

package qdrant

import (
	"fmt"
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
	if _, err := s.request(ctx, "PUT", path, map[string]any{"vectors": map[string]any{"Direction": map[string]any{"size": 2, "distance": "Cosine"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.request(ctx, "PUT", path+"/index?wait=true", map[string]string{"field_name": "Category", "field_schema": "keyword"}, nil); err != nil {
		t.Fatal(err)
	}
	points, err := AttachNativePoints[uint64](ctx, s, name, map[string]int{"Direction": 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range articles() {
		if err := points.UpsertPoint(ctx, NamedPoint[uint64]{ID: a.ID, Payload: map[string]any{"Category": a.Category}, Vectors: map[string][]float32{"Direction": a.Direction}}); err != nil {
			t.Fatal(err)
		}
	}
	searcher, err := NewNativeVectorSearcher(points, "Direction", func(p NamedPoint[uint64]) (article, error) {
		category, ok := p.Payload["Category"].(string)
		if !ok {
			return article{}, fmt.Errorf("point %d has no string Category", p.ID)
		}
		return article{p.ID, category, p.Vectors["Direction"]}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkRankedGuides(t, searcher)
}
