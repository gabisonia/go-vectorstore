package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func testApplication(t *testing.T) (*application, http.Handler) {
	t.Helper()
	store, err := memory.NewWithOptions(memory.StoreOptions{Embedder: fixtureEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := vectordata.EnsureTypedModelCollection[string, Document](t.Context(), store, "documents", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Upsert(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	app, err := newApplication(docs, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return app, app.handler()
}

func requestJSON(t *testing.T, handler http.Handler, method, path, body string, want int, out any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != want {
		t.Fatalf("%s %s: status %d (want %d), body %s", method, path, response.Code, want, response.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(response.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVectorSearchAndCategoryFilter(t *testing.T) {
	_, handler := testApplication(t)
	var result struct {
		Results []searchHit `json:"results"`
	}
	requestJSON(t, handler, "GET", "/api/search?q=sqlite+backup&top=1", "", 200, &result)
	if len(result.Results) != 1 || result.Results[0].Document.ID != "seed-sqlite" || result.Results[0].Distance == nil || result.Results[0].Document.Vector != nil {
		t.Fatalf("search result: %+v", result)
	}
	requestJSON(t, handler, "GET", "/api/search?q=sqlite+backup&category=observability&top=1", "", 200, &result)
	if len(result.Results) != 1 || result.Results[0].Document.Category != "observability" {
		t.Fatalf("filter did not precede limit: %+v", result)
	}
	requestJSON(t, handler, "GET", "/api/search?q=sqlite&category=custom", "", 200, &result)
	if len(result.Results) != 0 {
		t.Fatal("empty category returned data")
	}
}

func TestDocumentLifecycleAndValidation(t *testing.T) {
	app, handler := testApplication(t)
	for _, body := range []string{`{"title":"","category":"custom","body":"SQLite"}`, `{"title":"note","category":"unknown","body":"SQLite"}`, `{"title":"note","category":"custom","body":"SQLite","extra":true}`, `{"title":"note","category":"custom","body":"SQLite"} {}`} {
		requestJSON(t, handler, "POST", "/api/documents", body, 400, nil)
	}
	if count, err := app.documents.Count(t.Context(), nil); err != nil || count != int64(len(catalog)) {
		t.Fatalf("invalid writes changed collection: %d %v", count, err)
	}
	var note Document
	requestJSON(t, handler, "POST", "/api/documents", `{"title":"My backup note","category":"custom","body":"SQLite WAL backup restore"}`, 201, &note)
	if !strings.HasPrefix(note.ID, "note-") {
		t.Fatalf("note ID: %s", note.ID)
	}
	stored, err := app.documents.Get(t.Context(), note.ID, vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || len(stored.Vector) != dimensions {
		t.Fatalf("automatic embedding: %+v %v", stored, err)
	}
	var result struct {
		Results []searchHit `json:"results"`
	}
	requestJSON(t, handler, "GET", "/api/search?q=sqlite+backup&category=custom", "", 200, &result)
	if len(result.Results) != 1 || result.Results[0].Document.ID != note.ID {
		t.Fatal("new note was not searchable")
	}
	requestJSON(t, handler, "DELETE", "/api/documents/seed-sqlite", "", 400, nil)
	requestJSON(t, handler, "DELETE", "/api/documents/"+note.ID, "", 204, nil)
	requestJSON(t, handler, "DELETE", "/api/documents/"+note.ID, "", 404, nil)
	if _, err := app.documents.Get(t.Context(), "seed-sqlite", vectordata.RetrievalOptions{}); err != nil {
		t.Fatal("seed catalog was removed")
	}
}

func TestInvalidSearchAndUnavailableDatabase(t *testing.T) {
	app, handler := testApplication(t)
	for _, path := range []string{"/api/search?q=", "/api/search?q=x&top=0", "/api/search?q=x&top=11", "/api/search?q=x&top=no", "/api/search?q=x&mode=ann", "/api/search?q=x&category=unknown"} {
		requestJSON(t, handler, "GET", path, "", 400, nil)
	}
	app.ping = func(context.Context) error { return errors.New("private database detail") }
	req := httptest.NewRequest("GET", "/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 503 || strings.Contains(response.Body.String(), "private") {
		t.Fatalf("unhealthy response: %d %s", response.Code, response.Body.String())
	}
}

func TestEmbeddedUIAndCanceledEmbedding(t *testing.T) {
	_, handler := testApplication(t)
	req := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Search what you know.") {
		t.Fatal("embedded UI unavailable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (fixtureEmbedder{}).Embed(ctx, []string{"SQLite"}); !errors.Is(err, context.Canceled) {
		t.Fatal("embedding cancellation lost:", err)
	}
}
