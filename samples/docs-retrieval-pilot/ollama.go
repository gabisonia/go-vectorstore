package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type modelIdentity struct {
	Name          string
	Digest        string
	Dimensions    int
	ContextTokens int
	Threads       int
}

type ollama struct {
	URL    string
	Model  modelIdentity
	Client *http.Client
}

func (o *ollama) request(ctx context.Context, method, path string, input, output any) error {
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, o.URL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama %s: HTTP %d; check model setup/input size", path, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return fmt.Errorf("Ollama response exceeds 4 MiB")
	}
	return json.Unmarshal(b, output)
}

func (o *ollama) identify(ctx context.Context) (string, error) {
	u, err := url.Parse(o.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", fmt.Errorf("Ollama URL must be an HTTP(S) origin without credentials")
	}
	o.URL = strings.TrimRight(o.URL, "/")
	var tags struct {
		Models []struct{ Name, Digest string }
	}
	if err := o.request(ctx, "GET", "/api/tags", nil, &tags); err != nil {
		return "", err
	}
	o.Model.Digest = ""
	for _, model := range tags.Models {
		if model.Name == o.Model.Name {
			o.Model.Digest = model.Digest
		}
	}
	if o.Model.Digest == "" {
		return "", fmt.Errorf("model %q is not installed; pull it first", o.Model.Name)
	}
	var version struct{ Version string }
	err = o.request(ctx, "GET", "/api/version", nil, &version)
	return version.Version, err
}

func (o *ollama) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	var output struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	input := struct {
		Model    string         `json:"model"`
		Input    []string       `json:"input"`
		Truncate bool           `json:"truncate"`
		Options  map[string]int `json:"options"`
	}{o.Model.Name, inputs, false, map[string]int{"num_ctx": o.Model.ContextTokens, "num_thread": o.Model.Threads}}
	if err := o.request(ctx, "POST", "/api/embed", input, &output); err != nil {
		return nil, err
	}
	if len(output.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("embedding count mismatch: got %d, want %d", len(output.Embeddings), len(inputs))
	}
	for _, vector := range output.Embeddings {
		if _, err := vectordata.NormalizeModelVector(vector, vectordata.VectorProperty{Dimensions: o.Model.Dimensions, Metric: vectordata.DistanceCosine}); err != nil {
			return nil, fmt.Errorf("embedding validation: %w", err)
		}
	}
	return output.Embeddings, nil
}
