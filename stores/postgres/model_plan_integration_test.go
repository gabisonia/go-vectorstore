//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIntegrationModelQueryIndexes(t *testing.T) {
	c, records := modelFixture(t, 32, 5000, true)
	ctx := context.Background()
	vector := records[0].Vectors["Vector"]
	tests := []struct {
		name    string
		plan    func() (string, []any, error)
		indexes []string
	}{
		{name: "vector", plan: func() (string, []any, error) {
			q, a, _, err := c.searchPlan(vector, 10, vectordata.ModelSearchOptions{})
			return q, a, err
		}, indexes: []string{"idx_models_Vector_hnsw"}},
		{name: "scalar filter", plan: func() (string, []any, error) {
			return c.queryPlan(vectordata.Eq(vectordata.Column("Category"), "rare"), 10, vectordata.QueryOptions{})
		}, indexes: []string{"idx_models_Category_data"}},
		{name: "array membership", plan: func() (string, []any, error) {
			return c.queryPlan(vectordata.Contains(vectordata.Column("Tags"), "rare"), 10, vectordata.QueryOptions{})
		}, indexes: []string{"idx_models_Tags_data"}},
		{name: "array overlap", plan: func() (string, []any, error) {
			return c.queryPlan(vectordata.ContainsAny(vectordata.Column("Tags"), "rare", "missing"), 10, vectordata.QueryOptions{})
		}, indexes: []string{"idx_models_Tags_data"}},
		{name: "array containment", plan: func() (string, []any, error) {
			return c.queryPlan(vectordata.ContainsAll(vectordata.Column("Tags"), "rare", "rare"), 10, vectordata.QueryOptions{})
		}, indexes: []string{"idx_models_Tags_data"}},
		{name: "filtered vector", plan: func() (string, []any, error) {
			q, a, _, err := c.searchPlan(vector, 10, vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("Category"), "rare")})
			return q, a, err
		}, indexes: []string{"idx_models_Category_data"}},
		{name: "hybrid candidates", plan: func() (string, []any, error) {
			q, a, _, err := c.hybridPlan(vector, "orchid", 10, vectordata.HybridSearchOptions{})
			return q, a, err
		}, indexes: []string{"idx_models_Vector_hnsw", "idx_models_Body_fts"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, args, err := tt.plan()
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := c.store.pool.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			var indexes []string
			var visit func(any)
			visit = func(v any) {
				switch node := v.(type) {
				case map[string]any:
					if name, ok := node["Index Name"].(string); ok {
						indexes = append(indexes, name)
					}
					for _, child := range node {
						visit(child)
					}
				case []any:
					for _, child := range node {
						visit(child)
					}
				}
			}
			visit(decoded)
			for _, expected := range tt.indexes {
				if !slices.Contains(indexes, expected) {
					t.Fatalf("expected index %q, found %v; plan: %s", expected, indexes, raw)
				}
			}
			t.Logf("indexes: %v", indexes)
		})
	}
}
