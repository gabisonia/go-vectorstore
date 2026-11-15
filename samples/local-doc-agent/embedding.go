package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type localEmbedder struct {
	URL, Model string
	Dimensions int
	Client     *http.Client
}

func (e *localEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Truncate bool     `json:"truncate"`
	}{e.Model, inputs, false})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := e.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("local embeddings: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local embeddings: HTTP %d (check model setup and input length)", response.StatusCode)
	}
	const maximum = 4 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, fmt.Errorf("embedding response too large")
	}
	var decoded struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("embedding count: got %d, expected %d", len(decoded.Embeddings), len(inputs))
	}
	for _, vector := range decoded.Embeddings {
		if _, err := vectordata.NormalizeModelVector(vector, vectordata.VectorProperty{Dimensions: e.Dimensions}); err != nil {
			return nil, fmt.Errorf("embedding model/configuration: %w", err)
		}
	}
	return decoded.Embeddings, nil
}
