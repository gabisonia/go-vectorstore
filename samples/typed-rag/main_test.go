package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/samples/internal/demo"
)

func TestFixtureRun(t *testing.T) {
	for _, args := range [][]string{nil, {"-sqlite", filepath.Join(t.TempDir(), "rag.db")}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "[costs] Reducing cloud costs") || !strings.Contains(out.String(), "Fixture mode:") {
			t.Fatalf("missing cited sources: %s", &out)
		}
	}
	if err := run(context.Background(), []string{"-top", "0"}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted top=0")
	}
}
func TestOpenAIRunAgainstHTTPStandIn(t *testing.T) {
	var batches, chat int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-only" {
			t.Error("incorrect HTTP method/auth")
			http.Error(w, "bad request", 400)
			return
		}
		switch r.URL.Path {
		case "/v1/embeddings":
			batches++
			var request struct {
				Input      []string `json:"input"`
				Dimensions int      `json:"dimensions"`
				Encoding   string   `json:"encoding_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "bad JSON", 400)
				return
			}
			if len(request.Input) > 2 || request.Dimensions != 3 || request.Encoding != "float" {
				t.Errorf("invalid embedding request: %+v", request)
			}
			vectors, err := demo.DeterministicEmbedder().Embed(r.Context(), request.Input)
			if err != nil {
				t.Error(err)
				return
			}
			data := make([]map[string]any, len(vectors))
			for i := range vectors {
				j := len(vectors) - 1 - i
				data[i] = map[string]any{"index": j, "embedding": vectors[j]}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/v1/chat/completions":
			chat++
			var request struct {
				Messages []struct{ Role, Content string }
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, "untrusted data") || !strings.Contains(request.Messages[1].Content, "[costs]") {
				t.Errorf("generation missing grounded sources: %+v", request)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Use caching and batch writes [costs]."}}]}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "unmodeled endpoint", 501)
		}
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "test-only")
	t.Setenv("OPENAI_BASE_URL", server.URL+"/v1")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-openai", "-dimensions", "3"}, &out); err != nil {
		t.Fatal(err)
	}
	if batches != 3 || chat != 1 || !strings.Contains(out.String(), "Answer:\nUse caching and batch writes [costs].") {
		t.Fatalf("requests/result: batches=%d chat=%d output=%s", batches, chat, &out)
	}
}
