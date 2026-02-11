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
	defaultPGDSN             = "postgres://postgres:postgres@localhost:54330/vectorstore_test?sslmode=disable"
	defaultCollectionName    = "ragrimosa_story"
	defaultEmbeddingModel    = "text-embedding-3-small"
	defaultChatModel         = "gpt-4o-mini"
	defaultOpenAIBaseURL     = "https://api.openai.com/v1"
	defaultQuery             = "How did Lacrimosa and Rex build trust, and what happened later in Amsterdam?"
	defaultAssistantBehavior = "You are RAGrimosa, a retrieval-augmented assistant. Answer only from the retrieved context. If context is missing, say what is missing instead of inventing facts."
)

type storyChunk struct {
	ID      string
	Section string
	Text    string
}

type openAIClient struct {
	apiKey         string
	embeddingModel string
	chatModel      string
	baseURL        string
	httpClient     *http.Client
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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	query := flag.String("q", defaultQuery, "Question for RAGrimosa")
	collectionName := flag.String("collection", defaultCollectionName, "Collection name")
	topK := flag.Int("topk", 4, "How many chunks to retrieve")
	flag.Parse()

	if *topK <= 0 {
		exitf("topk must be greater than 0")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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

	client, err := newOpenAIClientFromEnv()
	if err != nil {
		exitf("init openai client: %v", err)
	}

	chunks := lacrimosaStoryChunks()
	records, dimension, err := buildChunkRecords(ctx, client, chunks)
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
		exitf("upsert chunks: %v", err)
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

	queryVector, err := client.Embed(ctx, *query)
	if err != nil {
		exitf("embed query: %v", err)
	}

	results, err := collection.SearchByVector(ctx, queryVector, *topK, vectordata.SearchOptions{})
	if err != nil {
		exitf("search chunks: %v", err)
	}

	retrievedContext := buildRetrievedContext(results)
	answer, err := client.GenerateAnswer(ctx, *query, retrievedContext)
	if err != nil {
		exitf("generate answer: %v", err)
	}

	fmt.Printf("Indexed %d Lacrimosa chunks in collection %q (dimension=%d).\n", len(chunks), collection.Name(), dimension)
	fmt.Printf("\nPrompt: %s\n", *query)
	fmt.Println("\nRetrieved chunks from DB:")
	if len(results) == 0 {
		fmt.Println("No chunks found.")
	} else {
		for i, res := range results {
			section, _ := res.Record.Metadata["section"].(string)
			content := ""
			if res.Record.Content != nil {
				content = truncate(strings.TrimSpace(*res.Record.Content), 160)
			}
			fmt.Printf("%d. id=%s | section=%q | score=%.4f | distance=%.4f\n", i+1, res.Record.ID, section, res.Score, res.Distance)
			fmt.Printf("   %s\n", content)
		}
	}

	fmt.Println("\nRAGrimosa answer:")
	fmt.Println(answer)
}

func newOpenAIClientFromEnv() (*openAIClient, error) {
	apiKey := strings.TrimSpace(envOrDefault("OPENAI_API_KEY", ""))
	if apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is required")
	}

	embeddingModel := strings.TrimSpace(os.Getenv("OPENAI_EMBEDDING_MODEL"))
	if embeddingModel == "" {
		embeddingModel = defaultEmbeddingModel
	}

	chatModel := strings.TrimSpace(os.Getenv("OPENAI_CHAT_MODEL"))
	if chatModel == "" {
		chatModel = defaultChatModel
	}

	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}

	return &openAIClient{
		apiKey:         apiKey,
		embeddingModel: embeddingModel,
		chatModel:      chatModel,
		baseURL:        strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}, nil
}

