package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPGDSN          = "postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable"
	defaultCollectionName = "sample_articles"
	defaultEmbeddingModel = "text-embedding-3-small"
	defaultOpenAIBaseURL  = "https://api.openai.com/v1"
)

type article struct {
	ID       string
	Title    string
	Content  string
	Category string
}

type openAIEmbedder struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

type embeddingsRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	query := flag.String("q", "How can I lower cloud costs without hurting reliability?", "Semantic search query")
	collectionName := flag.String("collection", defaultCollectionName, "Collection name")
	category := flag.String("category", "", "Optional metadata category filter")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, envOrDefault("PG_DSN", defaultPGDSN))
	if err != nil {
		exitf("connect postgres: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		exitf("ping postgres: %v", err)
	}

	store, err := postgres.NewVectorStore(pool, postgres.DefaultStoreOptions())
	if err != nil {
		exitf("create vector store: %v", err)
	}

	embedder, err := newOpenAIEmbedderFromEnv()
	if err != nil {
		exitf("init embedder: %v", err)
	}

	articles := fakeArticles()
	records, dimension, err := buildArticleRecords(ctx, embedder, articles)
	if err != nil {
		exitf("prepare records: %v", err)
	}

	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
		Name:      strings.TrimSpace(*collectionName),
		Dimension: dimension,
		Metric:    vectordata.DistanceCosine,
		Mode:      vectordata.EnsureStrict,
	})
	if err != nil {
		exitf("ensure collection: %v", err)
	}

	if err := collection.Upsert(ctx, records); err != nil {
		exitf("upsert records: %v", err)
	}

	if err := collection.EnsureIndexes(ctx, vectordata.IndexOptions{
		Vector: &vectordata.VectorIndexOptions{
			Method: vectordata.IndexMethodHNSW,
			Metric: vectordata.DistanceCosine,
			HNSW: vectordata.HNSWOptions{
				M:              16,
				EfConstruction: 64,
			},
		},
		Metadata: &vectordata.MetadataIndexOptions{UsePathOps: true},
	}); err != nil {
		exitf("ensure indexes: %v", err)
	}

	queryVector, err := embedder.Embed(ctx, *query)
	if err != nil {
		exitf("embed query: %v", err)
	}

	var filter vectordata.Filter
	if strings.TrimSpace(*category) != "" {
		filter = vectordata.Eq(vectordata.Metadata("category"), strings.TrimSpace(*category))
	}

	results, err := collection.SearchByVector(ctx, queryVector, 3, vectordata.SearchOptions{Filter: filter})
	if err != nil {
		exitf("search: %v", err)
	}

	fmt.Printf("Indexed %d fake articles in collection %q (dimension=%d).\n", len(articles), collection.Name(), dimension)
	fmt.Printf("\nQuery: %s\n", *query)
	if strings.TrimSpace(*category) != "" {
		fmt.Printf("Category filter: %s\n", *category)
	}
	fmt.Println("\nTop matches:")
	for i, res := range results {
		title, _ := res.Record.Metadata["title"].(string)
		categoryValue, _ := res.Record.Metadata["category"].(string)
		fmt.Printf("%d. id=%s | title=%q | category=%q | score=%.4f | distance=%.4f\n", i+1, res.Record.ID, title, categoryValue, res.Score, res.Distance)
	}
}

func newOpenAIEmbedderFromEnv() (*openAIEmbedder, error) {
	apiKey := strings.TrimSpace(envOrDefault("OPENAI_API_KEY", ""))
	if apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is required")
	}

	model := strings.TrimSpace(os.Getenv("OPENAI_EMBEDDING_MODEL"))
	if model == "" {
		model = defaultEmbeddingModel
	}

	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}

	return &openAIEmbedder{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (e *openAIEmbedder) Embed(ctx context.Context, input string) ([]float32, error) {
	payload := embeddingsRequest{
		Model: e.model,
		Input: input,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request embeddings: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embeddings response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr openAIErrorResponse
		if err := json.Unmarshal(responseBody, &apiErr); err == nil && strings.TrimSpace(apiErr.Error.Message) != "" {
			return nil, fmt.Errorf("openai embeddings error: %s (status=%d)", apiErr.Error.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("openai embeddings error: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var parsed embeddingsResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode embeddings response: %w", err)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, errors.New("openai embeddings response was empty")
	}

	out := make([]float32, 0, len(parsed.Data[0].Embedding))
	for _, value := range parsed.Data[0].Embedding {
		out = append(out, float32(value))
	}
	return out, nil
}

func buildArticleRecords(ctx context.Context, embedder *openAIEmbedder, articles []article) ([]vectordata.Record, int, error) {
	records := make([]vectordata.Record, 0, len(articles))
	dimension := 0

	for _, item := range articles {
		embedding, err := embedder.Embed(ctx, item.Title+"\n\n"+item.Content)
		if err != nil {
			return nil, 0, fmt.Errorf("embed article %q: %w", item.ID, err)
		}
		if dimension == 0 {
			dimension = len(embedding)
		}
		if len(embedding) != dimension {
			return nil, 0, fmt.Errorf("dimension mismatch for article %q: expected %d got %d", item.ID, dimension, len(embedding))
		}

		content := item.Content
		records = append(records, vectordata.Record{
			ID:      item.ID,
			Vector:  embedding,
			Content: &content,
			Metadata: map[string]any{
				"title":    item.Title,
				"category": item.Category,
				"source":   "fake",
			},
		})
	}

	if dimension == 0 {
		return nil, 0, errors.New("no articles to index")
	}

	return records, dimension, nil
}

func fakeArticles() []article {
	return []article{
		{
			ID:       "article-1",
			Title:    "Running semantic search with pgvector in Postgres",
			Category: "engineering",
			Content:  "pgvector adds a vector data type and distance operators to Postgres. You can store embeddings and run nearest-neighbor queries directly in SQL.",
		},
		{
			ID:       "article-2",
			Title:    "Context deadlines and cancellation patterns in Go services",
			Category: "backend",
			Content:  "Using context propagation with deadlines prevents hanging calls and keeps distributed systems responsive under pressure.",
		},
		{
			ID:       "article-3",
			Title:    "Reducing cloud costs with caching and batched writes",
			Category: "architecture",
			Content:  "Teams can lower spend by reducing duplicate calls, batching writes, and choosing query-friendly storage patterns for read-heavy traffic.",
		},
	}
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
