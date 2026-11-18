package main

import (
	"context"
	"strings"
	"unicode"
)

const dimensions = 12

type Document struct {
	ID       string    `vectordata:"key" json:"id"`
	Title    string    `vectordata:"data" json:"title"`
	Category string    `vectordata:"data,index" json:"category"`
	Body     string    `vectordata:"data,fulltext" json:"body"`
	Vector   []float32 `vectordata:"vector,dimensions=12,metric=cosine,index,method=hnsw,source=Body" json:"-"`
}

var categories = []string{"models", "observability", "orchestration", "storage", "custom"}

// Fixture embeddings make orchestration/retrieval reproducible without an API
// key or model download. This small vocabulary is not a semantic model.
type fixtureEmbedder struct{}

func (fixtureEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	groups := [][]string{
		{"postgres", "postgresql", "pgvector", "hnsw"},
		{"sqlite", "embedded", "wal", "file"},
		{"redis", "cache", "snapshot", "json"},
		{"aspire", "apphost", "orchestration", "dashboard"},
		{"otel", "opentelemetry", "observability", "metrics", "traces", "logs", "telemetry"},
		{"model", "models", "embedding", "embeddings", "vector", "vectors", "similarity", "search"},
		{"filter", "filters", "category", "where", "metadata"},
		{"backup", "backups", "persist", "persistence", "durability", "restore"},
		{"docker", "container", "containers", "service", "services", "startup"},
		{"go", "golang", "typed", "struct", "structs", "codec"},
		{"hybrid", "fulltext", "keyword", "keywords", "rrf"},
		{"memory", "testing", "test", "tests", "fixture"},
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[i] = make([]float32, dimensions)
		for j := range out[i] {
			out[i][j] = 0.01
		}
		for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			for j, synonyms := range groups {
				for _, synonym := range synonyms {
					if word == synonym {
						out[i][j]++
					}
				}
			}
		}
	}
	return out, nil
}

var catalog = []Document{
	{ID: "seed-postgres", Title: "PostgreSQL + pgvector", Category: "storage", Body: "PostgreSQL with pgvector stores typed model data and vector embeddings. HNSW indexes support vector similarity search. Use PostgreSQL for hybrid search that combines vectors with fulltext keywords."},
	{ID: "seed-sqlite", Title: "SQLite backup and persistence", Category: "storage", Body: "SQLite is an embedded database file. WAL supports concurrent readers and a writer. Use a SQLite-aware backup or checkpoint procedure; copying only the main file while writes are active is not a safe backup."},
	{ID: "seed-redis", Title: "Redis model snapshots", Category: "storage", Body: "Redis supports canonical model snapshots and optional native indexes. Persistence and durability depend on the Redis server configuration. Verify backups and restore behavior before relying on persistence."},
	{ID: "seed-aspire", Title: "A Go AppHost in Aspire 17", Category: "orchestration", Body: "Aspire 17 offers preview Go AppHosts. The AppHost wires services, containers, references, health checks and startup dependencies. Run it with the Aspire CLI rather than go run. The dashboard shows resource state and endpoints."},
	{ID: "seed-telemetry", Title: "Follow a search request", Category: "observability", Body: "OpenTelemetry sends structured logs, metrics and traces to the Aspire dashboard. Open the vector-api resource to inspect a search trace and its vectorstore search span. Metrics show request duration and search latency."},
	{ID: "seed-models", Title: "Typed models and automatic embeddings", Category: "models", Body: "Go structs declare key, data and vector properties using vectordata tags. The store embedder fills vectors from a source property. Typed codecs map Go models to dynamic records while preserving native values."},
	{ID: "seed-filters", Title: "Filter before retrieving results", Category: "models", Body: "Use a portable filter on indexed category metadata to constrain vector search. Filters are applied before the result limit. Search returns a typed page without vector payloads unless explicitly requested."},
	{ID: "seed-fixtures", Title: "Fixture embeddings are for learning", Category: "models", Body: "This sample uses a deterministic vocabulary fixture for vector embeddings. It helps test the Go library and Aspire orchestration without model downloads. For semantic search, choose a real embedding model and a new collection with matching dimensions."},
}