func (c *openAIClient) Embed(ctx context.Context, input string) ([]float32, error) {
	payload := embeddingsRequest{Model: c.embeddingModel, Input: input}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embeddings request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request embeddings: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embeddings response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseOpenAIError("embeddings", resp.StatusCode, responseBody)
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

func (c *openAIClient) GenerateAnswer(ctx context.Context, question, retrievedContext string) (string, error) {
	prompt := fmt.Sprintf("Question:\n%s\n\nRetrieved context from database:\n%s\n\nAnswer using only the retrieved context. If something is unknown, say it is not in the story.", question, retrievedContext)
	payload := chatCompletionsRequest{
		Model: c.chatModel,
		Messages: []chatMessage{
			{Role: "system", Content: defaultAssistantBehavior},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build chat request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request chat completion: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read chat response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", parseOpenAIError("chat completions", resp.StatusCode, responseBody)
	}

	var parsed chatCompletionsResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", errors.New("openai chat response was empty")
	}

	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func parseOpenAIError(operation string, status int, responseBody []byte) error {
	var apiErr openAIErrorResponse
	if err := json.Unmarshal(responseBody, &apiErr); err == nil && strings.TrimSpace(apiErr.Error.Message) != "" {
		return fmt.Errorf("openai %s error: %s (status=%d)", operation, apiErr.Error.Message, status)
	}
	return fmt.Errorf("openai %s error: status=%d body=%s", operation, status, string(responseBody))
}

func buildChunkRecords(ctx context.Context, client *openAIClient, chunks []storyChunk) ([]vectordata.Record, int, error) {
	records := make([]vectordata.Record, 0, len(chunks))
	dimension := 0

	for _, chunk := range chunks {
		embedding, err := client.Embed(ctx, chunk.Section+"\n\n"+chunk.Text)
		if err != nil {
			return nil, 0, fmt.Errorf("embed chunk %q: %w", chunk.ID, err)
		}

		if dimension == 0 {
			dimension = len(embedding)
		}
		if len(embedding) != dimension {
			return nil, 0, fmt.Errorf("dimension mismatch for chunk %q: expected %d got %d", chunk.ID, dimension, len(embedding))
		}

		content := chunk.Text
		records = append(records, vectordata.Record{
			ID:      chunk.ID,
			Vector:  embedding,
			Content: &content,
			Metadata: map[string]any{
				"section": chunk.Section,
				"source":  "lacrimosa_story",
			},
		})
	}

	if dimension == 0 {
		return nil, 0, errors.New("no chunks to index")
	}

	return records, dimension, nil
}

func buildRetrievedContext(results []vectordata.SearchResult) string {
	if len(results) == 0 {
		return "No chunks were retrieved from the database."
	}

	var b strings.Builder
	for i, res := range results {
		section, _ := res.Record.Metadata["section"].(string)
		content := ""
		if res.Record.Content != nil {
			content = strings.TrimSpace(*res.Record.Content)
		}
		fmt.Fprintf(&b, "Chunk %d (%s): %s\n\n", i+1, section, content)
	}

	return strings.TrimSpace(b.String())
}

func lacrimosaStoryChunks() []storyChunk {
	return []storyChunk{
		{
			ID:      "chunk-01-origin",
			Section: "Origin in Tbilisi",
			Text:    "Lacrimosa is a black newborn kitten found in Dighomi Massivi, Tbilisi. She was discovered by a young girl named Sopho beside discarded bricks.",
		},
		{
			ID:      "chunk-02-name",
			Section: "Name and Meaning",
			Text:    "Sopho named her Lacrimosa after the Lacrimosa movement in Mozart's Requiem and brought her to live in Gldani, Tbilisi.",
		},
		{
			ID:      "chunk-03-home",
			Section: "Early Environment",
			Text:    "Sopho lived in an apartment that smelled of books and rosemary bread. A German Shepherd named Rex also lived there. Rex was 12 years old and described as the bravest dog.",
		},
		{
			ID:      "chunk-04-introduction",
			Section: "Kitten and Dog Relationship",
			Text:    "Rex initially reacted to Lacrimosa with barking and caution. Sopho supervised gradual introductions. Over time, Lacrimosa slept close to Rex, showing trust and comfort.",
		},
		{
			ID:      "chunk-05-final-period",
			Section: "Rex's Final Period",
			Text:    "As Rex aged, he had difficulty climbing stairs. Lacrimosa often followed him and stayed close. Rex eventually passed away, and Lacrimosa remained near him in his last moments.",
		},
		{
			ID:      "chunk-06-relocation",
			Section: "Move to Amsterdam",
			Text:    "Years later, Sopho and Lacrimosa moved from Tbilisi to Amsterdam. Lacrimosa was about 11 years old then, and their new apartment was by a park.",
		},
		{
			ID:      "chunk-07-routine",
			Section: "Life in Amsterdam",
			Text:    "Lacrimosa adapted quickly in Amsterdam. Her daily routine included sitting on windowsills, observing city activity, and resting on the radiator and bookshelf.",
		},
		{
			ID:      "chunk-08-treats-status",
			Section: "Present Status",
			Text:    "Lacrimosa receives thin slices of banana as treats and still lives in Amsterdam with Sopho. She symbolizes a connection between Sopho's childhood in Tbilisi and adulthood in the Netherlands, with themes of survival, adaptation, companionship, and continuity.",
		},
	}
}

func truncate(value string, maxRunes int) string {
	if maxRunes <= 3 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-3]) + "..."
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
