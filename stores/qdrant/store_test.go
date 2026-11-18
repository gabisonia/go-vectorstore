package qdrant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestConfigurationAndCapabilities(t *testing.T) {
	for _, opts := range []StoreOptions{
		{}, {URL: "file:///tmp/database"}, {URL: "https://secret@example.com"},
		{URL: "http://localhost?api-key=secret"}, {URL: "http://localhost", Limits: Limits{MaxRecords: -1}},
		{URL: "http://localhost?"},
		{URL: "http://localhost", Limits: Limits{MaxReceipts: 1}},
	} {
		if _, err := New(opts); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("accepted invalid options: %v", err)
		}
	}
	s, err := New(StoreOptions{URL: "http://localhost:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	caps := s.Capabilities()
	if caps.Backend != "qdrant" || !caps.Metadata || !caps.InputEmbedding || !caps.ReadIterators || caps.HybridSearch || len(caps.VectorIndexCapabilities) != 0 {
		t.Fatalf("unsupported capability claims: %+v", caps)
	}
	caps.KeyTypes[0] = "mutated"
	if s.Capabilities().KeyTypes[0] == "mutated" {
		t.Fatal("capabilities alias store state")
	}
	_, err = s.GetModelCollection("records", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "V", Kind: vectordata.VectorBinary, Dimensions: 8}}}, vectordata.ModelCollectionOptions{})
	if !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatal(err)
	}
	if s.physical("x") == s.physical("x/../other") {
		t.Fatal("physical names not distinct")
	}
}

func TestHTTPRejectsRedirectAndDoesNotExposePayload(t *testing.T) {
	var forwarded bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	s, err := New(StoreOptions{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.request(t.Context(), "PUT", "/collections/x", map[string]any{"secret": "private document"}, nil)
	if err == nil || forwarded || err.Error() != "Qdrant HTTP 307" {
		t.Fatalf("redirect followed or payload exposed: %v %t", err, forwarded)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.request(ctx, "GET", "/", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUnknownOutcomePreservesCause(t *testing.T) {
	err := unknown("receipt", context.DeadlineExceeded)
	var outcome *OutcomeUnknownError
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &outcome) || outcome.Token != "receipt" {
		t.Fatal(err)
	}
}

func TestCloseRejectsNewIO(t *testing.T) {
	s, err := New(StoreOptions{URL: "http://localhost:1", Namespace: "closed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = s.CollectionExists(t.Context(), "records")
	var operation *vectordata.OperationError
	if !errors.Is(err, ErrClosed) || !errors.As(err, &operation) || operation.Namespace != "closed" {
		t.Fatal(err)
	}
}
