package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbeddingProtocolErrors(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
	}{{429, `private content`}, {200, `{`}, {200, `{"embeddings":[]}`}, {200, `{"embeddings":[[1]]}`}, {200, `{"embeddings":[[0,0]]}`}} {
		t.Run(fmt.Sprint(test.status, test.body), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); fmt.Fprint(w, test.body) }))
			defer server.Close()
			e := localEmbedder{URL: server.URL, Model: "fixture", Dimensions: 2, Client: server.Client()}
			if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}
func TestEmbeddingInFlightCancellation(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	e := localEmbedder{URL: server.URL, Model: "fixture", Dimensions: 2, Client: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.Embed(ctx, []string{"x"}); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no HTTP request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request ignored cancellation")
	}
}
