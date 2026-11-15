package demo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEmbeddingResponseValidation(t *testing.T) {
	for name, body := range map[string]string{
		"count":           `{"data":[]}`,
		"duplicate_index": `{"data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}]}`,
		"out_of_range":    `{"data":[{"index":2,"embedding":[1,0]},{"index":1,"embedding":[0,1]}]}`,
		"dimensions":      `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[0,1]}]}`,
		"bad_json":        `{`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			client := OpenAI{BaseURL: server.URL, Dimensions: 2, HTTP: server.Client()}
			if _, err := client.Embed(context.Background(), []string{"a", "b"}); err == nil {
				t.Fatal("accepted malformed response")
			}
		})
	}
}
func TestHTTPFailureAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "private-body")
	}))
	defer server.Close()
	client := OpenAI{BaseURL: server.URL, Dimensions: 2, HTTP: server.Client()}
	if _, err := client.Embed(context.Background(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "HTTP 429") || strings.Contains(err.Error(), "private-body") {
		t.Fatalf("HTTP error: %v", err)
	}
	entered := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer slow.Close()
	client.BaseURL = slow.URL
	client.HTTP = slow.Client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Embed(ctx, []string{"a"}); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach stand-in")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request ignored cancellation")
	}
}
